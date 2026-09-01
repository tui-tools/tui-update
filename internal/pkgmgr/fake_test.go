package pkgmgr

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/updates"
)

// TestDemoParity is the rule --demo lives by: every key that works against a
// real machine works against the sample one, and builds the same argv.
//
// It is a table over the three things wave 3 added, because a demo that is
// missing one of them is a demo that teaches the wrong keys.
func TestDemoParity(t *testing.T) {
	tests := []struct {
		name string
		opts updates.PlanOptions
		want []string
		deny []string
	}{
		{
			name: "the plain upgrade, with the snapshot",
			opts: updates.PlanOptions{Mode: updates.UpgradeDefault, Snapshot: true},
			want: []string{
				"snapper create -c root -t pre", "dnf makecache --refresh -q",
				"dnf -y upgrade", "snapper create -c root -t post",
			},
			deny: []string{"--security"},
		},
		{
			name: "the plain upgrade with the snapshot turned off",
			opts: updates.PlanOptions{Mode: updates.UpgradeDefault},
			want: []string{"dnf makecache --refresh -q", "dnf -y upgrade"},
			deny: []string{"snapper create"},
		},
		{
			name: "the security-only upgrade",
			opts: updates.PlanOptions{Mode: updates.UpgradeSecurity, Snapshot: true},
			want: []string{
				"snapper create -c root -t pre", "dnf -y upgrade --security",
				"snapper create -c root -t post",
			},
		},
	}
	for _, test := range tests {
		fake := NewFake()
		plan, err := fake.Plan(t.Context(), test.opts)
		if err != nil {
			t.Fatalf("%s: Plan: %v", test.name, err)
		}
		preview := plan.Preview()
		for _, want := range test.want {
			if !strings.Contains(preview, want) {
				t.Errorf("%s: the sequence is missing %q:\n%s",
					test.name, want, preview)
			}
		}
		for _, deny := range test.deny {
			if strings.Contains(preview, deny) {
				t.Errorf("%s: the sequence still carries %q:\n%s",
					test.name, deny, preview)
			}
		}
		if plan.TakeSnapshot != test.opts.Snapshot {
			t.Errorf("%s: TakeSnapshot = %v, want %v",
				test.name, plan.TakeSnapshot, test.opts.Snapshot)
		}
	}
}

// TestDemoSecurityUpgradeLeavesTheRestAlone: the sample machine's security
// upgrade really only applies the advisories, so what --demo shows afterwards
// is what a real one would show.
func TestDemoSecurityUpgradeLeavesTheRestAlone(t *testing.T) {
	fake := NewFake()
	plan, err := fake.Plan(t.Context(), updates.PlanOptions{
		Mode: updates.UpgradeSecurity, Snapshot: true,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, cmd := range plan.Commands {
		if _, runErr := fake.Run(t.Context(), cmd); runErr != nil {
			t.Fatalf("Run(%s): %v", cmd, runErr)
		}
	}
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if model.SecurityCount != 0 {
		t.Errorf("%d security fixes survived a security-only upgrade",
			model.SecurityCount)
	}
	if len(model.Pending) == 0 {
		t.Errorf("a security-only upgrade emptied the whole pending list")
	}
	for _, p := range model.Pending {
		if p.Security {
			t.Errorf("%s still carries an advisory", p.Name)
		}
	}
}

// TestDemoHoldsRoundTrip: the hold key changes the sample machine, both ways.
func TestDemoHoldsRoundTrip(t *testing.T) {
	fake := NewFake()
	if !fake.model.Hold.Available {
		t.Fatalf("the sample machine cannot hold a package")
	}

	cmd, err := fake.BuildHold(updates.HoldAdd, "vim-minimal")
	if err != nil {
		t.Fatalf("BuildHold: %v", err)
	}
	if _, err := fake.Run(t.Context(), cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !heldIn(t, fake, "vim-minimal") {
		t.Errorf("the hold did not reach the sample machine")
	}

	cmd, err = fake.BuildHold(updates.HoldRemove, "vim-minimal")
	if err != nil {
		t.Fatalf("BuildHold: %v", err)
	}
	if _, err := fake.Run(t.Context(), cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if heldIn(t, fake, "vim-minimal") {
		t.Errorf("the hold was not lifted")
	}
}

// TestDemoWithoutVersionlockRefuses is the other half of the demo: the machine
// where holding a package is impossible, and the refusal names the package
// that would fix it.
func TestDemoWithoutVersionlockRefuses(t *testing.T) {
	fake := NewFakeWithoutVersionlock()
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if model.Hold.Available {
		t.Fatalf("a machine with no versionlock plugin claimed it can hold")
	}

	_, err = fake.BuildHold(updates.HoldAdd, "nginx")
	if err == nil {
		t.Fatalf("a hold was built without the plugin")
	}
	if !strings.Contains(err.Error(), VersionlockFedora) {
		t.Errorf("the refusal does not name the package: %v", err)
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("the refusal still ran something: %v", fake.Ran())
	}

	// And the command itself, were it ever reached, fails the way dnf does.
	out, err := fake.Run(t.Context(), must(BuildHold(updates.ManagerDNF,
		updates.HoldAdd, "nginx")))
	if err == nil {
		t.Fatalf("the sample dnf accepted versionlock without the plugin: %q", out)
	}
	if !strings.Contains(err.Error(), "No such command") {
		t.Errorf("the failure is not dnf's own: %v", err)
	}
}

// heldIn reports whether the sample machine holds a package, read back through
// Load like the UI would.
func heldIn(t *testing.T, fake *Fake, name string) bool {
	t.Helper()
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, p := range model.Pending {
		if p.Name == name {
			return p.Held
		}
	}
	t.Fatalf("%q is not on the sample machine's pending list", name)
	return false
}
