package server

import (
	"computer-use-server/internal/cloud"
	"computer-use-server/internal/harness"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivacyModeMasksPublicConsoleAndTurnsOff(t *testing.T) {
	s := fixture(t)
	projects, err := harness.NewProjects(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	root := projects.Snapshot().Projects[0].Path
	s.Projects = projects
	s.Privacy, err = harness.NewPrivacy("", func() []harness.Project { return projects.Snapshot().Projects })
	if err != nil {
		t.Fatal(err)
	}
	local, public := s.UI(), s.Gateway()
	publicState := func() string {
		t.Helper()
		w := request(public, "GET", "/app/api/state", "", s.AccessPath)
		if w.Code != 200 {
			t.Fatalf("public state: %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	if !s.Privacy.Enabled() || strings.Contains(publicState(), root) {
		t.Fatal("privacy mode must be on by default")
	}
	if err := s.Privacy.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(publicState(), root) {
		t.Fatal("the public console shows project folders while privacy mode is off")
	}

	if w := request(local, "POST", "/api/privacy", `{"enabled":true}`, ""); w.Code != 200 {
		t.Fatalf("turn on: %d %s", w.Code, w.Body)
	}
	body := publicState()
	if strings.Contains(body, root) || !strings.Contains(body, "${RR_ROOT_") || strings.Contains(body, `"tokens"`) {
		t.Fatalf("public console not masked: %s", body)
	}
	w := request(local, "GET", "/api/state", "", "")
	var state struct {
		Privacy struct {
			Enabled bool                   `json:"enabled"`
			Tokens  []harness.PrivacyToken `json:"tokens"`
		} `json:"privacy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.Privacy.Enabled || len(state.Privacy.Tokens) == 0 {
		t.Fatalf("local state: %v %s", err, w.Body)
	}

	// The local console can turn it off without resending the other settings.
	if w := request(local, "POST", "/api/privacy", `{"enabled":false}`, ""); w.Code != 200 || s.Privacy.Enabled() {
		t.Fatalf("turn off: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(publicState(), root) {
		t.Fatal("still masked after turning privacy mode off")
	}

	// So can the cloud console, through a queued command.
	for _, enabled := range []bool{true, false} {
		payload, _ := json.Marshal(map[string]bool{"enabled": enabled})
		if err := s.executeCloudCommand(cloud.Command{Kind: "privacy.set", Payload: payload}); err != nil || s.Privacy.Enabled() != enabled {
			t.Fatalf("privacy.set %v: %v", enabled, err)
		}
	}
	if err := s.executeCloudCommand(cloud.Command{Kind: "privacy.set", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("privacy.set without enabled was accepted")
	}
}
