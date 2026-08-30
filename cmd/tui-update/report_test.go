package main

import (
	"os"
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/pkgmgr"
)

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that the manager the fake imitates is named rather
// than left to look like a live read, and that no update was read to produce
// any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	opts := options{demo: true, report: true}
	if err := runReport(baseConfig(), opts, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: " + pkgmgr.DemoManager + "\n",
		"helpers: ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportLive renders the live block. On a machine with a supported
// manager it names it; on one without, the point of the flag is that a report
// is produced anyway, with the detection error in it.
func TestRunReportLive(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "mode: live\n") {
		t.Errorf("a live report should say so:\n%s", got)
	}
	if strings.Contains(got, "backend: unknown") &&
		!strings.Contains(got, "backend error: ") {
		t.Errorf("report names neither a manager nor why there is none:\n%s", got)
	}
}

// TestReportLeaksNothingAboutTheUser is the privacy promise the bug form makes
// on this block's behalf: it is pasted into a public issue as it is, so the
// host name, the user name and any path under a home directory have to be
// absent from every mode the flag has.
func TestReportLeaksNothingAboutTheUser(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	// A machine named after its distribution ("fedora") would make this
	// assertion fire on the distro line, which is a fact the block is supposed
	// to carry. In that case the host name proves nothing either way, so it is
	// dropped rather than asserted on wrongly.
	if osRelease, err := os.ReadFile("/etc/os-release"); err == nil && host != "" &&
		strings.Contains(strings.ToLower(string(osRelease)), strings.ToLower(host)) {
		host = ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}

	for _, demo := range []bool{false, true} {
		var out strings.Builder
		opts := options{demo: demo, report: true}
		if err := runReport(baseConfig(), opts, &out); err != nil {
			t.Fatalf("runReport(demo=%v): %v", demo, err)
		}
		got := out.String()
		for _, secret := range []string{host, home, "/home/", os.Getenv("USER")} {
			if secret == "" {
				continue
			}
			if strings.Contains(got, secret) {
				t.Errorf("report(demo=%v) leaks %q:\n%s", demo, secret, got)
			}
		}
	}
}

// TestDescribeHelpers renders the detector's verdict for every binary the
// manager is driven through, which is what tells "the parser is wrong" from
// "checkupdates is not installed here".
func TestDescribeHelpers(t *testing.T) {
	tests := []struct {
		name   string
		states []pkgmgr.State
		want   string
	}{
		{
			name: "installed and missing read differently",
			states: []pkgmgr.State{
				{Name: "pacman", Installed: true},
				{Name: "checkupdates", Installed: false},
			},
			want: "pacman present, checkupdates absent",
		},
		{
			name:   "a machine with none of them",
			states: nil,
			want:   "none",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeHelpers(tc.states); got != tc.want {
				t.Errorf("describeHelpers = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInspectCoversTheManagersHelpers checks that each manager is inspected
// through the binaries it is actually driven with, and that an undetected
// manager falls back to naming the three the tool supports.
func TestInspectCoversTheManagersHelpers(t *testing.T) {
	tests := map[string][]string{
		"pacman": {"pacman", "checkupdates", "fakeroot", "snapper"},
		"apt":    {"apt", "needrestart", "snapper"},
		"dnf":    {"dnf", "rpm", "needs-restarting", "snapper"},
		"":       {"pacman", "apt", "dnf"},
	}
	for manager, want := range tests {
		names := map[string]bool{}
		for _, s := range pkgmgr.Inspect(manager) {
			names[s.Name] = true
		}
		for _, bin := range want {
			if !names[bin] {
				t.Errorf("Inspect(%q) does not cover %q", manager, bin)
			}
		}
	}
}
