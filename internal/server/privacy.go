package server

import (
	"bytes"
	"computer-use-server/internal/harness"
	"errors"
	"net/http"
)

// privacyState is the privacy section of /api/state. The public console learns
// only whether masking is on, never the values it hides.
func (s *Server) privacyState(public bool) map[string]any {
	if s.Privacy == nil {
		return map[string]any{"available": false, "enabled": false}
	}
	settings := s.Privacy.Settings()
	if public {
		return map[string]any{"available": true, "enabled": settings.Enabled}
	}
	return map[string]any{"available": true, "enabled": settings.Enabled, "mask_user": settings.MaskUser, "mask_host": settings.MaskHost, "words": settings.Words, "tokens": s.Privacy.Tokens()}
}

// Privacy settings exist only behind local dashboard auth; the cloud console uses
// the privacy.set command.
func (s *Server) privacyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/privacy", func(w http.ResponseWriter, r *http.Request) {
		write(w, s.privacyState(false))
	})
	mux.HandleFunc("POST /api/privacy", func(w http.ResponseWriter, r *http.Request) {
		// Fields left out keep their saved value, so {"enabled":false} only turns masking off.
		var in struct {
			Enabled  *bool                  `json:"enabled"`
			MaskUser *bool                  `json:"mask_user"`
			MaskHost *bool                  `json:"mask_host"`
			Words    *[]harness.PrivacyWord `json:"words"`
		}
		if !decode(w, r, &in) {
			return
		}
		if s.Privacy == nil {
			problem(w, 409, errors.New("privacy mode is unavailable"))
			return
		}
		settings := s.Privacy.Settings()
		if in.Enabled != nil {
			settings.Enabled = *in.Enabled
		}
		if in.MaskUser != nil {
			settings.MaskUser = *in.MaskUser
		}
		if in.MaskHost != nil {
			settings.MaskHost = *in.MaskHost
		}
		if in.Words != nil {
			settings.Words = *in.Words
		}
		if err := s.Privacy.Set(settings); err != nil {
			problem(w, 400, err)
			return
		}
		write(w, s.privacyState(false))
	})
}

// maskedWriter masks a public console response line by line. JSON from this
// server never escapes '/', so masking the encoded text reaches every path, and
// a line is complete before it is masked, so no value is split.
type maskedWriter struct {
	http.ResponseWriter
	privacy *harness.Privacy
	pending []byte
}

func (m *maskedWriter) Write(p []byte) (int, error) {
	m.pending = append(m.pending, p...)
	if i := bytes.LastIndexByte(m.pending, '\n'); i >= 0 {
		if _, err := m.ResponseWriter.Write([]byte(m.privacy.Redact(string(m.pending[:i+1])))); err != nil {
			return 0, err
		}
		m.pending = append([]byte(nil), m.pending[i+1:]...)
	}
	return len(p), nil
}

func (m *maskedWriter) finish() {
	if len(m.pending) > 0 {
		m.ResponseWriter.Write([]byte(m.privacy.Redact(string(m.pending))))
		m.pending = nil
	}
}

func (m *maskedWriter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
