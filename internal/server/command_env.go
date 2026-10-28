package server

import (
	"errors"
	"net/http"
)

// commandEnvState is the command_env section of /api/state.
func (s *Server) commandEnvState() map[string]any {
	return map[string]any{"available": s.CommandEnv != nil, "inherit": s.CommandEnv.Inherit()}
}

// The setting exists only behind local dashboard auth: the public console is
// read-only, and there is no cloud command for it.
func (s *Server) commandEnvRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/command-env", func(w http.ResponseWriter, r *http.Request) {
		write(w, s.commandEnvState())
	})
	mux.HandleFunc("POST /api/command-env", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Inherit *bool `json:"inherit"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Inherit == nil {
			problem(w, 400, errors.New("inherit is required"))
			return
		}
		if err := s.CommandEnv.SetInherit(*in.Inherit); err != nil {
			problem(w, 409, err)
			return
		}
		write(w, s.commandEnvState())
	})
}
