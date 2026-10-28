package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CommandEnv decides whether commands inherit ReadyRig's own environment, so
// variables the platform sets for the service (credential paths, proxies) reach
// them as in a terminal. Turned off, commands get only the short list in
// commandEnv. ReadyRig's own READYRIG_* and RELAY_* settings are never passed on.
type CommandEnv struct {
	mu       sync.RWMutex
	file     string
	settings CommandEnvSettings
	OnChange func()
}

// CommandEnvSettings is saved in command-env.json. Inheriting is on until the
// user turns it off.
type CommandEnvSettings struct {
	Inherit bool `json:"inherit"`
}

func NewCommandEnv(file string) (*CommandEnv, error) {
	e := &CommandEnv{file: file, settings: CommandEnvSettings{Inherit: true}}
	if file != "" {
		b, err := os.ReadFile(file)
		if err == nil {
			if err = json.Unmarshal(b, &e.settings); err != nil {
				return nil, fmt.Errorf("command env: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return e, nil
}

// Inherit reports whether commands get ReadyRig's environment. Without a
// setting (nil) they do, the default.
func (e *CommandEnv) Inherit() bool {
	if e == nil {
		return true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings.Inherit
}

// SetInherit saves and applies the setting; running commands keep theirs.
func (e *CommandEnv) SetInherit(on bool) error {
	if e == nil {
		return errors.New("command environment setting is unavailable")
	}
	e.mu.Lock()
	old := e.settings
	e.settings.Inherit = on
	if err := e.save(); err != nil {
		e.settings = old
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()
	if e.OnChange != nil {
		e.OnChange()
	}
	return nil
}

func (e *CommandEnv) save() error {
	if e.file == "" {
		return nil
	}
	b, err := json.MarshalIndent(e.settings, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(e.file), ".command-env-*")
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
	return os.Rename(f.Name(), e.file)
}

// inheritedEnv is ReadyRig's environment without its own settings, which can
// hold secrets such as RELAY_UPDATE_TOKEN.
func inheritedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "READYRIG_") || strings.HasPrefix(k, "RELAY_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}
