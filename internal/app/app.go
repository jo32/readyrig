package app

import (
	"computer-use-server/internal/chromemcp"
	"computer-use-server/internal/computer"
	"computer-use-server/internal/harness"
	"computer-use-server/internal/server"
	"computer-use-server/internal/store"
	"computer-use-server/internal/tunnel"
	"fmt"
	"os"
	"path/filepath"
)

type App struct {
	unlock    func()
	Server    *server.Server
	Processes *harness.Processes
	Files     *harness.Files
	Chrome    *chromemcp.Bridge
	Safari    *chromemcp.SafariBridge
}

// LockData lets offline CLI configuration changes use the same instance lock as
// the app. The caller must create the private directory before acquiring it.
func LockData(dir string) (func(), error) { return lockData(dir) }

func New(workspace, dataDir string) (*App, error) {
	var err error
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(workspace, 0755); err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	dataDir, err = filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(workspace, dataDir)
	if filepath.IsLocal(rel) {
		return nil, fmt.Errorf("data directory must be outside workspace to keep private application data outside file tools")
	}
	unlock, err := lockData(dataDir)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			unlock()
		}
	}()
	s, err := store.Open(dataDir)
	if err != nil {
		return nil, err
	}
	accessPath, err := server.NewAccessPath()
	if err != nil {
		s.Close()
		return nil, err
	}
	uiKey := harness.ID() + harness.ID()
	registry := harness.New(s, accessPath, uiKey)
	registry.RegisterHelp()
	// Advanced tools are callable through use_tool; this lists them in tools/list too.
	registry.ExposeAll = os.Getenv("READYRIG_EXPOSE_ALL_TOOLS") == "1"
	files, err := harness.NewFiles(workspace)
	if err != nil {
		s.Close()
		return nil, err
	}
	projects, err := harness.NewProjects(workspace, filepath.Join(dataDir, "projects.json"))
	if err != nil {
		files.Close()
		s.Close()
		return nil, err
	}
	files.Projects = projects
	spillDir := filepath.Join(dataDir, "spill")
	files.SpillDir = spillDir
	projects.Register(registry)
	privacy, err := harness.NewPrivacy(filepath.Join(dataDir, "privacy.json"), func() []harness.Project { return projects.Snapshot().Projects })
	if err != nil {
		files.Close()
		s.Close()
		return nil, err
	}
	privacy.OnChange = registry.Signal
	registry.Privacy = privacy
	processes := harness.NewProcesses(workspace)
	processes.Projects = projects
	processes.SpillDir = spillDir
	processes.Privacy = privacy
	c := computer.New(filepath.Join(dataDir, "screenshots"))
	files.Register(registry)
	processes.Register(registry)
	c.Register(registry)
	registry.PermissionCheck = func(spec harness.Spec) string {
		if spec.Category == "computer" {
			return c.Missing(spec.Name)
		}
		return ""
	}
	registry.OnPause = processes.Stop
	chrome := chromemcp.New(registry)
	// The browser tools may save files, such as screenshots, inside approved projects.
	approvedRoots := func() []string {
		state := projects.Snapshot()
		roots := make([]string, 0, len(state.Projects)+1)
		for _, project := range state.Projects {
			roots = append(roots, project.Path)
		}
		if state.FullAccess {
			roots = append(roots, string(filepath.Separator))
		}
		return roots
	}
	chrome.SetRoots(approvedRoots)
	safari := chromemcp.NewSafari(registry)
	safari.SetRoots(approvedRoots)
	chrome.Link(safari.Refresh)
	projects.OnChange = func() {
		chrome.RootsChanged()
		privacy.Rebuild()
	}
	sharing := tunnel.New(tunnel.Options{Dir: filepath.Join(dataDir, "cloudflared"), Changed: registry.Signal, RedactSecrets: registry.AddSecrets})
	ready = true
	return &App{unlock: unlock, Server: &server.Server{Registry: registry, Projects: projects, Privacy: privacy, Store: s, Computer: c, Chrome: chrome, Safari: safari, Tunnel: sharing, Workspace: workspace, AccessPath: accessPath, UIKey: uiKey}, Processes: processes, Files: files, Chrome: chrome, Safari: safari}, nil
}
func (a *App) Close() {
	if a.Server.Cloud != nil {
		a.Server.Cloud.Close()
	}
	a.Server.Tunnel.Close()
	a.Server.Registry.SetPaused(true)
	a.Chrome.Close()
	a.Safari.Close()
	a.Processes.Stop()
	a.Server.Registry.WaitBackground()
	a.Files.Close()
	a.Server.Store.Close()
	a.unlock()
}
