package pkgmgr

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	kitpkg "github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// TestAPTUpgradeAsksNothing pins the two halves of an apt upgrade that can no
// longer ask a question since the runner child lost its terminal: the kit's
// environment, which silences debconf and needrestart, and the dpkg options,
// which answer the config-file question ahead of time by keeping the local
// file. Both upgrade verbs carry both, and the options come before the verb.
func TestAPTUpgradeAsksNothing(t *testing.T) {
	for _, mode := range []string{updates.UpgradeDefault, updates.UpgradeDist} {
		cmd := must(BuildUpgrade(updates.ManagerAPT, mode, false))
		if !slices.Equal(cmd.Env, kitpkg.APTEnv()) {
			t.Errorf("%s: env = %v, want the kit's APTEnv %v", mode, cmd.Env,
				kitpkg.APTEnv())
		}
		want := []string{
			"apt-get", "-y",
			"-o", "Dpkg::Options::=--force-confdef",
			"-o", "Dpkg::Options::=--force-confold",
			mode,
		}
		if !slices.Equal(cmd.Argv, want) {
			t.Errorf("%s: argv = %q, want %q", mode, cmd.Argv, want)
		}
		if !strings.Contains(cmd.Description, "keeps the local version") {
			t.Errorf("%s: the description does not explain the config-file "+
				"policy: %q", mode, cmd.Description)
		}
		if IsHandOff(cmd) {
			t.Errorf("%s: an apt upgrade is a runner step, not a hand-off", mode)
		}
	}
	// The simulation and the refresh are reads and a cache write that ask
	// nothing; they stay as they were.
	if sim, _ := BuildSimulate(updates.ManagerAPT, updates.UpgradeDefault); len(sim.Env) != 0 {
		t.Errorf("the simulation needs no environment: %v", sim.Env)
	}
}

// TestAPTPreviewShowsTheEnvironment: escalated, the variables go through
// env(1) after the prefix, because sudo resets the environment, and the
// confirm dialog says so in the command line itself.
func TestAPTPreviewShowsTheEnvironment(t *testing.T) {
	fake := sample(t, DemoUbuntu)
	plan, err := fake.Plan(context.Background(),
		updates.PlanOptions{Mode: updates.UpgradeDefault})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	preview := DemoPreviewAll(fake, plan)
	want := "sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a " +
		"apt-get -y -o Dpkg::Options::=--force-confdef " +
		"-o Dpkg::Options::=--force-confold upgrade"
	if !strings.Contains(preview, want) {
		t.Errorf("preview:\n%s\nwant a line %q", preview, want)
	}
	if len(plan.HandOff) != 0 {
		t.Errorf("an apt plan has no hand-off: %v", plan.HandOff)
	}
	if !slices.Contains(plan.Explain, aptNoQuestionsSentence) {
		t.Errorf("the confirm dialog does not explain the apt policy: %q",
			plan.Explain)
	}
}

// TestOmarchyUpgradeIsAHandOff: on Omarchy Server the upgrade is the
// updater, marked as the plan's hand-off at its own index, previewed without
// `-n` because it has the terminal to ask a password on, and explained.
func TestOmarchyUpgradeIsAHandOff(t *testing.T) {
	fake := sample(t, DemoOmarchy)
	for _, snapshot := range []bool{false, true} {
		plan, err := fake.Plan(context.Background(),
			updates.PlanOptions{Mode: updates.UpgradeDefault, Snapshot: snapshot})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		handOffs := 0
		for i, cmd := range plan.Commands {
			if plan.IsHandOff(i) != IsHandOff(cmd) {
				t.Errorf("step %d %q: plan says hand-off=%v", i, cmd.String(),
					plan.IsHandOff(i))
			}
			if plan.IsHandOff(i) {
				handOffs++
				if got := fake.Preview(cmd); got !=
					"sudo omarchy-server-update run --no-reboot" {
					t.Errorf("hand-off preview = %q", got)
				}
			} else if !strings.HasPrefix(fake.Preview(cmd), "sudo -n ") {
				t.Errorf("runner step %q lost its -n", fake.Preview(cmd))
			}
		}
		if handOffs != 1 {
			t.Errorf("snapshot=%v: %d hand-offs, want the upgrade alone",
				snapshot, handOffs)
		}
		if !slices.Contains(plan.Explain, handOffSentence) {
			t.Errorf("the confirm dialog does not explain the hand-off: %q",
				plan.Explain)
		}
	}
}

