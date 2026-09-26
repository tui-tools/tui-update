package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
	"github.com/tui-tools/tui-update/internal/updates"
)

// startApply opens the confirm dialog on the sample machine's default plan
// (snapshot on) and accepts it, returning the plan and the first step's
// command.
func startApply(t *testing.T) (*app, *pkgmgr.Fake, updates.Plan) {
	t.Helper()
	a, fake := newTestApp(t, 120, 40)
	key(a, "U")
	plan, err := fake.Plan(t.Context(), defaultPlanOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.TakeSnapshot {
		t.Fatal("the sample plan takes no snapshot")
	}
	send(a, plannedMsg{plan: plan, apply: true})
	return a, fake, plan
}

// TestSnapshotPairIsPairedByThePreNumber: snapper refuses a post snapshot
// without --pre-number, and that number only exists once the pre snapshot
// ran. The dialog says where it goes; the sequence carries it over.
func TestSnapshotPairIsPairedByThePreNumber(t *testing.T) {
	a, fake, plan := startApply(t)
	if !strings.Contains(a.confirm.Command,
		"-t post --pre-number '"+updates.PreNumber+"'") {
		t.Errorf("the dialog does not show where the pre number goes:\n%s",
			a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, "the number the pre snapshot prints") {
		t.Errorf("the dialog does not explain the pre number:\n%s",
			a.confirm.Body)
	}

	next := key(a, "y")
	for step := range plan.Commands {
		msg, ok := next().(stepMsg)
		if !ok {
			t.Fatalf("step %d: the app did not start a step", step)
		}
		if msg.err != nil {
			t.Fatalf("step %d (%s): %v", step, msg.cmd, msg.err)
		}
		next = send(a, msg)
	}

	ran := fake.Ran()
	post := ran[len(ran)-1]
	if !strings.Contains(post.String(), "-t post --pre-number 42 ") {
		t.Errorf("the post snapshot ran as %q, want it paired with 42", post)
	}
	for _, cmd := range ran {
		if strings.Contains(cmd.String(), updates.PreNumber) {
			t.Errorf("%q ran with the placeholder still in it", cmd)
		}
	}
	log := strings.Join(a.applyLog, "\n")
	if !strings.Contains(log, "--pre-number 42 ") {
		t.Errorf("the apply pane does not show the post argv as it ran:\n%s", log)
	}
	if !strings.Contains(log, "pre snapshot 42") {
		t.Errorf("the apply pane does not name the pre snapshot:\n%s", log)
	}
	// The dialog's plan is untouched: it still shows what was confirmed.
	if !strings.Contains(plan.Commands[len(plan.Commands)-1].String(),
		updates.PreNumber) {
		t.Errorf("binding the number changed the confirmed plan")
	}
}

// TestFailedPreSnapshotRunsNothingElse: without a pre snapshot the upgrade is
// not run silently; the sequence stops where it failed.
func TestFailedPreSnapshotRunsNothingElse(t *testing.T) {
	a, _, plan := startApply(t)
	key(a, "y")
	next := send(a, stepMsg{index: 0, cmd: plan.Commands[0],
		output: "Creating snapshot failed.", err: errors.New("exit status 1")})

	if !a.applyDone || a.busy {
		t.Errorf("done=%v busy=%v after the failed pre snapshot",
			a.applyDone, a.busy)
	}
	if !strings.Contains(a.status, "the sequence stopped at snapper -c root create") {
		t.Errorf("status = %q", a.status)
	}
	if strings.Contains(a.status, "delete") {
		t.Errorf("status names a pre snapshot that was never taken: %q", a.status)
	}
	if _, ok := next().(stepMsg); ok {
		t.Errorf("a step started after the failed pre snapshot")
	}
}

// TestPreSnapshotWithoutANumberStops: a pre snapshot that exited zero but
// printed no number leaves nothing to pair the post one with, so the upgrade
// does not start either.
func TestPreSnapshotWithoutANumberStops(t *testing.T) {
	a, _, plan := startApply(t)
	key(a, "y")
	next := send(a, stepMsg{index: 0, cmd: plan.Commands[0],
		output: "something unexpected"})

	if !a.applyDone || a.busy {
		t.Errorf("done=%v busy=%v", a.applyDone, a.busy)
	}
	if a.statusKind != ui.StatusError {
		t.Errorf("status = %q, want an error", a.status)
	}
	if !strings.Contains(strings.Join(a.applyLog, "\n"), "printed no number") {
		t.Errorf("the pane does not say why it stopped:\n%s",
			strings.Join(a.applyLog, "\n"))
	}
	if _, ok := next().(stepMsg); ok {
		t.Errorf("a step started after a pre snapshot with no number")
	}
}

// TestFailedPostNamesThePreSnapshot: once the pre snapshot exists, a failure
// after it leaves that snapshot without a partner, and the reader is told its
// number and how to remove it.
func TestFailedPostNamesThePreSnapshot(t *testing.T) {
	a, _, plan := startApply(t)
	next := key(a, "y")
	last := len(plan.Commands) - 1
	for step := 0; step < last; step++ {
		msg, ok := next().(stepMsg)
		if !ok || msg.err != nil {
			t.Fatalf("step %d did not run cleanly: %+v", step, msg)
		}
		next = send(a, msg)
	}
	send(a, stepMsg{index: last, cmd: a.plan.Commands[last],
		output: "Missing or invalid pre-number.", err: errors.New("exit status 1")})

	want := "pre snapshot 42 has no post snapshot; `snapper -c root delete 42` removes it"
	if !strings.Contains(a.status, want) {
		t.Errorf("status = %q, want it to contain %q", a.status, want)
	}
	if !strings.Contains(strings.Join(a.applyLog, "\n"), want) {
		t.Errorf("the pane does not name the pre snapshot:\n%s",
			strings.Join(a.applyLog, "\n"))
	}
}
