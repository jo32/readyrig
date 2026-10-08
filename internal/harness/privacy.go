package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Privacy keeps local paths and other identifying text out of what agents see.
// While it is on, each value is replaced by a ${RR_*} token in tool results, and
// tokens in tool arguments are expanded back before a tool runs. Every value has
// its own token, so reading a file, editing it and writing it again through tokens
// leaves it byte for byte unchanged.
type Privacy struct {
	mu       sync.RWMutex
	file     string
	settings PrivacySettings
	projects func() []Project
	// pairs is ordered longest value first, so a project inside the home folder
	// gets its own token rather than the home token.
	pairs  []privacyPair
	first  [256]bool
	expand *strings.Replacer
	// OnChange runs, without the lock held, after the settings change.
	OnChange func()
}

// PrivacySettings is saved in privacy.json. Privacy mode is on until the user
// turns it off; home and project folders are always masked while Enabled. The user and host names are masked by default too: they
// appear in ordinary output such as ls -l. Custom words are opt-in.
type PrivacySettings struct {
	Enabled  bool          `json:"enabled"`
	MaskUser bool          `json:"mask_user"`
	MaskHost bool          `json:"mask_host"`
	Words    []PrivacyWord `json:"words"`
}

// PrivacyWord is a custom value to mask, shown to agents as ${RR_<NAME>}.
type PrivacyWord struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PrivacyToken describes one replacement for the local console.
type PrivacyToken struct {
	Token string `json:"token"`
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

type privacyPair struct {
	token, value, kind string
	// word values match whole words only; paths match where a path name ends.
	word bool
	// prefix word values also match at the start of a longer word, as the user
	// name does in ann-laptop or anns-team.
	prefix bool
}

const (
	tokenPrefix = "${RR_"
	// dollarToken stands for a literal $ that starts the text of a token, so a file
	// that already contains token text still round-trips exactly. Agents write a
	// literal token the same way: ${RR_DOLLAR}{RR_HOME}.
	dollarToken     = tokenPrefix + "DOLLAR}"
	maxPrivacyWords = 32
)

var privacyWordName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// NewPrivacy loads the saved settings. projects lists the approved projects; call
// Rebuild when they change.
func NewPrivacy(file string, projects func() []Project) (*Privacy, error) {
	p := &Privacy{file: file, projects: projects, settings: PrivacySettings{Enabled: true, MaskUser: true, MaskHost: true}}
	if file != "" {
		b, err := os.ReadFile(file)
		if err == nil {
			if err = json.Unmarshal(b, &p.settings); err != nil {
				return nil, fmt.Errorf("privacy: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if p.settings.Words == nil {
		p.settings.Words = []PrivacyWord{}
	}
	p.Rebuild()
	return p, nil
}

// Enabled reports whether results are masked.
func (p *Privacy) Enabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settings.Enabled
}

// Settings returns a copy of the saved settings.
func (p *Privacy) Settings() PrivacySettings {
	if p == nil {
		return PrivacySettings{Words: []PrivacyWord{}}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.settings
	s.Words = append([]PrivacyWord{}, s.Words...)
	return s
}

// Tokens lists the current replacements, longest value first. It contains the
// real values, so it belongs in the local console only.
func (p *Privacy) Tokens() []PrivacyToken {
	out := []PrivacyToken{}
	if p == nil {
		return out
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, v := range p.pairs {
		out = append(out, PrivacyToken{Token: v.token, Value: v.value, Kind: v.kind})
	}
	return out
}

// SetEnabled turns masking on or off and keeps the other settings.
func (p *Privacy) SetEnabled(on bool) error {
	s := p.Settings()
	s.Enabled = on
	return p.Set(s)
}

// Set validates, saves and applies new settings.
func (p *Privacy) Set(s PrivacySettings) error {
	if p == nil {
		return errors.New("privacy mode is unavailable")
	}
	if len(s.Words) > maxPrivacyWords {
		return fmt.Errorf("at most %d custom words", maxPrivacyWords)
	}
	words := make([]PrivacyWord, 0, len(s.Words))
	names := map[string]bool{}
	for _, w := range s.Words {
		name := strings.ToUpper(strings.TrimSpace(w.Name))
		if !privacyWordName.MatchString(name) {
			return fmt.Errorf("invalid word name %q: use letters, digits and _, starting with a letter", w.Name)
		}
		if names[name] {
			return fmt.Errorf("duplicate word name %q", name)
		}
		if len(w.Value) < 3 || len(w.Value) > 256 || strings.Contains(w.Value, tokenPrefix) {
			return fmt.Errorf("the value of %s must be 3 to 256 characters", name)
		}
		names[name] = true
		words = append(words, PrivacyWord{Name: name, Value: w.Value})
	}
	s.Words = words
	var projects []Project
	if p.projects != nil {
		projects = p.projects()
	}
	p.mu.Lock()
	old := p.settings
	p.settings = s
	if err := p.save(); err != nil {
		p.settings = old
		p.mu.Unlock()
		return err
	}
	p.rebuild(projects)
	p.mu.Unlock()
	if p.OnChange != nil {
		p.OnChange()
	}
	return nil
}

func (p *Privacy) save() error {
	if p.file == "" {
		return nil
	}
	b, err := json.MarshalIndent(p.settings, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.file), ".privacy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), p.file)
}

// Rebuild recomputes the replacements, for example after the projects change.
func (p *Privacy) Rebuild() {
	if p == nil {
		return
	}
	var projects []Project
	if p.projects != nil {
		projects = p.projects()
	}
	p.mu.Lock()
	p.rebuild(projects)
	p.mu.Unlock()
}

// rebuild runs with p.mu held. Tokens are kept while masking is off so that
// tokens an agent saw earlier still expand.
func (p *Privacy) rebuild(projects []Project) {
	var pairs []privacyPair
	tokens, values := map[string]bool{}, map[string]bool{}
	add := func(name, value, kind string, word bool) {
		// Names of four or more characters are rarely the start of unrelated words.
		prefix := word && kind != "word" && len(value) >= 4
		// A volume root such as / would match every path.
		if len(value) < 3 || values[value] || !word && (value == "/" || filepath.Dir(value) == value) {
			return
		}
		token := tokenPrefix + name + "}"
		for n := 2; tokens[token]; n++ {
			token = fmt.Sprintf("%s%s_%d}", tokenPrefix, name, n)
		}
		tokens[token], values[value] = true, true
		pairs = append(pairs, privacyPair{token: token, value: value, kind: kind, word: word, prefix: prefix})
	}
	addPath := func(name, value, kind string) {
		add(name, value, kind, false)
		// JSON encoders that escape slashes print /Users/ann as \/Users\/ann. It
		// gets its own token so that it, too, expands back exactly.
		if escaped := strings.ReplaceAll(value, "/", `\/`); escaped != value {
			add(name+"_ESCAPED", escaped, kind, false)
		}
	}
	for _, v := range projects {
		addPath("ROOT_"+tokenName(v.Name), v.Path, "project")
	}
	if home, err := os.UserHomeDir(); err == nil {
		addPath("HOME", filepath.Clean(home), "home")
	}
	if p.settings.MaskUser {
		if u, err := user.Current(); err == nil {
			name := u.Username
			// Windows reports DOMAIN\name.
			if i := strings.LastIndexByte(name, '\\'); i >= 0 {
				name = name[i+1:]
			}
			add("USER", name, "user", true)
		}
	}
	if p.settings.MaskHost {
		if host, err := os.Hostname(); err == nil {
			add("HOST", host, "host", true)
			if short, _, ok := strings.Cut(host, "."); ok {
				add("HOST_SHORT", short, "host", true)
			}
		}
	}
	for _, w := range p.settings.Words {
		add(w.Name, w.Value, "word", true)
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].value) > len(pairs[j].value) })
	p.pairs = pairs
	p.first = [256]bool{'$': true}
	expand := make([]string, 0, 2*len(pairs)+2)
	expand = append(expand, dollarToken, "$")
	for _, v := range pairs {
		p.first[v.value[0]] = true
		expand = append(expand, v.token, v.value)
	}
	p.expand = strings.NewReplacer(expand...)
}

// tokenName turns a project name into the upper-case part of a token.
func tokenName(name string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToUpper(name) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			underscore = false
		} else if !underscore && b.Len() > 0 {
			b.WriteByte('_')
			underscore = true
		}
	}
	if out := strings.TrimSuffix(b.String(), "_"); out != "" {
		return out
	}
	return "PROJECT"
}

