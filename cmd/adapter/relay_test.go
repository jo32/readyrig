package main

import (
	"computer-use-server/internal/cloud"
	"net/http"
	"strings"
	"testing"
)

func TestCloudRelayCommandNeedsExplicitConsent(t *testing.T) {
	o := &startupOptions{}
	if _, _, _, _, err := controlAction("cloud", []string{"relay", "on"}, o); err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "through the cloud server") {
		t.Fatal("relay on must explain the data transfer and ask for --yes:", err)
	}
	method, path, input, _, err := controlAction("cloud", []string{"relay", "on", "--yes"}, o)
	if err != nil || method != http.MethodPost || path != "/api/cloud/relay" {
		t.Fatal(method, path, err)
	}
	if v, ok := input.(map[string]any); !ok || v["enabled"] != true || v["acknowledged"] != true {
		t.Fatal(input)
	}
	method, path, input, _, err = controlAction("cloud", []string{"relay", "off"}, o)
	if err != nil || method != http.MethodPost || path != "/api/cloud/relay" {
		t.Fatal(method, path, err)
	}
	if v, ok := input.(map[string]any); !ok || v["enabled"] != false || v["acknowledged"] != nil {
		t.Fatal(input)
	}
	if method, path, _, _, err = controlAction("cloud", []string{"relay", "status"}, o); err != nil || method != http.MethodGet || path != "/api/cloud" {
		t.Fatal(method, path, err)
	}
	for _, args := range [][]string{{"relay"}, {"relay", "maybe"}, {"relay", "on", "--yes", "extra"}, {"relay", "off", "--yes"}, {"relay", "status", "x"}, {"relay", "on", "--no-such-flag"}} {
		if _, _, _, _, err := controlAction("cloud", args, o); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if !strings.Contains(cliHelp, "cloud relay on --yes") {
		t.Fatal("relay command missing from help")
	}
}

func TestAccountShowsRelayAndRequiresTypedConsent(t *testing.T) {
	screen := func(u *terminalUI) string { return strings.Join(u.renderAccount(100), "\n") }
	signed := cloud.Status{State: "online", DeviceID: "device"}
	u := &terminalUI{tab: 4, client: &controlClient{}, cloud: signed}
	if s := screen(u); !strings.Contains(s, "Relay     off (default") || strings.Contains(s, "passes through") {
		t.Fatal(s)
	}
	results := make(chan error, 1)
	u.toggleRelay(results)
	if u.prompt != relayPrompt || !strings.Contains(u.prompt, "through the cloud server") || u.busy {
		t.Fatal("turning relay on must ask first:", u.prompt, u.busy)
	}
	// Anything other than a typed yes cancels without sending a request.
	u.input = "no"
	u.editPrompt("\r", results)
	if u.prompt != "" || u.busy {
		t.Fatal("declined relay prompt sent a request", u.prompt, u.busy)
	}
	signed.Relay = cloud.RelayStatus{Enabled: true, State: "connected"}
	u.cloud = signed
	if s := screen(u); !strings.Contains(s, "Relay     connected") || !strings.Contains(s, "passes through the cloud server") {
		t.Fatal(s)
	}
	// Not signed in: there is nothing to relay through, and no prompt is offered.
	out := &terminalUI{tab: 4, client: &controlClient{}}
	out.toggleRelay(results)
	if out.prompt != "" || !strings.Contains(out.message, "Sign in") || strings.Contains(screen(out), "Relay     ") {
		t.Fatal(out.prompt, out.message)
	}
}
