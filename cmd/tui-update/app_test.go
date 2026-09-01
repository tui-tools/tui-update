package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
	"github.com/tui-tools/tui-update/internal/updates"
)

// newTestApp builds the app around the sample machine, already loaded and
// sized, which is the state every key test starts from.
func newTestApp(t *testing.T, width, height int) (*app, *pkgmgr.Fake) {
	t.Helper()
	fake := pkgmgr.NewFake()
	a := newApp(fake, theme.FromPalette(theme.TokyoNight()), compat.Result{})
	send(a, tea.WindowSizeMsg{Width: width, Height: height})
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	send(a, loadedMsg{model: model})
	return a, fake
}

// defaultPlanOptions is what the plan screen asks for when nothing has been
// toggled: the plain upgrade, with the snapshot on.
func defaultPlanOptions() updates.PlanOptions {
	return updates.PlanOptions{Mode: updates.UpgradeDefault, Snapshot: true}
}

// send delivers one message and runs nothing in the background: the returned
// command is dropped, so a test drives the model rather than the runtime.
func send(a *app, msg tea.Msg) tea.Cmd {
	_, cmd := a.Update(msg)
	return cmd
}

// key delivers one key press.
func key(a *app, name string) tea.Cmd {
	if len(name) == 1 {
		return send(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)})
	}
	return send(a, tea.KeyMsg{Type: keyTypes[name]})
}

// keyTypes are the named keys the tests press.
var keyTypes = map[string]tea.KeyType{
	"enter": tea.KeyEnter,
	"esc":   tea.KeyEscape,
	"down":  tea.KeyDown,
	"up":    tea.KeyUp,
}

// TestOpeningScreenListsThePendingUpdates is the screen the tool opens on.
func TestOpeningScreenListsThePendingUpdates(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	view := a.View()
	for _, want := range []string{
		"tui-update", "kernel", "openssl", "14 pending", "security",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the opening screen does not mention %q:\n%s", want, view)
		}
	}
	// Kernel and firmware come first, whatever the alphabet says.
	if a.visible[0].Group != updates.GroupKernel {
		t.Errorf("first row is %+v, want a kernel", a.visible[0])
	}
}

// TestWidthsNeverOverflow is the family's responsiveness rule, asserted: from
// a narrow pane to a wide terminal, no rendered line may be wider than the
// terminal, on any screen.
func TestWidthsNeverOverflow(t *testing.T) {
	screens := []struct {
		name string
		keys []string
	}{
		{"pending", nil},
		{"plan", []string{"p"}},
		{"history", []string{"h"}},
		{"timers", []string{"t"}},
		{"help", []string{"?"}},
	}
	for _, width := range []int{40, 60, 80, 100, 140, 200} {
		for _, screen := range screens {
			a, fake := newTestApp(t, width, 24)
			for _, k := range screen.keys {
				key(a, k)
			}
			// The plan and history screens are filled by a background read,
			// which a test delivers itself.
			primeScreen(t, a, fake, screen.name)

			for i, line := range strings.Split(a.View(), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("%s at width %d: line %d is %d cells wide:\n%q",
						screen.name, width, i, got, line)
				}
			}
		}
	}
}

// primeScreen delivers the message the screen's own background read would
// have produced.
func primeScreen(t *testing.T, a *app, fake *pkgmgr.Fake, name string) {
	t.Helper()
	switch name {
	case "plan":
		plan, err := fake.Plan(t.Context(), defaultPlanOptions())
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		send(a, plannedMsg{plan: plan})
	case "history":
		history, err := fake.History(t.Context())
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		send(a, historyMsg{transactions: history})
	}
}

