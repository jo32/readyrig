package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxFileBytes bounds files written, edited or returned as base64.
const MaxFileBytes = 1024 * 1024

const (
	defaultReadLines = 2000
	maxReadBytes     = 128 * 1024
	maxLineBytes     = 2000
	countBudget      = 16 << 20
	spillPrefix      = "spill:"
)

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}
var spillName = regexp.MustCompile(`^[0-9a-f]{24}\.(stdout|stderr)$`)

// os.Root resolves every operation beneath an open directory handle, including symlinks.
type Files struct {
	root     *os.Root
	Projects *Projects
	// SpillDir holds full output of long commands; read_file reads it as spill:<name>.
	SpillDir string
}

func NewFiles(path string) (*Files, error) {
	root, err := os.OpenRoot(path)
	return &Files{root: root}, err
}
func (f *Files) Close() error { return f.root.Close() }
func (f *Files) resolve(session, project, path string) (*os.Root, string, func(), error) {
	if f.Projects != nil {
		return f.Projects.Resolve(session, project, path)
	}
	if project != "" {
		return nil, "", nil, errors.New("projects unavailable")
	}
	if err := pathOK(path); err != nil {
		return nil, "", nil, err
	}
	return f.root, path, func() {}, nil
}

// open resolves a project path, or a spill:<name> reference to saved command output.
func (f *Files) open(session, project, path string) (*os.Root, string, func(), error) {
	if !strings.HasPrefix(path, spillPrefix) {
		return f.resolve(session, project, path)
	}
	name := strings.TrimPrefix(path, spillPrefix)
	if f.SpillDir == "" || !spillName.MatchString(name) {
		return nil, "", nil, errors.New("unknown spill file")
	}
	root, err := os.OpenRoot(f.SpillDir)
	if err != nil {
		return nil, "", nil, errors.New("unknown spill file")
	}
	return root, name, func() { root.Close() }, nil
}

// tagged adds the project a result's path belongs to.
func (f *Files) tagged(run Handler) Handler {
	return func(ctx context.Context, in Invocation) (Output, error) {
		out, err := run(ctx, in)
		tagProject(f.Projects, out, "resolved_path")
		return out, err
	}
}
func enum(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}
func limited(kind, description string, min, max int) map[string]any {
	return map[string]any{"type": kind, "description": description, "minimum": min, "maximum": max}
}

var projectProp = Prop("string", "Project id or name (rarely needed)")

