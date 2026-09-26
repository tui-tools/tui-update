package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
	"github.com/tui-tools/tui-update/internal/updates"
)

// newMachineApp builds the app around one of the sample machines, loaded,
// with the plan asked for in order to run it and the confirm dialog open.
func newMachineApp(t *testing.T, machine string) (*app, *pkgmgr.Fake, updates.Plan) {
	t.Helper()
	fake, err := pkgmgr.NewFakeMachine(machine)
	if err != nil {
		t.Fatalf("sample machine: %v", err)
	}
	a := newApp(fake, theme.FromPalette(theme.TokyoNight()), compat.Result{})
	send(a, tea.WindowSizeMsg{Width: 120, Height: 40})
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	send(a, loadedMsg{model: model})
	key(a, "U")
	plan, err := fake.Plan(t.Context(), defaultPlanOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	send(a, plannedMsg{plan: plan, apply: true})
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm dialog", a.mode)
	}
	return a, fake, plan
}

// TestAPTConfirmShowsTheNonInteractiveUpgrade: the dialog's command line is
// the one that runs, variables and dpkg options included, and its body says
// in words what they do.
func TestAPTConfirmShowsTheNonInteractiveUpgrade(t *testing.T) {
	a, _, _ := newMachineApp(t, pkgmgr.DemoUbuntu)
	want := "sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a " +
		"apt-get -y -o Dpkg::Options::=--force-confdef " +
		"-o Dpkg::Options::=--force-confold upgrade"
	if !strings.Contains(a.confirm.Command, want) {
		t.Errorf("the dialog does not show %q:\n%s", want, a.confirm.Command)
	}
	for _, words := range []string{"keeps the local version", ".dpkg-dist",
		"needrestart restarts"} {
		if !strings.Contains(a.confirm.Body, words) {
			t.Errorf("the dialog body does not say %q:\n%s", words, a.confirm.Body)
		}
	}
}

// TestOmarchyUpgradeIsHandedTheTerminal drives the Omarchy sequence end to
// end: the runner steps run in the background as before, the updater's step
// is a tea.Exec hand-off rather than a runner step, and after it returns the
// sequence goes on, finishes and re-reads the machine.
func TestOmarchyUpgradeIsHandedTheTerminal(t *testing.T) {
	a, fake, plan := newMachineApp(t, pkgmgr.DemoOmarchy)
	if !strings.Contains(a.confirm.Command, "sudo omarchy-server-update run --no-reboot") {
		t.Errorf("the dialog does not show the hand-off without -n:\n%s",
			a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, "The terminal is handed over") {
		t.Errorf("the dialog does not say the terminal is handed over:\n%s",
			a.confirm.Body)
	}

	cmd := key(a, "y")
	handOffs := 0
	for step := range plan.Commands {
		if cmd == nil {
			t.Fatalf("step %d: nothing was started", step)
		}
		msg := cmd()
		if !plan.IsHandOff(step) {
			// A runner step: the command runs the fake and answers.
			if _, ok := msg.(stepMsg); !ok {
				t.Fatalf("step %d: got %T, want a stepMsg", step, msg)
			}
			cmd = send(a, msg)
			continue
		}
		handOffs++
		// A hand-off: the command asks Bubble Tea to hand the terminal over,
		// which the program runtime does and a test cannot; nothing ran yet.
		if _, ok := msg.(stepMsg); ok {
			t.Fatalf("step %d ran as a runner step, want a tea.Exec hand-off", step)
		}
		if len(fake.HandedOff()) != 0 {
			t.Fatalf("the updater ran before the terminal was handed over")
		}
		log := strings.Join(a.applyLog, "\n")
		if !strings.Contains(log, "handed over to this step") {
			t.Errorf("the apply pane does not say the terminal is handed "+
				"over:\n%s", log)
		}
		// What the runtime does: give the process the terminal, run it, and
		// deliver the callback's message.
		process, err := fake.HandOff(plan.Commands[step])
		if err != nil {
			t.Fatalf("HandOff: %v", err)
		}
		process.SetStdin(strings.NewReader("\n"))
		process.SetStdout(&strings.Builder{})
		runErr := process.Run()
		cmd = send(a, stepMsg{index: step, cmd: plan.Commands[step],
			err: runErr, handOff: true})
	}
	if handOffs != 1 {
		t.Errorf("%d hand-offs, want the updater alone", handOffs)
	}
	if !a.applyDone || a.busy {
		t.Errorf("done=%v busy=%v after the last step", a.applyDone, a.busy)
	}
	if cmd == nil {
		t.Errorf("the machine is not re-read after the sequence")
	}
	log := strings.Join(a.applyLog, "\n")
	if !strings.Contains(log, "its output was on the terminal") {
		t.Errorf("the apply pane does not account for the hand-off:\n%s", log)
	}
	if got := fake.HandedOff(); len(got) != 1 ||
		got[0].Argv[0] != pkgmgr.OmarchyUpdate {
		t.Errorf("handed off %v, want the updater once", got)
	}
}

// TestFailedHandOffStopsTheSequence: an updater that exits non-zero, or one
// the backend cannot prepare, stops the sequence like any failed step, and
// the snapshot after it is never taken.
func TestFailedHandOffStopsTheSequence(t *testing.T) {
	a, fake, plan := newMachineApp(t, pkgmgr.DemoOmarchy)
	key(a, "y")
	index := -1
	for i := range plan.Commands {
		if plan.IsHandOff(i) {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("the Omarchy plan has no hand-off")
	}
	a.applyStep = index
	send(a, stepMsg{index: index, cmd: plan.Commands[index],
		err: errors.New("exit status 1"), handOff: true})
	if !a.applyDone || a.busy {
		t.Errorf("done=%v busy=%v after a failed hand-off", a.applyDone, a.busy)
	}
	if !strings.Contains(a.status, "the sequence stopped at") {
		t.Errorf("status = %q", a.status)
	}
	for _, ran := range fake.Ran() {
		if strings.Contains(ran.String(), "-t post") {
			t.Errorf("the post snapshot ran after a failed upgrade")
		}
	}
}

// TestDemoMachineFlag: --demo-machine picks the sample machine, the default
// is Fedora's, and a name nobody knows is refused before the UI starts.
func TestDemoMachineFlag(t *testing.T) {
	for machine, manager := range map[string]string{
		"": updates.ManagerDNF, pkgmgr.DemoUbuntu: updates.ManagerAPT,
		pkgmgr.DemoOmarchy: updates.ManagerPacman,
	} {
		args := []string{"--demo"}
		if machine != "" {
			args = append(args, "--demo-machine", machine)
		}
		opts, err := parseFlags(args, nil)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		fake, err := demoBackend(opts)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if fake.Name() != manager {
			t.Errorf("%v drives %s, want %s", args, fake.Name(), manager)
		}
	}
	opts, err := parseFlags([]string{"--demo", "--demo-machine", "gentoo"}, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := demoBackend(opts); err == nil {
		t.Error("an unknown sample machine was accepted")
	}
}