// TestPlanShowsTheWholeSequence: the plan screen is where a reader decides,
// so it has to carry the restart classification, the snapshot answer and
// every command, in order.
func TestPlanShowsTheWholeSequence(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	key(a, "p")
	primeScreen(t, a, fake, "plan")
	if a.mode != modePlan {
		t.Fatalf("mode = %v, want the plan screen", a.mode)
	}

	view := strings.Join(a.planLines(), "\n")
	for _, want := range []string{
		"Restart classification",
		"snapshot before: yes",
		"snapper create -c root -t pre",
		"snapper create -c root -t post",
		"dnf makecache --refresh -q",
		"dnf -y upgrade",
		"never reboots by itself",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, view)
		}
	}

	// The order is the argument: snapshot, refresh, upgrade, snapshot.
	want := []string{
		"snapper create -c root -t pre",
		"dnf makecache --refresh -q",
		"dnf -y upgrade",
		"snapper create -c root -t post",
	}
	if len(a.plan.Commands) != len(want) {
		t.Fatalf("the plan has %d commands, want %d: %v",
			len(a.plan.Commands), len(want), a.plan.Preview())
	}
	for i, prefix := range want {
		if !strings.HasPrefix(a.plan.Commands[i].String(), prefix) {
			t.Errorf("command %d is %q, want it to start with %q",
				i, a.plan.Commands[i].String(), prefix)
		}
	}
}

// TestApplyConfirmsBeforeRunningAnything is the family's central promise on
// this tool: U opens a dialog carrying every command, and nothing has run
// when it does.
func TestApplyConfirmsBeforeRunningAnything(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	key(a, "U")
	plan, err := fake.Plan(t.Context(), defaultPlanOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	send(a, plannedMsg{plan: plan, apply: true})

	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm dialog", a.mode)
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("something ran before the dialog was answered: %v", fake.Ran())
	}
	if !a.confirm.Danger {
		t.Errorf("an upgrade is a destructive change and must be painted so")
	}
	for _, want := range []string{
		"snapper create -c root -t pre", "dnf -y upgrade",
		"snapper create -c root -t post",
	} {
		if !strings.Contains(a.confirm.Command, want) {
			t.Errorf("the dialog does not show %q:\n%s", want, a.confirm.Command)
		}
	}
	if !strings.Contains(a.confirm.Body, "never reboots by itself") {
		t.Errorf("the dialog does not say the tool will not reboot:\n%s",
			a.confirm.Body)
	}

	// Answering no runs nothing at all.
	key(a, "n")
	if len(fake.Ran()) != 0 {
		t.Errorf("declining still ran %v", fake.Ran())
	}
}