// TestHandOffArgv: the real hand-off runs what its preview showed — the
// prefix without `-n`, env(1) for a command's variables, the resolved binary.
func TestHandOffArgv(t *testing.T) {
	run := &runner.Runner{
		Bin: "/usr/bin/omarchy-server-update", Name: OmarchyUpdate,
		Privilege: []string{"/usr/bin/sudo", "-n"},
	}
	cmd := must(BuildUpgrade(updates.ManagerPacman, updates.UpgradeDefault, true))
	want := []string{"/usr/bin/sudo", "/usr/bin/omarchy-server-update", "run", "--no-reboot"}
	if got := handOffArgv(run, cmd); !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
	if got := handOffPreview(run, cmd); got !=
		"/usr/bin/sudo omarchy-server-update run --no-reboot" {
		t.Errorf("preview = %q", got)
	}
	cmd.Env = []string{"A=1"}
	want = []string{"/usr/bin/sudo", "env", "A=1", "/usr/bin/omarchy-server-update",
		"run", "--no-reboot"}
	if got := handOffArgv(run, cmd); !slices.Equal(got, want) {
		t.Errorf("argv with env = %q, want %q", got, want)
	}
	direct := &runner.Runner{Bin: "/usr/bin/omarchy-server-update", Name: OmarchyUpdate}
	want = []string{"/usr/bin/omarchy-server-update", "run", "--no-reboot"}
	if got := handOffArgv(direct, cmd); !slices.Equal(got, want) {
		t.Errorf("argv as root = %q, want %q", got, want)
	}
}

// TestHandOffRefusesARunnerStep: only the commands IsHandOff names get the
// terminal; anything else asked for as a hand-off is a programming error.
func TestHandOffRefusesARunnerStep(t *testing.T) {
	fake := sample(t, DemoUbuntu)
	if _, err := fake.HandOff(must(BuildUpgrade(updates.ManagerAPT,
		updates.UpgradeDefault, false))); err == nil {
		t.Error("the fake handed the terminal to an apt upgrade")
	}
	real := &Real{runners: map[string]*runner.Runner{}}
	if _, err := real.HandOff(must(BuildReboot())); err == nil {
		t.Error("the real backend handed the terminal to a reboot")
	}
}

// TestFakeHandOffRuns: the sample hand-off says on the terminal what would
// have run there, waits for Enter, then applies the upgrade to the sample
// machine and records itself.
func TestFakeHandOffRuns(t *testing.T) {
	fake := sample(t, DemoOmarchy)
	cmd := must(BuildUpgrade(updates.ManagerPacman, updates.UpgradeDefault, true))
	process, err := fake.HandOff(cmd)
	if err != nil {
		t.Fatalf("hand-off: %v", err)
	}
	var out bytes.Buffer
	process.SetStdin(strings.NewReader("\n"))
	process.SetStdout(&out)
	process.SetStderr(&out)
	if err := process.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "sudo omarchy-server-update run --no-reboot") {
		t.Errorf("the terminal was not told what runs there:\n%s", out.String())
	}
	if got := fake.HandedOff(); len(got) != 1 || got[0].String() != cmd.String() {
		t.Errorf("handed off = %v", got)
	}
	model, _ := fake.Load(context.Background())
	if len(model.Pending) != 0 {
		t.Errorf("%d packages still pending after the hand-off", len(model.Pending))
	}
}

// TestDemoMachines: every sample machine builds, and an unknown name is
// refused with the list of the known ones.
func TestDemoMachines(t *testing.T) {
	want := map[string]string{
		DemoFedora: updates.ManagerDNF, DemoUbuntu: updates.ManagerAPT,
		DemoOmarchy: updates.ManagerPacman,
	}
	for _, machine := range DemoMachines() {
		fake := sample(t, machine)
		if fake.Name() != want[machine] {
			t.Errorf("%s: manager %q, want %q", machine, fake.Name(), want[machine])
		}
		if _, err := fake.Plan(context.Background(),
			updates.PlanOptions{Mode: updates.UpgradeDefault, Snapshot: true}); err != nil {
			t.Errorf("%s: plan: %v", machine, err)
		}
	}
	if _, err := NewFakeMachine("gentoo"); err == nil ||
		!strings.Contains(err.Error(), DemoOmarchy) {
		t.Errorf("unknown machine: err = %v", err)
	}
}

// sample builds one of the sample machines, failing the test when it cannot.
func sample(t *testing.T, machine string) *Fake {
	t.Helper()
	fake, err := NewFakeMachine(machine)
	if err != nil {
		t.Fatalf("sample machine %s: %v", machine, err)
	}
	return fake
}
