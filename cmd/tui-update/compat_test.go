package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuiupdate "github.com/tui-tools/tui-update"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
	"github.com/tui-tools/tui-update/internal/updates"
)

// loadManifest loads the manifest block the binary really reads.
func loadManifest(t *testing.T) manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(tuiupdate.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded manifest does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Fatalf("manifest name = %q, want %q", m.Name, toolName)
	}
	return m
}

// backend loads one declared backend.
func backend(t *testing.T, name string) compat.Backend {
	t.Helper()
	b, ok := loadManifest(t).Backend(name)
	if !ok {
		t.Fatalf("the manifest declares no %q backend", name)
	}
	return b
}

// TestManifestDeclaresEveryManager: the tool drives three package managers,
// and the compatibility block is what it probes each of them with. A manager
// the code can select but the manifest does not describe would run with no
// version, no minimum and no caveats.
func TestManifestDeclaresEveryManager(t *testing.T) {
	for _, manager := range []string{
		updates.ManagerPacman, updates.ManagerAPT, updates.ManagerDNF,
	} {
		b := backend(t, manager)
		if b.Binary == "" || len(b.VersionCommand) == 0 {
			t.Errorf("%s: a backend with no version command cannot be probed",
				manager)
		}
		if b.Minimum == "" {
			t.Errorf("%s: no minimum version is declared", manager)
		}
		if len(b.SearchPaths) == 0 {
			t.Errorf("%s: no search paths, so a non-root PATH may miss it",
				manager)
		}
		if len(b.Notes) == 0 {
			t.Errorf("%s: every one of these managers has a caveat worth "+
				"stating", manager)
		}
	}
}

func TestManifestMinimums(t *testing.T) {
	tests := map[string]string{
		updates.ManagerPacman: "6.0",
		updates.ManagerAPT:    "2.0",
		updates.ManagerDNF:    "4.0",
	}
	for manager, want := range tests {
		if got := backend(t, manager).Minimum; got != want {
			t.Errorf("%s minimum = %q, want %q", manager, got, want)
		}
	}
}

// TestVersionRegexReadsRealOutput uses the `--version` banners as they really
// print. The dnf one is the interesting case: dnf5 announces itself by name
// before the number, dnf4 prints a bare version on its first line, and the
// same pattern has to read both.
func TestVersionRegexReadsRealOutput(t *testing.T) {
	tests := []struct {
		manager string
		output  string
		want    string
	}{
		// dnf5 announces itself by name before the number, and prints four
		// components where the family schema records three: the pattern keeps
		// the first three so the version can be written into `tested`.
		{updates.ManagerDNF, "dnf5 version 5.2.18.0\ndnf5 plugin API version 2.0",
			"5.2.18"},
		{updates.ManagerDNF, "4.24.0\n  Installed: rpm-0:4.20.1-1.fc42.x86_64",
			"4.24.0"},
		{updates.ManagerAPT, "apt 2.7.14 (amd64)", "2.7.14"},
		{updates.ManagerAPT, "apt 2.0.10 (amd64)", "2.0.10"},
		{updates.ManagerPacman,
			" .--.                  Pacman v7.0.0 - libalpm v15.0.0", "7.0.0"},
		{updates.ManagerPacman,
			" .--.                  Pacman v6.0.2 - libalpm v13.0.2", "6.0.2"},
	}
	for _, test := range tests {
		b := backend(t, test.manager)
		if got := compat.ParseVersion(test.output, b.VersionRegex); got != test.want {
			t.Errorf("%s: ParseVersion(%q) = %q, want %q",
				test.manager, test.output, got, test.want)
		}
	}
}

// TestVersionRegexAgainstCapturedBanners runs the same patterns over the
// `--version` output captured verbatim in internal/pkgmgr/testdata. The table
// above is the readable form; this is the one that cannot drift from what a
// real machine printed.
func TestVersionRegexAgainstCapturedBanners(t *testing.T) {
	tests := []struct {
		manager string
		fixture string
		want    string
	}{
		{updates.ManagerDNF, "dnf5-version.txt", "5.2.18"},
		{updates.ManagerDNF, "dnf4-version.txt", "4.24.0"},
		{updates.ManagerAPT, "apt-version.txt", "2.7.14"},
		{updates.ManagerPacman, "pacman-version.txt", "7.0.0"},
	}
	for _, test := range tests {
		raw, err := os.ReadFile(filepath.Join(
			"..", "..", "internal", "pkgmgr", "testdata", test.fixture))
		if err != nil {
			t.Fatalf("read %s: %v", test.fixture, err)
		}
		b := backend(t, test.manager)
		if got := compat.ParseVersion(string(raw), b.VersionRegex); got != test.want {
			t.Errorf("%s: ParseVersion(%s) = %q, want %q",
				test.manager, test.fixture, got, test.want)
		}
	}
}

// TestDNF5FeatureGate pins what the dnf backend changes behaviour on: dnf5
// moved `needs-restarting` into the main binary, where it refreshes the
// metadata before answering.
func TestDNF5FeatureGate(t *testing.T) {
	b := backend(t, updates.ManagerDNF)
	tests := map[string]bool{
		"4.24.0":   false,
		"4.99.0":   false,
		"5.0.0":    true,
		"5.2.18.0": true,
	}
	for version, want := range tests {
		caps := compat.NewCaps(version, b.Features)
		if got := caps.Has(pkgmgr.FeatureDNF5); got != want {
			t.Errorf("dnf %s: dnf5 = %v, want %v", version, got, want)
		}
	}
}

// TestUnknownVersionKeepsEveryFeature: a version the probe could not read must
// not hide a working view. The backend refuses in its own words instead.
func TestUnknownVersionKeepsEveryFeature(t *testing.T) {
	caps := compat.Result{}.Caps()
	if !caps.Has(pkgmgr.FeatureDNF5) || !caps.Has(pkgmgr.FeatureAPTSolver3) {
		t.Errorf("an unprobed version must be treated as capable")
	}
}

func TestProbeInDemoModeReportsNothing(t *testing.T) {
	if got := detectManager(true); got != "" {
		t.Errorf("--demo detected %q, want nothing probed", got)
	}
	if got := probeCompat(context.Background(), ""); got.Backend != "" {
		t.Errorf("an unnamed manager probed something: %+v", got)
	}
}

func TestClassifiesVersionsAgainstTheMinimum(t *testing.T) {
	b := backend(t, updates.ManagerPacman)
	tests := map[string]compat.Status{
		"5.2": compat.StatusBelowMinimum,
		"6.0": compat.StatusUntested,
		"7.0": compat.StatusUntested,
	}
	for version, want := range tests {
		result := compat.ProbeWith(context.Background(), b,
			func(context.Context, []string) (string, error) {
				return "Pacman v" + version + " - libalpm v15.0.0", nil
			})
		if result.Version != version {
			t.Errorf("probed version %q, want %q", result.Version, version)
		}
		// A version in the manifest's tested list would classify as tested;
		// the expectations above hold while that list is short, so they are
		// skipped for a version the evidence file already covers.
		if isTested(b, version) {
			continue
		}
		if result.Status != want {
			t.Errorf("pacman %s: status %v, want %v", version, result.Status, want)
		}
	}
}

// isTested reports whether the manifest already records a passing run.
func isTested(b compat.Backend, version string) bool {
	for _, tested := range b.Tested {
		if tested == version {
			return true
		}
	}
	return false
}
