package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

func runSetup(f *flag.FlagSet, o *startupOptions) error {
	if option := f.Lookup("if-needed"); option != nil && option.Value.String() == "true" {
		if _, err := os.Stat(filepath.Join(o.DataDir, configFile)); err == nil {
			fmt.Println("ReadyRig is already configured. Run 'readyrig' to open the terminal dashboard.")
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("the setup guide needs an interactive terminal; run 'readyrig setup' in a terminal, or use 'readyrig init' with startup options")
	}
	start, err := setupGuide(f, o, os.Stdin, os.Stdout)
	if err != nil || !start {
		return err
	}
	return startDaemon(f, o, os.Stdout)
}

// Keep the guide separate from terminal detection so its answers and cancellation
// can be checked without touching a user's settings or launching a service.
func setupGuide(f *flag.FlagSet, o *startupOptions, in io.Reader, out io.Writer) (bool, error) {
	if o.FullAccess {
		return false, errors.New("Full Access is session-only; use 'readyrig serve --full-access'")
	}
	reader := bufio.NewReader(in)
	ask := func(label, initial string) (string, error) {
		fmt.Fprintf(out, "%s [%s]: ", label, terminalText(initial))
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("setup cancelled before completion: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			line = initial
		}
		return line, nil
	}
	yesNo := func(label string, initial bool) (bool, error) {
		defaultAnswer := "y/N"
		if initial {
			defaultAnswer = "Y/n"
		}
		for {
			answer, err := ask(label, defaultAnswer)
			if err != nil {
				return false, err
			}
			switch strings.ToLower(answer) {
			case "y", "yes":
				return true, nil
			case "n", "no":
				return false, nil
			case strings.ToLower(defaultAnswer):
				return initial, nil
			default:
				fmt.Fprintln(out, "Please enter y or n.")
			}
		}
	}
	fmt.Fprint(out, "\nReadyRig setup\nChoose a workspace and the tools available to your agent.\n\n")
	// Acquire before asking questions so a running instance or another editor cannot
	// change the configuration while the guide is open.
	unlock, err := configurationLock(o.DataDir)
	if err != nil {
		return false, fmt.Errorf("stop ReadyRig with 'readyrig stop' before configuring: %w", err)
	}
	defer unlock()
	workspace, err := ask("1/3  Workspace folder", o.Workspace)
	if err != nil {
		return false, err
	}
	if err := f.Set("workspace", workspace); err != nil {
		return false, err
	}
	fmt.Fprintln(out, "\nTerminal tools execute commands using your account's permissions.")
	shell, err := yesNo("2/3  Enable terminal tools?", o.Shell)
	if err != nil {
		return false, err
	}
	_ = f.Set("allow-shell", fmt.Sprint(shell))
	fmt.Fprintln(out, "\nBrowser tools require remote debugging turned on in Chrome (chrome://inspect/#remote-debugging).")
	browser, err := yesNo("3/3  Enable Chrome browser tools?", !o.NoChrome)
	if err != nil {
		return false, err
	}
	_ = f.Set("no-chrome", fmt.Sprint(!browser))
	if err := validateOptions(o); err != nil {
		return false, err
	}
	_ = f.Set("workspace", o.Workspace)
	start, err := yesNo("\nStart ReadyRig in the background now?", true)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(o.Workspace, 0755); err != nil {
		return false, err
	}
	if err := saveConfig(o.DataDir, f); err != nil {
		return false, err
	}
	fmt.Fprintln(out, "\nSaved settings:", terminalText(filepath.Join(o.DataDir, configFile)))
	fmt.Fprintln(out, "Run 'readyrig' for the terminal dashboard, or 'readyrig help' for commands.")
	return start, nil
}
