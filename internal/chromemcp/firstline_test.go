package chromemcp

import "testing"

func TestFirstLineSurfacesTheUpstreamReason(t *testing.T) {
	result := map[string]any{"content": []any{map[string]any{"type": "text", "text": "Error: net::ERR_CONNECTION_REFUSED at http://127.0.0.1:4173/\nmore"}}}
	if got := firstLine(resultText(result), "fallback"); got != "net::ERR_CONNECTION_REFUSED at http://127.0.0.1:4173/" {
		t.Fatalf("got %q", got)
	}
	if got := firstLine("", "fallback"); got != "fallback" {
		t.Fatalf("got %q", got)
	}
}
