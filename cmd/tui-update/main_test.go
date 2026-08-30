package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
)

// baseConfig is the configuration as it stands before the flags are folded in.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

func TestParseFlags(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	opts, err := parseFlags([]string{"--demo", "--theme", "/t/colors.toml"}, devNull)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || opts.themePath != "/t/colors.toml" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.sudoSet {
		t.Error("sudoSet should be false when -sudo is absent")
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := baseConfig()
	applyOverrides(&cfg, options{themePath: "/t/colors.toml"})
	if got := cfg.Theme(); got != "/t/colors.toml" {
		t.Errorf("Theme() = %q", got)
	}
	// An untouched -sudo must not clear the configured prefix.
	if got := cfg.String(config.KeySudo, ""); got != "sudo -n" {
		t.Errorf("sudo = %q, want the config value", got)
	}

	// An explicit empty -sudo disables escalation.
	cfg = baseConfig()
	applyOverrides(&cfg, options{sudoSet: true, sudo: ""})
	if got := cfg.String(config.KeySudo, "unset"); got != "" {
		t.Errorf("sudo = %q, want empty", got)
	}
	if got := cfg.SudoPrefix(); got != nil {
		t.Errorf("SudoPrefix = %q, want nil", got)
	}
}

func TestDefaultsCoverEveryFlag(t *testing.T) {
	// Every key a flag can override must be declared, otherwise the
	// environment layer silently skips it.
	for _, key := range []string{config.KeySudo, config.KeyTheme} {
		if _, ok := defaults()[key]; !ok {
			t.Errorf("defaults() is missing %q", key)
		}
	}
}

func TestPickBackendDemo(t *testing.T) {
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Result{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if !strings.Contains(backend.Describe(), "demo") {
		t.Errorf("Describe = %q, want it to say it is a demo", backend.Describe())
	}
}

// TestCheckReportsTheSummary covers the contract the smoke test depends on:
// the counts, the restart verdict and the timer state a shell script greps
// for.
func TestCheckReportsTheSummary(t *testing.T) {
	report := demoCheck(t)

	// `pending` has to be an integer, because the smoke test compares it with
	// what the manager itself printed.
	if report.Pending != 14 {
		t.Errorf("pending = %d, want 14", report.Pending)
	}
	if report.Security != 3 {
		t.Errorf("security = %d, want 3", report.Security)
	}
	if report.Manager != "dnf" {
		t.Errorf("manager = %q", report.Manager)
	}
	if report.Restart != "reboot" || !report.RebootRequired {
		t.Errorf("restart = %q, rebootRequired = %v",
			report.Restart, report.RebootRequired)
	}
	if len(report.Services) != 2 {
		t.Errorf("services = %v, want sshd and nginx", report.Services)
	}
	if !report.Snapshot || report.SnapshotConfig != "root" {
		t.Errorf("snapshot = %v (%q)", report.Snapshot, report.SnapshotConfig)
	}
	if len(report.Timers) != 1 || report.Timers[0].Unit != "dnf-automatic.timer" {
		t.Errorf("timers = %+v", report.Timers)
	}
}

// TestCheckNeverMarshalsNull: a shell script that greps for `"services": []`
// must not have to handle a null instead.
func TestCheckNeverMarshalsNull(t *testing.T) {
	var out bytes.Buffer
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Result{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if err := runCheck(backend, compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if strings.Contains(out.String(), "null") {
		t.Errorf("--check emitted a null:\n%s", out.String())
	}
	for _, want := range []string{
		`"tool": "tui-update"`,
		`"manager": "dnf"`,
		`"pending": 14`,
		`"snapshot": true`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--check output is missing %s", want)
		}
	}
}

// demoCheck runs --check against the sample machine and decodes it.
func demoCheck(t *testing.T) checkReport {
	t.Helper()
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Result{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	var out bytes.Buffer
	if err := runCheck(backend, compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("--check did not emit valid JSON: %v\n%s", err, out.String())
	}
	return report
}
