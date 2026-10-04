package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ToolHelp is a live definition plus policy status, not a cached tool catalogue.
type ToolHelp struct {
	Spec
	// InputSchema shadows Spec.InputSchema so compact listings can omit it.
	InputSchema       map[string]any `json:"inputSchema,omitempty"`
	Enabled           bool           `json:"enabled"`
	Available         bool           `json:"available"`
	UnavailableReason string         `json:"unavailable_reason,omitempty"`
	Permission        string         `json:"permission_required,omitempty"`
	slim              bool
}

// slimFlagsNote defines the omission rule for slim listings, so an absent flag is never ambiguous.
const slimFlagsNote = "Slim listing: mutating and parallel appear only when true; enabled and available only when false (absent means the opposite). Use names for full entries with schemas."

// MarshalJSON writes the full entry, or in slim mode drops default-valued flags and
// redundant annotations so a browse list stays cheap in an agent's context.
func (h ToolHelp) MarshalJSON() ([]byte, error) {
	if !h.slim {
		type full ToolHelp
		return json.Marshal(full(h))
	}
	out := struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		Category          string `json:"category"`
		Mutating          bool   `json:"mutating,omitempty"`
		Parallel          bool   `json:"parallel,omitempty"`
		Group             string `json:"group,omitempty"`
		Enabled           *bool  `json:"enabled,omitempty"`
		Available         *bool  `json:"available,omitempty"`
		UnavailableReason string `json:"unavailable_reason,omitempty"`
		Permission        string `json:"permission_required,omitempty"`
	}{Name: h.Name, Description: h.Description, Category: h.Category, Mutating: h.Mutating, Parallel: h.Parallel,
		Group: h.Group, UnavailableReason: h.UnavailableReason, Permission: h.Permission}
	if !h.Enabled {
		out.Enabled = &h.Enabled
	}
	if !h.Available {
		out.Available = &h.Available
	}
	return json.Marshal(out)
}

type HelpResult struct {
	Paused bool   `json:"paused"`
	Total  int    `json:"total"`
	Note   string `json:"note,omitempty"`
	// Unknown lists requested names that are not registered.
	Unknown []string   `json:"unknown,omitempty"`
	Tools   []ToolHelp `json:"tools"`
}

// firstSentence keeps compact listings short.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 && i < 220 {
		return s[:i+1]
	}
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}

