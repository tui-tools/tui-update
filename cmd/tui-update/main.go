// Command tui-update is a terminal UI for the machine's pending package
// updates: what is waiting, what applying it would restart or reboot, and
// what would be snapshotted first. It previews the exact command sequence
// before running it, and never reboots by itself. pacman, apt and dnf are the
// managers implemented today, behind one interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
	"github.com/tui-tools/tui-update/internal/updates"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-update/config.toml and ~/.config/tui-update/config.toml.
const toolName = "tui-update"

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys tui-update understands. Only these
// are read from the environment (TUI_UPDATE_SUDO, …).
func defaults() map[string]string {
	return map[string]string{
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// options holds the parsed command line.
type options struct {
	demo bool
	// demoNoVersionlock drives the sample machine without the dnf versionlock
	// plugin, which is the only way to see the hold key's refusal — and the
	// package it names — without uninstalling a plugin on a real machine.
	demoNoVersionlock bool
	check             bool
	report            bool
	themePath         string
	sudo              string
	showVersion       bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a sample machine, without touching the real one")
	fs.BoolVar(&opts.demoNoVersionlock, "demo-no-versionlock", false,
		"with --demo, the sample machine has no dnf versionlock plugin, so "+
			"holding a package is refused with the package to install")
	fs.BoolVar(&opts.check, "check", false,
		"read the pending updates and print the result as JSON, then exit "+
			"(no UI, no changes); exit 1 if the manager cannot be read")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-update — the machine's pending package "+
			"updates\n\nUsage:\n  tui-update [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_UPDATE_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sudo" {
			opts.sudoSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// Which manager this machine runs is decided before anything else,
	// because it is what the version probe is keyed on: the manifest carries
	// one backend block per manager, and probing the wrong one would report a
	// version that has nothing to do with what is about to be driven.
	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine where no manager can be driven.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It
	// reads no updates and it survives a machine with no supported manager at
	// all, because "there is nothing here to drive" is one of the things a bug
	// report has to be able to say. So it comes before the backend is
	// required, and it does its own detection and probe.
	if opts.report {
		return runReport(cfg, opts, os.Stdout)
	}

	manager := detectManager(opts.demo)
	backendCompat := probeCompat(context.Background(), manager)

	backend, err := pickBackend(cfg, opts, backendCompat)
	if err != nil {
		return err
	}

	// --check is the non-interactive path: it reads the manager and prints,
	// and never starts a terminal program.
	if opts.check {
		return runCheck(backend, backendCompat, os.Stdout)
	}

	program := tea.NewProgram(newApp(backend, theme.New(), backendCompat),
		tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// detectManager names the manager to probe, and returns nothing under
// --demo: that drives an in-memory machine, and probing the host would report
// a version that has nothing to do with what is on screen.
func detectManager(demo bool) string {
	if demo {
		return ""
	}
	manager, _, err := pkgmgr.Detect()
	if err != nil {
		return ""
	}
	return manager
}

// pickBackend returns the demo backend or the real one.
func pickBackend(cfg config.Config, opts options,
	backendCompat compat.Result) (updates.Backend, error) {
	if opts.demo {
		if opts.demoNoVersionlock {
			return pkgmgr.NewFakeWithoutVersionlock(), nil
		}
		return pkgmgr.NewFake(), nil
	}
	return pkgmgr.NewReal(cfg.SudoPrefix(), backendCompat.Caps())
}