func (f *Files) Register(r *Registry) {
	r.Register(Tool{Spec: Spec{Name: "read_file", Category: "files", Description: "Read a text file as numbered lines, or an image (PNG, JPEG, GIF, WebP) as a picture you can see. Text: up to 2000 lines or 128 KiB per call; a longer file reports the start_line that continues it, so page with start_line/end_line/limit. Images are scaled to max_size px (default 1568); region zooms in. Use encoding=base64 only for other binary data. Paths are project-relative or absolute inside a project; spill:<name> reads saved command output.", Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "File path, or spill:<name> for saved command output"), "start_line": limited("integer", "First line to return, 1-based (default 1)", 0, 1<<31-1), "end_line": limited("integer", "Last line to return, inclusive", 0, 1<<31-1), "limit": limited("integer", "Maximum number of lines to return (default 2000)", 0, 1<<31-1), "encoding": enum("utf8 (default) or base64", "utf8", "base64"), "max_size": limited("integer", "Image long edge, px", minImageEdge, maxImageEdge), "region": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Image area [x1,y1,x2,y2] in px"}}, "path")}, Run: f.tagged(f.read)})
	r.Register(Tool{Spec: Spec{Name: "write_file", Category: "files", Description: "Create or replace a file (atomic, creates parent directories, keeps permissions). For partial changes use edit_file. create_only=true refuses to overwrite. Max 1 MiB.", Mutating: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "File path"), "content": Prop("string", "Text or base64 content"), "encoding": enum("utf8 (default) or base64", "utf8", "base64"), "create_only": Prop("boolean", "Fail if the file already exists")}, "path", "content")}, Run: f.tagged(f.write)})
	r.Register(Tool{Spec: Spec{Name: "edit_file", Category: "files", Description: "Replace old_string with new_string in an existing file. old_string must match exactly once (add surrounding lines to make it unique) unless replace_all=true. Returns the first changed line and a snippet.", Mutating: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "File path"), "old_string": Prop("string", "Exact text to find (non-empty)"), "new_string": Prop("string", "Replacement text; may be empty to delete"), "replace_all": Prop("boolean", "Replace every occurrence instead of requiring a unique match")}, "path", "old_string", "new_string")}, Run: f.tagged(f.edit)})
	r.Register(Tool{Spec: Spec{Name: "list_directory", Category: "files", Description: "List a directory in name order. depth>1 recurses (max 8) and then skips .gitignore'd paths, .git and node_modules unless no_ignore. pattern is a glob ('*.go', 'src/**/*.ts'); type=file|dir; sort=name|modified|size; limit defaults to 1000.", Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "Directory, default ."), "depth": limited("integer", "Levels to list, default 1", 1, 8), "pattern": Prop("string", "Glob filter; without a slash it matches names at any depth"), "type": enum("Only files or only directories", "file", "dir"), "sort": enum("Order of results, default name", "name", "modified", "size"), "limit": limited("integer", "Maximum entries, default 1000", 1, 5000), "no_ignore": Prop("boolean", "Do not skip ignored paths when recursing")})}, Run: f.tagged(f.list)})
	r.Register(Tool{Spec: Spec{Name: "glob", Category: "files", Description: "Find files by glob relative to path ('**/*.go', 'src/**/test_*.py', '*.{md,txt}'). A pattern without a slash matches one level only. Skips ignored paths unless no_ignore.", Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "pattern": Prop("string", "Glob pattern"), "path": Prop("string", "Directory to search, default ."), "type": enum("file (default), dir or any", "file", "dir", "any"), "sort": enum("Order of results, default name", "name", "modified", "size"), "limit": limited("integer", "Maximum paths, default 500", 1, 5000), "no_ignore": Prop("boolean", "Do not skip ignored paths")}, "pattern")}, Run: f.tagged(f.glob)})
	r.Register(Tool{Spec: Spec{Name: "search_files", Category: "files", Description: "Search file contents (grep -rn). Literal unless regex=true (RE2). include is a file glob; context adds lines around matches; case_insensitive; output=files lists paths only. Skips ignored, binary and >1 MiB files. Returns at most max_results (default 200); when truncated pass next_offset as offset.", Parallel: true, InputSchema: Schema(map[string]any{"project": projectProp, "path": Prop("string", "File or directory, default ."), "query": Prop("string", "Text or regular expression to find"), "regex": Prop("boolean", "Treat query as a regular expression"), "case_insensitive": Prop("boolean", "Ignore letter case"), "include": Prop("string", "Glob of files to search"), "context": limited("integer", "Lines of context before and after each match (0-5)", 0, 5), "max_results": limited("integer", "Maximum matches, default 200", 1, 1000), "offset": limited("integer", "Matches to skip, from a previous next_offset", 0, 1<<31-1), "output": enum("lines (default) or files", "lines", "files"), "no_ignore": Prop("boolean", "Search ignored paths too")}, "query")}, Run: f.tagged(f.search)})
}
func pathOK(p string) error {
	if p == "" || filepath.IsAbs(p) || !filepath.IsLocal(p) {
		return &ToolError{"outside_project", "path must stay inside the workspace"}
	}
	return nil
}