// Redact masks s when privacy mode is on.
func (p *Privacy) Redact(s string) string {
	if p == nil {
		return s
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.settings.Enabled || len(p.pairs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if !p.first[s[i]] {
			continue
		}
		if strings.HasPrefix(s[i:], tokenPrefix) {
			b.WriteString(s[last:i])
			b.WriteString(dollarToken)
			last = i + 1
			continue
		}
		for _, v := range p.pairs {
			if strings.HasPrefix(s[i:], v.value) && v.fits(s, i) {
				b.WriteString(s[last:i])
				b.WriteString(v.token)
				last = i + len(v.value)
				i = last - 1
				break
			}
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// fits reports whether the value found at s[i:] stands on its own: /Users/ann is
// not masked inside /Users/anna, nor the word ann inside banner.
func (v privacyPair) fits(s string, i int) bool {
	end := i + len(v.value)
	if v.word && !wordStart(s, i) {
		return false
	}
	if end == len(s) || v.prefix {
		return true
	}
	c := s[end]
	if v.word {
		return !wordByte(c)
	}
	if nameByte(c) {
		return false
	}
	// /Users/ann.old is another folder; /Users/ann. ends a sentence.
	return c != '.' || end+1 == len(s) || !nameByte(s[end+1])
}

// wordStart reports whether a word starts at s[i]. Masked text is often encoded
// JSON, where "\nann" and "\u003eann" put a letter right before the word.
func wordStart(s string, i int) bool {
	if i == 0 || !wordByte(s[i-1]) {
		return true
	}
	if i >= 2 && s[i-2] == '\\' && strings.IndexByte("bfnrt", s[i-1]) >= 0 {
		return true
	}
	if i < 6 || s[i-6] != '\\' || s[i-5] != 'u' {
		return false
	}
	for _, c := range []byte(s[i-4 : i]) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
func nameByte(c byte) bool { return wordByte(c) || c == '-' || c >= 0x80 }

// holdBack is how many bytes at the end of data could be the start of a masked
// value that more output will complete. A stream read in pieces keeps them for
// the next read, so a value is never split across two results unmasked.
func (p *Privacy) holdBack(data []byte) int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.settings.Enabled {
		return 0
	}
	n := 0
	values := []string{tokenPrefix}
	for _, v := range p.pairs {
		values = append(values, v.value)
	}
	for _, v := range values {
		for k := min(len(v), len(data)); k > n; k-- {
			if bytes.HasSuffix(data, []byte(v[:k])) {
				n = k
				break
			}
		}
	}
	return n
}

// cutFragment is how many bytes at the start of data could be the end of a
// masked value whose start was cut away, as by the middle of long output.
func (p *Privacy) cutFragment(data []byte) int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.settings.Enabled {
		return 0
	}
	n := 0
	for _, v := range p.pairs {
		for k := min(len(v.value)-1, len(data)); k > n; k-- {
			if bytes.HasPrefix(data, []byte(v.value[len(v.value)-k:])) {
				n = k
				break
			}
		}
	}
	return n
}

// RedactValue masks every string in a JSON-like value, in place where it can.
// Image data is left alone.
func (p *Privacy) RedactValue(v any) any {
	if !p.Enabled() {
		return v
	}
	return p.redactAny(v)
}
func (p *Privacy) redactAny(v any) any {
	switch x := v.(type) {
	case nil, bool, int, int64, float64, json.Number:
		return v
	case string:
		return p.Redact(x)
	case map[string]any:
		image := x["type"] == "image"
		for k, val := range x {
			if s, ok := val.(string); ok && (image && k == "data" || k == "screenshot" && strings.HasPrefix(s, "data:")) {
				continue
			}
			x[k] = p.redactAny(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = p.redactAny(x[i])
		}
		return x
	}
	// Structs and typed collections: a JSON round trip reaches every string.
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var generic any
	if json.Unmarshal(b, &generic) != nil {
		return v
	}
	return p.redactAny(generic)
}

// redactError rewords an error whose text contains masked values. errors.Is and
// errors.As still see the original.
func (p *Privacy) redactError(err error) error {
	if err == nil || !p.Enabled() {
		return err
	}
	msg := err.Error()
	if masked := p.Redact(msg); masked != msg {
		return &describedError{masked, err}
	}
	return err
}

// redactOutput masks a tool result on its way to the agent. Background results
// stay real for the audit log; agents learn of them through masked events.
func (p *Privacy) redactOutput(out *Output) {
	if !p.Enabled() {
		return
	}
	out.Value = p.redactAny(out.Value)
	out.Text = p.Redact(out.Text)
	out.Failure = p.Redact(out.Failure)
	if out.MCPResult != nil {
		if m, ok := p.redactAny(out.MCPResult).(map[string]any); ok {
			out.MCPResult = m
		}
	}
}

// redactEvent masks a notice or progress line.
func (p *Privacy) redactEvent(e Event) Event {
	if !p.Enabled() || e.Data == nil {
		return e
	}
	data := make(map[string]any, len(e.Data))
	for k, v := range e.Data {
		data[k] = v
	}
	e.Data = p.redactAny(data).(map[string]any)
	return e
}

// Expand replaces tokens in s with the values they stand for.
func (p *Privacy) Expand(s string) string {
	if p == nil || !strings.Contains(s, tokenPrefix) {
		return s
	}
	p.mu.RLock()
	r := p.expand
	p.mu.RUnlock()
	if r == nil {
		return s
	}
	return r.Replace(s)
}

// ExpandJSON expands tokens in every string of tool arguments. It works whether
// or not masking is on, so tokens an agent saw earlier keep working.
func (p *Privacy) ExpandJSON(raw json.RawMessage) json.RawMessage {
	if p == nil || !bytes.Contains(raw, []byte(tokenPrefix)) {
		return raw
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return p.Expand(x)
		case map[string]any:
			for k, val := range x {
				x[k] = walk(val)
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if e.Encode(walk(v)) != nil {
		return raw
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
