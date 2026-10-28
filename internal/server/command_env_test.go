package server

import (
	"computer-use-server/internal/harness"
	"strings"
	"testing"
)

func TestCommandEnvToggleIsLocalOnly(t *testing.T) {
	s := fixture(t)
	var err error
	s.CommandEnv, err = harness.NewCommandEnv("")
	if err != nil {
		t.Fatal(err)
	}
	local, public := s.UI(), s.Gateway()
	if w := request(local, "GET", "/api/state", "", ""); !strings.Contains(w.Body.String(), `"command_env":{"available":true,"inherit":true}`) {
		t.Fatalf("commands must inherit the environment by default: %s", w.Body)
	}
	if w := request(public, "POST", "/app/api/command-env", `{"inherit":false}`, s.AccessPath); w.Code == 200 || !s.CommandEnv.Inherit() {
		t.Fatalf("the public console changed the setting: %d", w.Code)
	}
	if w := request(local, "POST", "/api/command-env", `{}`, ""); w.Code != 400 {
		t.Fatalf("missing inherit accepted: %d", w.Code)
	}
	if w := request(local, "POST", "/api/command-env", `{"inherit":false}`, ""); w.Code != 200 || s.CommandEnv.Inherit() {
		t.Fatalf("turn off: %d %s", w.Code, w.Body)
	}
	if w := request(local, "GET", "/api/command-env", "", ""); !strings.Contains(w.Body.String(), `"inherit":false`) {
		t.Fatalf("status: %s", w.Body)
	}
}