// readLine returns the next line without its terminator, cut to max bytes. ok
// is false at end of input.
func readLine(br *bufio.Reader, max int) (line string, ok bool, err error) {
	var buf []byte
	cut, any := false, false
	for {
		chunk, e := br.ReadSlice('\n')
		if len(chunk) > 0 {
			any = true
			if room := max - len(buf); room > 0 {
				buf = append(buf, chunk[:min(room, len(chunk))]...)
				if len(chunk) > room {
					cut = true
				}
			} else {
				cut = true
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil && e != io.EOF {
			return "", false, e
		}
		if !any {
			return "", false, nil
		}
		s := strings.TrimRight(string(buf), "\r\n")
		if cut {
			s = strings.ToValidUTF8(s, "") + " …[line truncated]"
		}
		return strings.ToValidUTF8(s, "�"), true, nil
	}
}
func numbered(first int, lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", first+i, l)
	}
	return b.String()
}
func (f *Files) read(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path     string    `json:"path"`
		Project  string    `json:"project"`
		Start    int       `json:"start_line"`
		End      int       `json:"end_line"`
		Limit    int       `json:"limit"`
		Encoding string    `json:"encoding"`
		MaxSize  int       `json:"max_size"`
		Region   []float64 `json:"region"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if a.Start < 0 || a.End < 0 || a.Limit < 0 || a.End > 0 && a.End < a.Start {
		return Output{}, errors.New("invalid line range")
	}
	if a.Encoding != "" && a.Encoding != "utf8" && a.Encoding != "base64" {
		return Output{}, errors.New("encoding must be utf8 or base64")
	}
	root, path, release, err := f.open(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	defer release()
	file, err := openRead(root, path)
	if err != nil {
		return Output{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Output{}, err
	}
	if !info.Mode().IsRegular() {
		return Output{}, errors.New("only regular files can be read")
	}
	resolved := filepath.Join(root.Name(), path)
	mime, isImage := imageTypes[strings.ToLower(filepath.Ext(path))]
	if (a.MaxSize != 0 || len(a.Region) > 0) && (!isImage || a.Encoding != "") {
		return Output{}, errors.New("max_size and region apply only to images read without encoding")
	}
	if isImage && a.Encoding == "" {
		if info.Size() > maxImageInput {
			return Output{}, fmt.Errorf("image larger than %d MiB", maxImageInput>>20)
		}
		b, err := io.ReadAll(io.LimitReader(file, maxImageInput+1))
		if err != nil {
			return Output{}, err
		}
		img, err := prepareImage(b, mime, imageRequest{MaxSize: a.MaxSize, Region: a.Region})
		if err != nil {
			return Output{}, err
		}
		value := map[string]any{"resolved_path": resolved, "size": info.Size(), "mime": img.MIME, "image": true}
		if img.Width > 0 {
			value["image_size"] = []int{img.Width, img.Height}
		}
		if img.Changed {
			value["original_size"] = []int{img.OrigW, img.OrigH}
			value["sent_bytes"] = len(img.Data)
		}
		return Output{Value: value, Images: []Image{{MIME: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Data)}}}, ctx.Err()
	}
	if a.Encoding == "base64" {
		b, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
		if err != nil {
			return Output{}, err
		}
		truncated := len(b) > MaxFileBytes
		if truncated {
			b = b[:MaxFileBytes]
		}
		return Output{Value: map[string]any{"resolved_path": resolved, "content": base64.StdEncoding.EncodeToString(b), "size": info.Size(), "truncated": truncated, "encoding": a.Encoding}}, ctx.Err()
	}
	br := bufio.NewReaderSize(file, 64*1024)
	head, _ := br.Peek(8192)
	if bytes.IndexByte(head, 0) >= 0 {
		return Output{}, &ToolError{"binary_file", "binary file: use encoding=base64"}
	}
	start := max(a.Start, 1)
	end := start + defaultReadLines - 1
	if a.End > 0 {
		end = a.End
	}
	if a.Limit > 0 {
		end = min(end, start+a.Limit-1)
	}
	var window []string
	used, lineNo, scanned, next := 0, 0, 0, 0
	complete := true
	for {
		if lineNo%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
		}
		line, ok, err := readLine(br, maxLineBytes)
		if err != nil {
			return Output{}, err
		}
		if !ok {
			break
		}
		lineNo++
		scanned += len(line) + 1
		if lineNo < start {
			continue
		}
		if next == 0 {
			switch {
			case lineNo > end:
				next = lineNo
			case used+len(line)+8 > maxReadBytes && len(window) > 0:
				next = lineNo
			default:
				window = append(window, line)
				used += len(line) + 8
			}
		}
		if next != 0 && scanned > countBudget {
			complete = false
			break
		}
	}
	value := map[string]any{"resolved_path": resolved, "content": strings.Join(window, "\n"), "size": info.Size(), "truncated": next != 0, "encoding": a.Encoding, "start_line": start}
	if len(window) > 0 {
		value["end_line"] = start + len(window) - 1
	}
	if complete {
		value["total_lines"] = lineNo
	}
	if next != 0 {
		value["next_start_line"] = next
	}
	text := numbered(start, window)
	switch {
	case lineNo == 0:
		text = "[empty file]"
	case len(window) == 0:
		text = fmt.Sprintf("[start_line %d is past the end of the file (%d lines)]", start, lineNo)
	case next != 0:
		total := ""
		if complete {
			total = fmt.Sprintf(" of %d", lineNo)
		}
		text += fmt.Sprintf("[showing lines %d-%d%s; continue with start_line=%d]", start, start+len(window)-1, total, next)
	}
	return Output{Value: value, Text: text, TextKeys: []string{"content"}}, nil
}

// writeAtomic replaces path through a temporary file so readers never see a partial write.
func writeAtomic(ctx context.Context, root *os.Root, path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := root.MkdirAll(dir, 0755); err != nil {
		return err
	}
	temp := filepath.Join(dir, ".adapter-"+ID())
	out, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = out.Write(b)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = root.Chmod(temp, mode); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return root.Rename(temp, path)
}
func (f *Files) write(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path, Project, Content, Encoding string
		CreateOnly                       bool `json:"create_only"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	root, path, release, err := f.resolve(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	defer release()
	a.Path = path
	if a.Encoding != "" && a.Encoding != "utf8" && a.Encoding != "base64" {
		return Output{}, errors.New("invalid encoding")
	}
	b := []byte(a.Content)
	if a.Encoding == "base64" {
		var err error
		b, err = base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			return Output{}, err
		}
	}
	if len(b) > MaxFileBytes {
		return Output{}, &ToolError{"too_large", "file exceeds 1 MiB"}
	}
	mode := os.FileMode(0644)
	existed := false
	if info, err := root.Stat(a.Path); err == nil {
		existed = true
		mode = info.Mode().Perm()
		if a.CreateOnly {
			return Output{}, &ToolError{"file_exists", "file already exists (create_only)"}
		}
		if info.IsDir() {
			return Output{}, &ToolError{"is_directory", "path is a directory"}
		}
	}
	if err := writeAtomic(ctx, root, a.Path, b, mode); err != nil {
		return Output{}, err
	}
	verb := "wrote"
	if !existed {
		verb = "created"
	}
	return Output{Value: map[string]any{"path": a.Path, "resolved_path": filepath.Join(root.Name(), a.Path), "bytes_written": len(b), "created": !existed}, Text: fmt.Sprintf("%s %s (%d bytes)", verb, a.Path, len(b))}, nil
}
func (f *Files) edit(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path       string `json:"path"`
		Project    string `json:"project"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if a.Old == "" {
		return Output{}, errors.New("old_string must not be empty; use write_file to create a file")
	}
	if a.Old == a.New {
		return Output{}, errors.New("old_string and new_string are identical")
	}
	root, path, release, err := f.resolve(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	defer release()
	file, err := openRead(root, path)
	if err != nil {
		return Output{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return Output{}, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return Output{}, errors.New("only regular files can be edited")
	}
	b, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	file.Close()
	if err != nil {
		return Output{}, err
	}
	if len(b) > MaxFileBytes {
		return Output{}, &ToolError{"too_large", "file exceeds 1 MiB; edit_file cannot change it"}
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return Output{}, &ToolError{"binary_file", "binary or non-UTF-8 file cannot be edited as text"}
	}
	src, oldText, newText := string(b), a.Old, a.New
	count := strings.Count(src, oldText)
	if count == 0 && strings.Contains(src, "\r\n") && !strings.Contains(oldText, "\r\n") {
		// The file uses CRLF line endings but the agent sent LF.
		crlfOld := strings.ReplaceAll(oldText, "\n", "\r\n")
		if n := strings.Count(src, crlfOld); n > 0 {
			oldText, newText, count = crlfOld, strings.ReplaceAll(newText, "\n", "\r\n"), n
		}
	}
	if count == 0 {
		msg := "old_string not found in the file"
		if trimmed := strings.TrimSpace(oldText); trimmed != "" && strings.Contains(src, trimmed) {
			msg += " (the text exists but whitespace or indentation differs)"
		}
		return Output{}, &ToolError{"no_match", msg}
	}
	first := strings.Index(src, oldText)
	if count > 1 && !a.ReplaceAll {
		var lines []string
		for from := 0; len(lines) < 5; {
			i := strings.Index(src[from:], oldText)
			if i < 0 {
				break
			}
			lines = append(lines, fmt.Sprint(strings.Count(src[:from+i], "\n")+1))
			from += i + len(oldText)
		}
		return Output{}, &ToolError{"ambiguous_match", fmt.Sprintf("old_string matches %d times (lines %s); add surrounding context to make it unique or set replace_all=true", count, strings.Join(lines, ", "))}
	}
	limit := 1
	if a.ReplaceAll {
		limit = -1
	}
	out := strings.Replace(src, oldText, newText, limit)
	if len(out) > MaxFileBytes {
		return Output{}, &ToolError{"too_large", "result exceeds 1 MiB"}
	}
	if err := writeAtomic(ctx, root, path, []byte(out), info.Mode().Perm()); err != nil {
		return Output{}, err
	}
	firstLine := strings.Count(src[:first], "\n") + 1
	lines := strings.Split(out, "\n")
	from := max(firstLine-3, 1)
	to := min(firstLine+strings.Count(newText, "\n")+3, len(lines))
	replacements := 1
	if a.ReplaceAll {
		replacements = count
	}
	snippet := numbered(from, lines[from-1:to])
	return Output{Value: map[string]any{"path": path, "resolved_path": filepath.Join(root.Name(), path), "replacements": replacements, "bytes_written": len(out), "first_line": firstLine, "snippet": snippet}, Text: fmt.Sprintf("edited %s: %d replacement(s), first at line %d\n%s", path, replacements, firstLine, snippet)}, nil
}

// entryInfo is one row of list_directory and glob output.
type entryInfo struct {
	Name     string
	Dir      bool
	Size     int64
	Modified time.Time
	Symlink  bool
}

func (e entryInfo) value() map[string]any {
	return map[string]any{"name": e.Name, "directory": e.Dir, "size": e.Size, "modified": e.Modified, "symlink": e.Symlink}
}
func sortEntries(entries []entryInfo, by string) {
	switch by {
	case "modified":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Modified.After(entries[j].Modified) })
	case "size":
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Size > entries[j].Size })
	}
}
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
func entriesText(entries []entryInfo) string {
	var b strings.Builder
	for _, e := range entries {
		kind, size, name := "f", humanSize(e.Size), e.Name
		if e.Dir {
			kind, size, name = "d", "-", e.Name+"/"
		}
		if e.Symlink {
			kind = "l"
		}
		fmt.Fprintf(&b, "%s %9s  %s  %s\n", kind, size, e.Modified.Local().Format("2006-01-02 15:04"), name)
	}
	if b.Len() == 0 {
		return "[no entries]"
	}
	return b.String()
}

type listOptions struct {
	session                             string
	project, path, pattern, typ, sortBy string
	depth, limit                        int
	noIgnore, anywhere                  bool
}

func (f *Files) listEntries(ctx context.Context, o listOptions) (Output, error) {
	if o.path == "" {
		o.path = "."
	}
	root, path, release, err := f.resolve(o.session, o.project, o.path)
	if err != nil {
		return Output{}, err
	}
	defer release()
	if o.depth == 0 {
		o.depth = 1
	}
	if o.limit == 0 {
		o.limit = 1000
	}
	var g glob
	if o.pattern != "" {
		if g = compileGlob(o.pattern, o.anywhere); len(g) == 0 {
			return Output{}, errors.New("pattern is empty")
		}
	}
	recursive := o.depth > 1
	var all []entryInfo
	cut := false
	err = walkTree(ctx, root, path, o.depth, recursive && !o.noIgnore, func(it walkItem) (bool, error) {
		isDir := it.Entry.IsDir()
		if g != nil && !g.match(it.Rel) || o.typ == "file" && isDir || o.typ == "dir" && !isDir {
			return true, nil
		}
		info, err := it.Entry.Info()
		if err != nil {
			return true, nil
		}
		if len(all) >= 20000 {
			cut = true
			return false, errWalkBudget
		}
		size := info.Size()
		if isDir {
			size = 0
		}
		all = append(all, entryInfo{Name: it.Rel, Dir: isDir, Size: size, Modified: info.ModTime(), Symlink: it.Entry.Type()&os.ModeSymlink != 0})
		return true, nil
	})
	if errors.Is(err, errWalkBudget) {
		cut, err = true, nil
	}
	if err != nil {
		return Output{}, err
	}
	sortEntries(all, o.sortBy)
	if len(all) > o.limit {
		all, cut = all[:o.limit], true
	}
	out := make([]map[string]any, 0, len(all))
	for _, e := range all {
		out = append(out, e.value())
	}
	text := entriesText(all)
	if cut {
		text += fmt.Sprintf("[truncated at %d entries; narrow the path or pattern]\n", len(all))
	}
	return Output{Value: map[string]any{"resolved_path": filepath.Join(root.Name(), path), "entries": out, "truncated": cut}, Text: text, TextKeys: []string{"entries"}}, ctx.Err()
}
func (f *Files) list(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path, Project, Pattern, Type, Sort string
		Depth, Limit                       int
		NoIgnore                           bool `json:"no_ignore"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if a.Depth < 0 || a.Depth > 8 || a.Limit < 0 || a.Limit > 5000 {
		return Output{}, errors.New("depth must be 1-8 and limit 1-5000")
	}
	return f.listEntries(ctx, listOptions{session: in.Session, project: a.Project, path: a.Path, pattern: a.Pattern, typ: a.Type, sortBy: a.Sort, depth: a.Depth, limit: a.Limit, noIgnore: a.NoIgnore, anywhere: true})
}
func (f *Files) glob(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path, Project, Pattern, Type, Sort string
		Limit                              int
		NoIgnore                           bool `json:"no_ignore"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if a.Pattern == "" {
		return Output{}, errors.New("pattern is required")
	}
	if a.Limit < 0 || a.Limit > 5000 {
		return Output{}, errors.New("limit must be 1-5000")
	}
	if a.Limit == 0 {
		a.Limit = 500
	}
	typ := a.Type
	switch typ {
	case "":
		typ = "file"
	case "any":
		typ = ""
	}
	out, err := f.listEntries(ctx, listOptions{session: in.Session, project: a.Project, path: a.Path, pattern: a.Pattern, typ: typ, sortBy: a.Sort, depth: 30, limit: a.Limit, noIgnore: a.NoIgnore})
	if err != nil {
		return out, err
	}
	// Paths only: size and time columns are noise for a pattern lookup.
	var b strings.Builder
	for _, e := range out.Value.(map[string]any)["entries"].([]map[string]any) {
		b.WriteString(e["name"].(string))
		if e["directory"] == true {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	out.Text = b.String()
	if out.Text == "" {
		out.Text = "[no matches]"
	} else if out.Value.(map[string]any)["truncated"] == true {
		out.Text += "[truncated; narrow the pattern or path]\n"
	}
	return out, nil
}

type matchLine struct {
	Path   string
	Line   int
	Text   string
	Before []string
	After  []string
}

func (m matchLine) value() map[string]any {
	v := map[string]any{"path": m.Path, "line": m.Line, "text": m.Text}
	if len(m.Before) > 0 {
		v["before"] = m.Before
	}
	if len(m.After) > 0 {
		v["after"] = m.After
	}
	return v
}
func clip(s string) string {
	if len(s) <= 1000 {
		return s
	}
	return strings.ToValidUTF8(s[:1000], "") + "…"
}
func (f *Files) search(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Path, Project, Query, Include, Output string
		Regex                                 bool
		CaseInsensitive                       bool `json:"case_insensitive"`
		Context, MaxResults, Offset           int
		NoIgnore                              bool `json:"no_ignore"`
	}
	if err := json.Unmarshal(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if a.Path == "" {
		a.Path = "."
	}
	if a.Query == "" {
		return Output{}, errors.New("query is required")
	}
	if a.Context < 0 || a.Context > 5 || a.MaxResults < 0 || a.MaxResults > 1000 || a.Offset < 0 {
		return Output{}, errors.New("context must be 0-5, max_results 1-1000, offset >= 0")
	}
	if a.Output != "" && a.Output != "lines" && a.Output != "files" {
		return Output{}, errors.New("output must be lines or files")
	}
	if a.MaxResults == 0 {
		a.MaxResults = 200
	}
	var re *regexp.Regexp
	if a.Regex {
		pattern := a.Query
		if a.CaseInsensitive {
			pattern = "(?i)" + pattern
		}
		var err error
		if re, err = regexp.Compile(pattern); err != nil {
			return Output{}, fmt.Errorf("invalid regular expression: %w", err)
		}
	}
	needle := a.Query
	if a.CaseInsensitive && re == nil {
		needle = strings.ToLower(needle)
	}
	hit := func(line string) bool {
		switch {
		case re != nil:
			return re.MatchString(line)
		case a.CaseInsensitive:
			return strings.Contains(strings.ToLower(line), needle)
		}
		return strings.Contains(line, needle)
	}
	var include glob
	if a.Include != "" {
		if include = compileGlob(a.Include, true); len(include) == 0 {
			return Output{}, errors.New("include is empty")
		}
	}
	root, path, release, err := f.resolve(in.Session, a.Project, a.Path)
	if err != nil {
		return Output{}, err
	}
	defer release()
	var matches []matchLine
	var filesHit []string
	seen, scanned, skipped := 0, 0, 0
	cut := false
	filesOnly := a.Output == "files"
	searchFile := func(rel, p string) error {
		file, e := openRead(root, p)
		if e != nil {
			return nil
		}
		b, e := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
		file.Close()
		if e != nil {
			return nil
		}
		if len(b) > MaxFileBytes {
			skipped++
			return nil
		}
		if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
			return nil
		}
		scanned++
		lines := strings.Split(string(b), "\n")
		found := false
		for i, line := range lines {
			if !hit(strings.TrimRight(line, "\r")) {
				continue
			}
			found = true
			if filesOnly {
				break
			}
			seen++
			if seen <= a.Offset {
				continue
			}
			if len(matches) >= a.MaxResults {
				cut = true
				return errWalkBudget
			}
			m := matchLine{Path: rel, Line: i + 1, Text: clip(strings.TrimRight(line, "\r"))}
			for j := max(0, i-a.Context); j < i; j++ {
				m.Before = append(m.Before, clip(strings.TrimRight(lines[j], "\r")))
			}
			for j := i + 1; j <= min(len(lines)-1, i+a.Context); j++ {
				m.After = append(m.After, clip(strings.TrimRight(lines[j], "\r")))
			}
			matches = append(matches, m)
		}
		if found && filesOnly {
			seen++
			if seen > a.Offset {
				if len(filesHit) >= a.MaxResults {
					cut = true
					return errWalkBudget
				}
				filesHit = append(filesHit, rel)
			}
		}
		return nil
	}
	info, statErr := root.Stat(path)
	if statErr != nil {
		return Output{}, fmt.Errorf("search: %w", statErr)
	}
	if info.IsDir() {
		err = walkTree(ctx, root, path, 30, !a.NoIgnore, func(it walkItem) (bool, error) {
			if it.Entry.IsDir() {
				return true, nil
			}
			if it.Entry.Type()&os.ModeSymlink != 0 || !it.Entry.Type().IsRegular() {
				return false, nil
			}
			if include != nil && !include.match(it.Rel) {
				return false, nil
			}
			return false, searchFile(filepath.Join(a.Path, it.Rel), it.Path)
		})
	} else {
		err = searchFile(a.Path, path)
	}
	if errors.Is(err, errWalkBudget) {
		cut, err = true, nil
	}
	if err != nil {
		return Output{}, fmt.Errorf("search: %w", err)
	}
	value := map[string]any{"resolved_path": filepath.Join(root.Name(), path), "truncated": cut, "scanned": scanned}
	if skipped > 0 {
		value["skipped_large"] = skipped
	}
	var text strings.Builder
	if filesOnly {
		value["files"] = filesHit
		for _, p := range filesHit {
			text.WriteString(p + "\n")
		}
		if cut {
			value["next_offset"] = a.Offset + len(filesHit)
		}
	} else {
		out := make([]map[string]any, 0, len(matches))
		for i, m := range matches {
			out = append(out, m.value())
			if a.Context > 0 && i > 0 {
				text.WriteString("--\n")
			}
			for j, l := range m.Before {
				fmt.Fprintf(&text, "%s-%d-%s\n", m.Path, m.Line-len(m.Before)+j, l)
			}
			fmt.Fprintf(&text, "%s:%d:%s\n", m.Path, m.Line, m.Text)
			for j, l := range m.After {
				fmt.Fprintf(&text, "%s-%d-%s\n", m.Path, m.Line+1+j, l)
			}
		}
		value["matches"] = out
		if cut {
			value["next_offset"] = a.Offset + len(matches)
		}
	}
	if text.Len() == 0 {
		text.WriteString("[no matches]")
	}
	if cut {
		fmt.Fprintf(&text, "[truncated; continue with offset=%d]\n", value["next_offset"])
	}
	return Output{Value: value, Text: text.String(), TextKeys: []string{"matches", "files"}}, ctx.Err()
}