func (r *Registry) RegisterHelp() {
	r.Register(Tool{
		Spec:                Spec{Name: "help", Category: "system", Description: "List tools with full schemas; compact=true for names and one line each (slim=true also omits default flags); name or names for specific tools; category to filter; include_disabled shows blocked tools. Advanced-group tools are not in tools/list; run them with use_tool. Works while paused.", Parallel: true, InputSchema: Schema(map[string]any{"name": Prop("string", "Exact tool name to inspect"), "names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 40, "description": "Tool names to inspect; unknown ones are returned in unknown"}, "include_disabled": Prop("boolean", "Include unavailable tools"), "compact": Prop("boolean", "Names and one-line descriptions, no schemas"), "slim": Prop("boolean", "With compact: omit default-valued flags"), "category": Prop("string", "Only this category")}), Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}},
		AvailableWhenPaused: true,
		Run: func(ctx context.Context, in Invocation) (Output, error) {
			var args struct {
				Name            string   `json:"name"`
				IncludeDisabled bool     `json:"include_disabled"`
				Compact         bool     `json:"compact"`
				Slim            bool     `json:"slim"`
				Names           []string `json:"names"`
				Category        string   `json:"category"`
			}
			if err := Decode(in.Arguments, &args); err != nil {
				return Output{}, err
			}
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if args.Name != "" {
				if _, ok := r.tools[args.Name]; !ok {
					return Output{}, fmt.Errorf("tool %q is not currently registered; call help with {} for the live list", args.Name)
				}
			}
			wanted := map[string]bool{}
			if args.Name != "" {
				wanted[args.Name] = true
			}
			result := HelpResult{Paused: r.paused, Tools: []ToolHelp{}}
			for _, name := range args.Names {
				if _, ok := r.tools[name]; !ok {
					if !wanted[name] {
						result.Unknown = append(result.Unknown, name)
					}
					continue
				}
				wanted[name] = true
			}
			// Selecting tools by name returns full schemas, even for unavailable ones, so the reason is visible.
			selected := len(wanted) > 0
			hidden := false
			for _, name := range r.order {
				if (selected && !wanted[name]) || (args.Category != "" && r.tools[name].Spec.Category != args.Category) {
					continue
				}
				t := r.tools[name]
				enabled := r.enabled[t.Spec.Category]
				h := ToolHelp{Spec: t.Spec, InputSchema: t.Spec.InputSchema, Enabled: enabled, Available: enabled && (!r.paused || t.AvailableWhenPaused)}
				if !enabled {
					h.UnavailableReason = "capability_disabled"
				} else if !h.Available {
					h.UnavailableReason = "control_paused"
				}
				if !selected && !args.IncludeDisabled && !h.Available {
					continue
				}
				// A missing OS permission keeps the tool listed but marks it unusable, with the reason.
				if h.Available && r.PermissionCheck != nil {
					if msg := r.PermissionCheck(t.Spec); msg != "" {
						h.Available, h.UnavailableReason, h.Permission = false, "permission_required", msg
					}
				}
				if t.Spec.Group == "advanced" && !r.ExposeAll {
					hidden = true
				}
				if args.Compact && !selected {
					h.InputSchema = nil
					h.Spec.InputSchema = nil
					h.Spec.Description = firstSentence(h.Spec.Description)
					h.slim = args.Slim
				}
				result.Tools = append(result.Tools, h)
			}
			result.Total = len(result.Tools)
			if hidden {
				result.Note = "Tools in group advanced are not listed by tools/list; run them with use_tool {name, arguments}."
			}
			if args.Compact && args.Slim && !selected {
				result.Note = strings.TrimSpace(result.Note + " " + slimFlagsNote)
			}
			return Output{Value: result}, nil
		},
	})
	r.Register(Tool{
		Spec: Spec{Name: "batch", Category: "system", Description: "Run several tools in one request to save round trips. calls is a list of {tool, arguments} (max 12, no nesting). A read-only batch runs in parallel, otherwise calls run in order. Each result comes back in order; a failing call does not stop the rest unless stop_on_error. Typical use: read several files and search in one call.", Mutating: true, Parallel: true, InputSchema: Schema(map[string]any{"calls": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "minItems": 1, "maxItems": maxBatch, "description": "[{tool, arguments}, ...]"}, "stop_on_error": Prop("boolean", "Skip the remaining calls after the first failure")}, "calls")},
		Run:  r.runBatch,
	})
	r.Register(Tool{
		Spec: Spec{Name: "use_tool", Category: "system", Description: "Run any tool by name, including advanced tools that tools/list does not show (see help compact). The same checks apply.", Mutating: true, Parallel: true, InputSchema: Schema(map[string]any{"name": Prop("string", "Tool name, as listed by help"), "arguments": Prop("object", "Arguments for that tool")}, "name")},
		Run: func(ctx context.Context, in Invocation) (Output, error) {
			var args struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := Decode(in.Arguments, &args); err != nil {
				return Output{}, err
			}
			if args.Name == "use_tool" {
				return Output{}, errors.New("use_tool cannot call itself")
			}
			if len(args.Arguments) == 0 {
				args.Arguments = json.RawMessage(`{}`)
			}
			out, _, err := r.Invoke(ctx, args.Name, Invocation{Session: in.Session, Client: in.Client, Arguments: args.Arguments})
			// The inner call owns its background completion; the wrapper must not repeat it.
			out.Completion, out.Snapshot, out.Cancel = nil, nil, nil
			return out, err
		},
	})
}

// maxBatch bounds the calls in one batch request.
const maxBatch = 12

type batchResult struct {
	Tool   string `json:"tool"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"error_code,omitempty"`
	Result any    `json:"result,omitempty"`
	text   string
}

// runBatch runs several tools for one request, saving a round trip per call.
// Each call is audited as usual. Read-only batches run in parallel, others in
// order. One failing call does not stop the rest unless stop_on_error is set.
func (r *Registry) runBatch(ctx context.Context, in Invocation) (Output, error) {
	var a struct {
		Calls []struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
		Stop bool `json:"stop_on_error"`
	}
	if err := Decode(in.Arguments, &a); err != nil {
		return Output{}, err
	}
	if len(a.Calls) == 0 || len(a.Calls) > maxBatch {
		return Output{}, fmt.Errorf("calls must hold 1-%d entries", maxBatch)
	}
	readOnly := true
	r.mu.Lock()
	for i, c := range a.Calls {
		if c.Tool == "" || c.Tool == "batch" {
			r.mu.Unlock()
			return Output{}, fmt.Errorf("call %d: tool is required and cannot be batch", i+1)
		}
		if t, ok := r.tools[c.Tool]; !ok || t.Spec.Mutating {
			readOnly = false
		}
	}
	r.mu.Unlock()
	results := make([]batchResult, len(a.Calls))
	var images []Image
	var imageMu sync.Mutex
	run := func(i int) {
		c := a.Calls[i]
		args := c.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		out, _, err := r.Invoke(ctx, c.Tool, Invocation{Session: in.Session, Client: in.Client, Arguments: args})
		res := batchResult{Tool: c.Tool, OK: err == nil, Result: out.Value, text: out.Text}
		if err != nil {
			res.Error, res.Code = err.Error(), ErrorCode(err)
		}
		if res.text == "" && out.Value != nil {
			if b, e := json.Marshal(out.Value); e == nil {
				res.text = string(b)
			}
		}
		if len(out.Images) > 0 {
			imageMu.Lock()
			images = append(images, out.Images...)
			imageMu.Unlock()
		}
		results[i] = res
	}
	if readOnly {
		var wg sync.WaitGroup
		for i := range a.Calls {
			wg.Add(1)
			go func() { defer wg.Done(); run(i) }()
		}
		wg.Wait()
	} else {
		for i := range a.Calls {
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
			run(i)
			if a.Stop && !results[i].OK {
				for j := i + 1; j < len(a.Calls); j++ {
					results[j] = batchResult{Tool: a.Calls[j].Tool, Error: "skipped after an earlier failure", Code: "skipped"}
				}
				break
			}
		}
	}
	var text strings.Builder
	for i, res := range results {
		fmt.Fprintf(&text, "### %d %s\n", i+1, res.Tool)
		if res.Error != "" {
			fmt.Fprintf(&text, "error [%s]: %s\n", res.Code, res.Error)
		}
		if res.text != "" {
			text.WriteString(res.text)
			if !strings.HasSuffix(res.text, "\n") {
				text.WriteByte('\n')
			}
		}
	}
	return Output{Value: map[string]any{"results": results}, Text: text.String(), TextKeys: []string{"results"}, Images: images}, nil
}