// TestApplyRunsTheSequenceInOrder drives the whole apply screen: one command
// at a time, each one starting only after the previous one answered.
func TestApplyRunsTheSequenceInOrder(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	key(a, "U")
	plan, err := fake.Plan(t.Context(), defaultPlanOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	send(a, plannedMsg{plan: plan, apply: true})
	key(a, "y")

	if a.mode != modeApply {
		t.Fatalf("mode = %v, want the apply screen", a.mode)
	}
	for step := 0; step < len(plan.Commands); step++ {
		cmd := plan.Commands[step]
		out, runErr := fake.Run(t.Context(), cmd)
		send(a, stepMsg{index: step, cmd: cmd, output: out, err: runErr})
	}
	if !a.applyDone {
		t.Errorf("the sequence did not finish")
	}
	if a.busy {
		t.Errorf("the app is still busy after the last command")
	}

	ran := fake.Ran()
	if len(ran) != len(plan.Commands) {
		t.Fatalf("ran %d commands, want %d", len(ran), len(plan.Commands))
	}
	for i, cmd := range plan.Commands {
		if ran[i].String() != cmd.String() {
			t.Errorf("command %d: ran %q, previewed %q",
				i, ran[i].String(), cmd.String())
		}
	}
	// The pane carries what each command printed. Thirteen of the fourteen:
	// the sample machine holds nginx, and a held package is precisely one an
	// upgrade does not touch.
	log := strings.Join(a.applyLog, "\n")
	if !strings.Contains(log, "Upgraded 13 packages") {
		t.Errorf("the apply pane does not carry the upgrade output:\n%s", log)
	}
	after, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(after.Pending) != 1 || after.Pending[0].Name != "nginx" {
		t.Errorf("after the upgrade the pending list is %v, want the held "+
			"package alone", after.Pending)
	}
}

// TestRebootIsOfferedButNeverTaken: the tool never reboots by itself, and the
// key that offers it opens a confirm dialog like everything else.
func TestRebootIsOfferedButNeverTaken(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	a.mode, a.applyDone = modeApply, true
	plan, err := fake.Plan(t.Context(), defaultPlanOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a.plan = plan

	// The hint bar only offers it once the sequence asked for one.
	hints := a.applyHelpKeys()
	found := false
	for _, hint := range hints {
		if hint.Key == "R" {
			found = true
		}
	}
	if !found {
		t.Errorf("the apply screen does not offer the reboot: %+v", hints)
	}

	key(a, "R")
	if a.mode != modeConfirm {
		t.Fatalf("R did not open a confirm dialog (mode %v)", a.mode)
	}
	if a.confirm.Command != "sudo -n systemctl reboot" {
		t.Errorf("the dialog previews %q", a.confirm.Command)
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("R rebooted without being confirmed: %v", fake.Ran())
	}
}

// TestTimerActionsArePreviewed: turning unattended updates on or off is a
// change to what the machine does unattended, so it goes through the dialog.
func TestTimerActionsArePreviewed(t *testing.T) {
	a, fake := newTestApp(t, 120, 30)
	key(a, "t")
	if a.mode != modeTimers {
		t.Fatalf("mode = %v, want the timers screen", a.mode)
	}
	key(a, "e")
	if a.mode != modeConfirm {
		t.Fatalf("e did not open a confirm dialog (mode %v)", a.mode)
	}
	if a.confirm.Command != "sudo -n systemctl enable --now dnf-automatic.timer" {
		t.Errorf("the dialog previews %q", a.confirm.Command)
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("the timer changed before being confirmed: %v", fake.Ran())
	}
	// Confirming returns the background command; running it is what a real
	// session's runtime would do.
	if cmd := key(a, "y"); cmd != nil {
		send(a, cmd())
	}
	if len(fake.Ran()) != 1 {
		t.Fatalf("ran %v", fake.Ran())
	}
	if got := fake.Ran()[0].String(); got != "systemctl enable --now dnf-automatic.timer" {
		t.Errorf("ran %q, which is not what was previewed", got)
	}
}

// TestFilterMatchesEveryColumn.
func TestFilterMatchesEveryColumn(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	a.filter = "openssl"
	a.applyFilter()
	if len(a.visible) != 2 {
		t.Errorf("filtering on openssl left %d packages", len(a.visible))
	}
	a.filter = "FEDORA-2026-9a1f2b3c4d"
	a.applyFilter()
	if len(a.visible) != 2 {
		t.Errorf("filtering on an advisory id left %d packages", len(a.visible))
	}
	a.filter = "nothing-here"
	a.applyFilter()
	if len(a.visible) != 0 {
		t.Errorf("filtering on nonsense left %d packages", len(a.visible))
	}
	if !strings.Contains(a.View(), "no package matches") {
		t.Errorf("an empty filter result must say so")
	}
}

// TestSecurityColumnSaysNotApplicable on a manager that publishes no security
// metadata: "n/a" and "no" are different claims.
func TestSecurityColumnSaysNotApplicable(t *testing.T) {
	pacman := updates.Capabilities{Manager: updates.ManagerPacman}
	if got := securityCell(updates.Package{}, pacman); got != "n/a" {
		t.Errorf("pacman security cell = %q, want n/a", got)
	}
	dnf := updates.Capabilities{SecurityMetadata: true}
	if got := securityCell(updates.Package{}, dnf); got == "n/a" {
		t.Errorf("dnf can tell, so the cell must not read n/a")
	}
	if got := securityCell(updates.Package{Security: true}, dnf); got != "yes" {
		t.Errorf("a security update reads %q", got)
	}
}

// TestRestartSentence is what both the header and the confirm dialog say in
// one line, for each of the three verdicts.
func TestRestartSentence(t *testing.T) {
	if got := restartSentence(updates.Restart{Class: updates.RestartNone}); !strings.
		Contains(got, "Nothing running") {
		t.Errorf("none = %q", got)
	}
	got := restartSentence(updates.Restart{
		Class: updates.RestartServices, Services: []string{"sshd", "nginx"},
	})
	if !strings.Contains(got, "sshd, nginx") {
		t.Errorf("services = %q", got)
	}
	got = restartSentence(updates.Restart{
		Class: updates.RestartReboot, Reason: "linux 1 → 2",
	})
	if !strings.Contains(got, "reboot") || !strings.Contains(got, "linux 1 → 2") {
		t.Errorf("reboot = %q", got)
	}
}

// TestUpgradeModeCycle walks m through every manager's real cycle.
//
// The point of the table is the third column: a mode is offered only where the
// manager can really do it. apt publishes security metadata and still cannot
// upgrade only the security fixes, so `security` never appears there, and
// pacman — which publishes no metadata at all — has nothing to cycle.
func TestUpgradeModeCycle(t *testing.T) {
	tests := []struct {
		name  string
		caps  updates.Capabilities
		cycle []string
	}{
		{
			name:  "pacman has one kind of upgrade",
			caps:  pkgmgr.CapabilitiesFor(updates.ManagerPacman),
			cycle: []string{updates.UpgradeDefault},
		},
		{
			name: "apt cycles the two upgrades it really has",
			caps: pkgmgr.CapabilitiesFor(updates.ManagerAPT),
			cycle: []string{
				updates.UpgradeDefault, updates.UpgradeDist,
				updates.UpgradeDefault,
			},
		},
		{
			name: "dnf cycles the plain upgrade and the security-only one",
			caps: pkgmgr.CapabilitiesFor(updates.ManagerDNF),
			cycle: []string{
				updates.UpgradeDefault, updates.UpgradeSecurity,
				updates.UpgradeDefault,
			},
		},
		{
			name: "a manager with everything walks all three",
			caps: updates.Capabilities{
				SecurityMetadata: true, SecurityUpgrade: true, DistUpgrade: true,
			},
			cycle: []string{
				updates.UpgradeDefault, updates.UpgradeDist,
				updates.UpgradeSecurity, updates.UpgradeDefault,
			},
		},
	}
	for _, test := range tests {
		mode := updates.UpgradeDefault
		for i, want := range test.cycle {
			if i > 0 {
				mode = updates.NextUpgradeMode(test.caps, mode)
			}
			if mode != want {
				t.Errorf("%s: step %d is %q, want %q", test.name, i, mode, want)
			}
		}
	}
}

// TestSecurityOnlyIsSkippedWithoutTheCapability is the same rule stated as the
// one thing that must never happen: a reader who asked for the security fixes
// is handed the full upgrade instead.
func TestSecurityOnlyIsSkippedWithoutTheCapability(t *testing.T) {
	for _, manager := range []string{
		updates.ManagerPacman, updates.ManagerAPT,
	} {
		caps := pkgmgr.CapabilitiesFor(manager)
		for _, mode := range updates.UpgradeModes(caps) {
			if mode == updates.UpgradeSecurity {
				t.Errorf("%s offers a security-only upgrade it cannot perform",
					manager)
			}
		}
		if _, err := pkgmgr.BuildUpgrade(manager, updates.UpgradeSecurity,
			false); err == nil {
			t.Errorf("%s built a security-only upgrade command", manager)
		}
	}
}

// TestModeKeyIsRefusedWhereThereIsNothingToCycle: a manager with one kind of
// upgrade says so rather than silently doing nothing.
func TestModeKeyIsRefusedWhereThereIsNothingToCycle(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	a.caps = pkgmgr.CapabilitiesFor(updates.ManagerPacman)
	a.mode = modePlan
	key(a, "m")
	if a.upgradeMode != updates.UpgradeDefault {
		t.Errorf("mode changed to %q on a manager with one upgrade",
			a.upgradeMode)
	}
	if !strings.Contains(a.status, "one kind of upgrade") {
		t.Errorf("status = %q", a.status)
	}
}

// TestSecurityOnlyPlanRunsTheNarrowedTransaction: on the sample machine m
// reaches the security mode, and the plan it builds carries `--security` on
// the one command that changes anything.
func TestSecurityOnlyPlanRunsTheNarrowedTransaction(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	a.mode = modePlan
	key(a, "m")
	if a.upgradeMode != updates.UpgradeSecurity {
		t.Fatalf("m reached %q, want the security-only mode", a.upgradeMode)
	}

	plan, err := fake.Plan(t.Context(), updates.PlanOptions{
		Mode: updates.UpgradeSecurity, Snapshot: true,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	send(a, plannedMsg{plan: plan})

	preview := plan.Preview()
	if !strings.Contains(preview, "dnf -y upgrade --security") {
		t.Errorf("the security-only plan runs:\n%s", preview)
	}
	if !strings.Contains(plan.Title, "security fix") {
		t.Errorf("title = %q, want it to name what is being applied", plan.Title)
	}
	// The dry run shows the security fixes alone, so the narrowing is visible
	// before the confirm dialog rather than only in the argv.
	for _, want := range []string{"openssl", "kernel"} {
		if !strings.Contains(plan.DryRun, want) {
			t.Errorf("the security dry run does not list %q:\n%s", want, plan.DryRun)
		}
	}
	if strings.Contains(plan.DryRun, "vim-minimal") {
		t.Errorf("the security dry run lists a package with no advisory:\n%s",
			plan.DryRun)
	}
}

// TestSnapshotToggleRePlansBeforeTheConfirm is the whole point of the s key:
// the snapshot commands appear and disappear on the plan a reader is looking
// at, not somewhere between the confirm dialog and the machine.
func TestSnapshotToggleRePlansBeforeTheConfirm(t *testing.T) {
	a, fake := newTestApp(t, 120, 40)
	key(a, "p")
	primeScreen(t, a, fake, "plan")

	with := strings.Join(a.planLines(), "\n")
	if !strings.Contains(with, "snapper create -c root -t pre") ||
		!strings.Contains(with, "snapshot before: yes") {
		t.Fatalf("the plan opens without the snapshot:\n%s", with)
	}

	// s turns it off and re-plans, which the test delivers itself.
	key(a, "s")
	if a.snapshot {
		t.Fatalf("s did not turn the snapshot off")
	}
	off, err := fake.Plan(t.Context(), updates.PlanOptions{
		Mode: updates.UpgradeDefault, Snapshot: false,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	send(a, plannedMsg{plan: off})

	without := strings.Join(a.planLines(), "\n")
	if strings.Contains(without, "snapper create") {
		t.Errorf("the snapshot survived being turned off:\n%s", without)
	}
	if !strings.Contains(without, "turned off") {
		t.Errorf("the plan does not say the snapshot was declined:\n%s", without)
	}
	for _, cmd := range off.Commands {
		if strings.HasPrefix(cmd.String(), "snapper") {
			t.Errorf("the sequence still takes a snapshot: %v", off.Preview())
		}
	}
	// And the machine's own answer is untouched: it can still take one.
	if !off.Snapshot.Available {
		t.Errorf("turning the snapshot off changed what the machine can do")
	}

	// s again puts it back, and the confirm dialog carries the pair.
	key(a, "s")
	if !a.snapshot {
		t.Fatalf("s did not turn the snapshot back on")
	}
	send(a, plannedMsg{plan: mustPlan(t, fake, defaultPlanOptions())})
	key(a, "U")
	if a.mode != modeConfirm {
		t.Fatalf("U did not open the confirm dialog (mode %v)", a.mode)
	}
	if !strings.Contains(a.confirm.Command, "snapper create -c root -t pre") {
		t.Errorf("the dialog lost the snapshot:\n%s", a.confirm.Command)
	}
}

// TestSnapshotToggleIsRefusedWithNowhereToSnapshot.
func TestSnapshotToggleIsRefusedWithNowhereToSnapshot(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	a.model.Snapshot = updates.Snapshot{Reason: "snapper is not installed"}
	a.mode = modePlan
	key(a, "s")
	if !a.snapshot {
		t.Errorf("the toggle moved on a machine with nowhere to snapshot")
	}
	if !strings.Contains(a.status, "snapper is not installed") {
		t.Errorf("status = %q, want the machine's own reason", a.status)
	}
}

// TestHoldIsPreviewedAndToggles: H holds the selected package and lifts a hold
// it already carries, both behind the same confirm dialog as everything else.
func TestHoldIsPreviewedAndToggles(t *testing.T) {
	a, fake := newTestApp(t, 120, 30)

	// The sample machine holds nginx, so the key on that row lifts it.
	a.cursor = indexOf(t, a, "nginx")
	key(a, "H")
	if a.mode != modeConfirm {
		t.Fatalf("H did not open a confirm dialog (mode %v)", a.mode)
	}
	if a.confirm.Command != "sudo -n dnf versionlock delete nginx" {
		t.Errorf("the dialog previews %q", a.confirm.Command)
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("the hold changed before being confirmed: %v", fake.Ran())
	}
	if cmd := key(a, "y"); cmd != nil {
		send(a, cmd())
	}
	if got := fake.Ran()[0].String(); got != "dnf versionlock delete nginx" {
		t.Errorf("ran %q, which is not what was previewed", got)
	}

	// And on a package that is not held, it places one.
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	send(a, loadedMsg{model: model})
	a.cursor = indexOf(t, a, "vim-minimal")
	key(a, "H")
	if a.confirm.Command != "sudo -n dnf versionlock add vim-minimal" {
		t.Errorf("the dialog previews %q", a.confirm.Command)
	}
	if !a.confirm.Danger {
		t.Errorf("holding a package stops its security fixes and must be " +
			"painted as the change it is")
	}
}

// TestHoldIsRefusedWithoutTheVersionlockPlugin: the refusal names the package
// to install, rather than letting dnf fail about an unknown command.
func TestHoldIsRefusedWithoutTheVersionlockPlugin(t *testing.T) {
	fake := pkgmgr.NewFakeWithoutVersionlock()
	a := newApp(fake, theme.FromPalette(theme.TokyoNight()), compat.Result{})
	send(a, tea.WindowSizeMsg{Width: 120, Height: 30})
	model, err := fake.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	send(a, loadedMsg{model: model})

	key(a, "H")
	if a.mode == modeConfirm {
		t.Fatalf("a hold was previewed on a machine that cannot hold anything")
	}
	if len(fake.Ran()) != 0 {
		t.Errorf("something ran anyway: %v", fake.Ran())
	}
	for _, want := range []string{"versionlock", pkgmgr.VersionlockFedora} {
		if !strings.Contains(a.status, want) {
			t.Errorf("the refusal does not mention %q: %q", want, a.status)
		}
	}
}

// indexOf finds a package on the visible list, so a test can put the cursor
// on it by name rather than by a position the sort could move.
func indexOf(t *testing.T, a *app, name string) int {
	t.Helper()
	for i, p := range a.visible {
		if p.Name == name {
			return i
		}
	}
	t.Fatalf("%q is not on the pending list", name)
	return 0
}

// mustPlan builds a plan or fails the test.
func mustPlan(t *testing.T, fake *pkgmgr.Fake,
	opts updates.PlanOptions) updates.Plan {
	t.Helper()
	plan, err := fake.Plan(t.Context(), opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

// TestEmptyMachineSaysSo.
func TestEmptyMachineSaysSo(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	send(a, loadedMsg{model: updates.Model{Manager: "dnf"}})
	if !strings.Contains(a.View(), "up to date") {
		t.Errorf("a machine with nothing pending must say so:\n%s", a.View())
	}
	key(a, "p")
	if !strings.Contains(a.status, "nothing to upgrade") {
		t.Errorf("status = %q", a.status)
	}
}

// TestPendingFailureIsNotAnUpToDateMachine: on Omarchy Server 4.0.1
// `checkupdates` cannot run, and the whole screen used to go with it. The
// pending list is one section of the model; when it fails the rest still
// arrives, and the empty table says the list could not be read rather than
// claiming there is nothing to install.
func TestPendingFailureIsNotAnUpToDateMachine(t *testing.T) {
	a, _ := newTestApp(t, 120, 30)
	send(a, loadedMsg{model: updates.Model{
		Manager:      "pacman",
		PendingError: "`checkupdates` failed: Cannot find the fakeroot binary",
		Timers: []updates.Timer{{
			Unit: "omarchy-server-update.timer", Present: true, State: "disabled",
		}},
		Snapshot: updates.Snapshot{Available: true, Config: "root"},
	}})

	view := a.View()
	if strings.Contains(view, "up to date") {
		t.Errorf("a failed read must not read as up to date:\n%s", view)
	}
	if !strings.Contains(a.status, "fakeroot") {
		t.Errorf("status = %q, want the reason the read failed", a.status)
	}
	// The sections that did load are still there to be looked at.
	if len(a.model.Timers) != 1 || !a.model.Snapshot.Available {
		t.Error("the rest of the model was dropped with the pending list")
	}
}

// TestHelpScreenCoversEveryActionKey: a key the tool answers that the help
// screen does not name is a key nobody will find.
func TestHelpScreenCoversEveryActionKey(t *testing.T) {
	help := helpKeys()
	for _, want := range []string{
		"U", "m", "s", "h", "H", "t", "e / d", "R", "/", "q",
	} {
		found := false
		for _, hint := range help {
			if hint.Key == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the help screen does not mention %q", want)
		}
	}
}
