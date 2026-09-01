package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-update/internal/updates"
)

// mode is the screen the app currently shows. Only one dialog is open at a
// time, which keeps the update loop flat.
type mode int

const (
	// modePending is the opening screen: what is waiting to be installed.
	modePending mode = iota
	// modePlan is what applying it would do.
	modePlan
	// modeApply is the sequence running, with its output.
	modeApply
	// modeHistory is the manager's own transaction log, read-only.
	modeHistory
	// modeTimers is the unattended-update state.
	modeTimers
	modeConfirm
	modeFilter
	modeHelp
)

// loadTimeout bounds a read, and runTimeout one command of an upgrade. An
// upgrade downloads packages, so its budget is the generous one.
const (
	loadTimeout = 60 * time.Second
	runTimeout  = 60 * time.Minute
)

// app is the tui-update Bubble Tea model.
type app struct {
	backend updates.Backend
	theme   theme.Theme
	caps    updates.Capabilities
	// backendCompat is what the version probe found, rendered in the header.
	backendCompat compat.Result

	model updates.Model
	// visible holds the packages left after the filter, in display order.
	visible []updates.Package
	// history is the manager's transaction log, read when the screen opens.
	history []updates.Transaction
	// plan is the sequence the plan screen is showing.
	plan updates.Plan
	// upgradeMode is one of the updates.Upgrade* constants. Which of them the
	// m key can reach is the backend's answer, not this file's.
	upgradeMode string
	// snapshot is whether the plan wraps the upgrade in the snapper pre/post
	// pair. It starts on, because a snapshot that has to be asked for is one
	// nobody takes, and s turns it off for a machine where the extra subvolume
	// is not wanted.
	snapshot bool

	width, height int
	cursor        int
	offset        int
	filter        string
	// timerCursor is the selection on the timers screen, kept separately so
	// switching screens does not move the package list.
	timerCursor int
	// scroll is the offset of the plan, apply and history screens.
	scroll int

	mode mode
	// previous is the screen a dialog returns to.
	previous mode
	confirm  ui.Confirm
	input    ui.Input

	// applyLog is what the running sequence has printed so far, and applyStep
	// which command of it is next.
	applyLog  []string
	applyStep int
	// applyDone reports that the sequence finished, so the screen can offer
	// the reboot.
	applyDone bool

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last Load returned an error, so the empty
	// state does not claim the machine simply has nothing to install.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
}

// loadedMsg carries the result of a Load.
type loadedMsg struct {
	model updates.Model
	err   error
}

// historyMsg carries the result of a history read.
type historyMsg struct {
	transactions []updates.Transaction
	err          error
}

// plannedMsg carries the result of building a plan.
type plannedMsg struct {
	plan updates.Plan
	err  error
	// apply reports that the plan was asked for in order to run it, so the
	// confirm dialog opens as soon as it arrives.
	apply bool
}

// stepMsg carries the result of one command of a running sequence. The
// sequence is driven one command at a time rather than in a single
// background call, so the pane fills as the upgrade progresses instead of
// staying empty until it is over.
type stepMsg struct {
	index  int
	cmd    updates.Command
	output string
	err    error
}

// ranMsg carries the result of a single confirmed command that is not part of
// an upgrade sequence: a timer change, or the reboot.
type ranMsg struct {
	title  string
	output string
	err    error
}

// plan is what a confirm dialog is holding: one or more commands, run in
// order, and whether they are the upgrade sequence.
type pending struct {
	title    string
	commands []updates.Command
	// sequence marks the upgrade, which opens the apply screen rather than
	// running quietly in the background.
	sequence bool
}

// newApp builds the model around a backend.
func newApp(backend updates.Backend, th theme.Theme,
	backendCompat compat.Result) *app {
	a := &app{
		backend:       backend,
		theme:         th,
		caps:          backend.Capabilities(),
		backendCompat: backendCompat,
		upgradeMode:   updates.UpgradeDefault,
		snapshot:      true,
		width:         80,
		height:        24,
		loading:       true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first load.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads the pending updates in the background.
func (a *app) load() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		model, err := backend.Load(ctx)
		return loadedMsg{model: model, err: err}
	}
}

// loadHistory reads the manager's transaction log in the background.
func (a *app) loadHistory() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		transactions, err := backend.History(ctx)
		return historyMsg{transactions: transactions, err: err}
	}
}

// buildPlan runs the manager's dry run and assembles the sequence.
func (a *app) buildPlan(apply bool) tea.Cmd {
	backend := a.backend
	opts := updates.PlanOptions{Mode: a.upgradeMode, Snapshot: a.snapshot}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		p, err := backend.Plan(ctx, opts)
		return plannedMsg{plan: p, err: err, apply: apply}
	}
}

// step runs one command of the upgrade sequence in the background.
func (a *app) step(index int, cmd updates.Command) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		out, err := backend.Run(ctx, cmd)
		return stepMsg{index: index, cmd: cmd, output: out, err: err}
	}
}

// runOne executes a single confirmed command in the background.
func (a *app) runOne(title string, cmd updates.Command) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		out, err := backend.Run(ctx, cmd)
		return ranMsg{title: title, output: out, err: err}
	}
}

// setStatus records a plain message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.model = msg.model
		a.applyFilter()
		// A model that arrived with a pending-list failure is still worth
		// showing — the timers, the snapshot support and the history in it are
		// real — but the empty package table is not "up to date", and the
		// status line has to be the one saying which.
		a.loadFailed = msg.model.PendingError != ""
		if a.loadFailed {
			a.setStatus(ui.StatusError,
				"the pending list could not be read: "+msg.model.PendingError)
		}
		return a, nil

	case historyMsg:
		a.loading = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.history = msg.transactions
		return a, nil

	case plannedMsg:
		return a.handlePlanned(msg)

	case stepMsg:
		return a.handleStep(msg)

	case ranMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, a.load()
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.title, firstLine(summary))
		a.loading = true
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// handlePlanned opens the plan screen, or the confirm dialog when the plan
// was asked for in order to run it.
func (a *app) handlePlanned(msg plannedMsg) (tea.Model, tea.Cmd) {
	a.loading = false
	if msg.err != nil {
		a.setStatus(ui.StatusError, msg.err.Error())
		return a, nil
	}
	a.plan, a.scroll = msg.plan, 0
	a.setStatus(ui.StatusInfo, "")
	if !msg.apply {
		a.mode = modePlan
		return a, nil
	}
	a.previous = modePlan
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   msg.plan.Title,
		Body:    a.applyBody(msg.plan),
		Command: a.previewAll(msg.plan.Commands),
		Danger:  true,
		Payload: pending{
			title:    msg.plan.Title,
			commands: msg.plan.Commands,
			sequence: true,
		},
	}
	return a, nil
}

// applyBody is what the confirm dialog says above the command sequence: the
// two things a reader has to weigh before agreeing.
func (a *app) applyBody(p updates.Plan) string {
	lines := []string{restartSentence(p.Restart)}
	switch {
	case p.TakeSnapshot:
		lines = append(lines, "A pre-upgrade snapshot is taken first, and a "+
			"post one after; `snapper status` between them lists what changed.")
	case p.Snapshot.Available:
		lines = append(lines, "No snapshot: this machine could take one, and "+
			"it was turned off for this plan.")
	default:
		lines = append(lines, "No snapshot: "+p.Snapshot.Reason+".")
	}
	if p.Mode == updates.UpgradeSecurity {
		lines = append(lines, "Only the packages carrying a security advisory "+
			"are upgraded; everything else stays where it is.")
	}
	lines = append(lines, "tui-update never reboots by itself.")
	return strings.Join(lines, "\n")
}

// handleStep records one finished command and starts the next one.
func (a *app) handleStep(msg stepMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.busy, a.applyDone = false, true
		a.applyLog = append(a.applyLog, "✗ "+firstLine(msg.err.Error()))
		if out := strings.TrimSpace(msg.output); out != "" {
			a.applyLog = append(a.applyLog, splitOutput(out)...)
		}
		a.setStatus(ui.StatusError, "the sequence stopped at "+msg.cmd.String())
		a.scrollToEnd()
		return a, a.load()
	}
	if out := strings.TrimSpace(msg.output); out != "" {
		a.applyLog = append(a.applyLog, splitOutput(out)...)
	}
	a.applyLog = append(a.applyLog, "✓ done")

	a.applyStep = msg.index + 1
	if a.applyStep < len(a.plan.Commands) {
		next := a.plan.Commands[a.applyStep]
		a.applyLog = append(a.applyLog, "", "$ "+a.backend.Preview(next))
		a.scrollToEnd()
		return a, a.step(a.applyStep, next)
	}

	a.busy, a.applyDone = false, true
	a.applyLog = append(a.applyLog, "",
		"The sequence finished. "+restartSentence(a.plan.Restart))
	a.setStatus(ui.StatusOK, "upgrade finished")
	a.scrollToEnd()
	return a, a.load()
}

// splitOutput turns a command's output into pane lines.
func splitOutput(out string) []string {
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

// scrollToEnd keeps the apply pane pinned to the newest output.
func (a *app) scrollToEnd() {
	a.scroll = max(len(a.applyLog)-a.paneHeight(), 0)
}

// handleKey routes a key press to the open dialog, or to the current screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy && a.mode != modeApply {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modeHelp:
		a.mode = modePending
		return a, nil
	case modePlan:
		return a.handlePlanKey(msg)
	case modeApply:
		return a.handleApplyKey(msg)
	case modeHistory:
		return a.handleScrollScreen(msg, len(a.history))
	case modeTimers:
		return a.handleTimersKey(msg)
	default:
		return a.handlePendingKey(msg)
	}
}

// handleConfirm resolves the confirm dialog.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = a.previous
	confirmed := a.confirm.Confirmed
	answer, ok := a.confirm.Payload.(pending)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok || len(answer.commands) == 0 {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	if !answer.sequence {
		a.setStatusf(ui.StatusInfo, "running %s…",
			a.backend.Preview(answer.commands[0]))
		return a, a.runOne(answer.title, answer.commands[0])
	}

	a.mode = modeApply
	a.applyStep, a.applyDone = 0, false
	a.applyLog = []string{
		answer.title,
		"",
		"$ " + a.backend.Preview(answer.commands[0]),
	}
	a.scrollToEnd()
	return a, a.step(0, answer.commands[0])
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.applyFilter()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.applyFilter()
	a.mode = modePending
	return a, nil
}

// handlePendingKey handles the opening screen.
func (a *app) handlePendingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor, a.offset = 0, 0
	case "G", "end":
		a.cursor = max(len(a.visible)-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "/":
		a.input = ui.NewInput("Filter packages", "name, repo, version…", a.filter)
		a.input.Help = "Matches any column. Empty clears the filter."
		a.mode = modeFilter
	case "enter", "p":
		return a, a.openPlan(false)
	case "U":
		return a, a.openPlan(true)
	case "h":
		return a, a.confirmHold()
	case "H":
		a.mode, a.scroll, a.loading = modeHistory, 0, true
		return a, a.loadHistory()
	case "t":
		a.mode, a.timerCursor = modeTimers, 0
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	}
	return a, nil
}

// openPlan builds the plan, either to show it or to run it.
func (a *app) openPlan(apply bool) tea.Cmd {
	if len(a.model.Pending) == 0 {
		a.setStatus(ui.StatusInfo, "there is nothing to upgrade")
		return nil
	}
	a.loading = true
	a.setStatus(ui.StatusInfo, "asking "+a.model.Manager+" what it would do…")
	return a.buildPlan(apply)
}

// confirmHold previews holding the selected package at its installed version,
// or lifting a hold it already carries.
//
// The refusal comes from the backend, which read the machine before the key
// was pressed: on a dnf without the versionlock plugin this says what to
// install rather than letting `dnf versionlock add` fail in front of the user
// with a message about an unknown command.
func (a *app) confirmHold() tea.Cmd {
	if a.cursor < 0 || a.cursor >= len(a.visible) {
		a.setStatus(ui.StatusWarn, "no package selected")
		return nil
	}
	pkg := a.visible[a.cursor]
	action := updates.HoldAdd
	if pkg.Held {
		action = updates.HoldRemove
	}
	cmd, err := a.backend.BuildHold(action, pkg.Name)
	if err != nil {
		a.setStatus(ui.StatusError, firstLine(err.Error()))
		return nil
	}

	body := cmd.Description + "."
	if action == updates.HoldAdd {
		body += "\nA held package stops receiving updates, security fixes " +
			"included, until the hold is lifted."
	}
	if reason := a.model.Hold.Reason; reason != "" {
		body += "\nHow: " + reason + "."
	}
	a.previous = modePending
	a.openConfirm(cmd.Description, body, cmd)
	return nil
}

// handlePlanKey handles the plan screen.
func (a *app) handlePlanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode, a.scroll = modePending, 0
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "U", "enter":
		return a, a.confirmPlan()
	case "m":
		return a, a.cycleUpgradeMode()
	case "s":
		return a, a.toggleSnapshot()
	case "R", "ctrl+r":
		return a, a.openPlan(false)
	}
	return a.handleScrollScreen(msg, len(a.planLines()))
}

// cycleUpgradeMode walks the modes this backend really offers: the plain
// upgrade, apt's dist-upgrade where it exists, and the security-only upgrade
// where the manager can apply exactly the security fixes and nothing else.
//
// The cycle comes from the capabilities rather than from a list written here,
// so a manager that cannot narrow an upgrade to the advisories never lands on
// a mode it would have to fake.
func (a *app) cycleUpgradeMode() tea.Cmd {
	modes := updates.UpgradeModes(a.caps)
	if len(modes) < 2 {
		a.setStatusf(ui.StatusWarn,
			"%s has one kind of upgrade, so there is nothing to switch",
			a.model.Manager)
		return nil
	}
	a.upgradeMode = updates.NextUpgradeMode(a.caps, a.upgradeMode)
	a.setStatusf(ui.StatusInfo, "mode: %s", a.upgradeMode)
	return a.openPlan(false)
}

// toggleSnapshot turns the pre/post snapper pair on or off and re-plans, so
// the commands appear or disappear on the plan screen before anything is
// confirmed. A machine with nowhere to take a snapshot says so instead.
func (a *app) toggleSnapshot() tea.Cmd {
	if !a.model.Snapshot.Available {
		a.setStatusf(ui.StatusWarn, "no snapshot here: %s",
			a.model.Snapshot.Reason)
		return nil
	}
	a.snapshot = !a.snapshot
	if a.snapshot {
		a.setStatus(ui.StatusInfo, "snapshot: on")
	} else {
		a.setStatus(ui.StatusWarn, "snapshot: off")
	}
	return a.openPlan(false)
}

// confirmPlan opens the confirm dialog on the plan already on screen.
func (a *app) confirmPlan() tea.Cmd {
	if len(a.plan.Commands) == 0 {
		return a.openPlan(true)
	}
	a.previous = modePlan
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   a.plan.Title,
		Body:    a.applyBody(a.plan),
		Command: a.previewAll(a.plan.Commands),
		Danger:  true,
		Payload: pending{
			title:    a.plan.Title,
			commands: a.plan.Commands,
			sequence: true,
		},
	}
	return nil
}

// handleApplyKey handles the screen the sequence runs on.
func (a *app) handleApplyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "backspace":
		if a.busy {
			a.setStatus(ui.StatusWarn,
				"the sequence is still running; ctrl+c quits")
			return a, nil
		}
		a.mode, a.scroll = modePending, 0
		return a, nil
	case "R":
		return a, a.confirmReboot()
	}
	return a.handleScrollScreen(msg, len(a.applyLog))
}

// confirmReboot offers the reboot the upgrade asked for. It is the only place
// in the tool a reboot can start, it is never automatic, and it carries its
// own confirm dialog.
func (a *app) confirmReboot() tea.Cmd {
	if a.busy {
		a.setStatus(ui.StatusWarn, "the sequence is still running")
		return nil
	}
	cmd, err := a.backend.BuildReboot()
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	body := "The upgrade replaced code nothing running will pick up until the " +
		"machine has restarted."
	if reason := a.plan.Restart.Reason; reason != "" {
		body += "\nWhy: " + reason
	}
	body += "\nEvery session on this machine, including this one, ends here."
	a.previous = modeApply
	a.openConfirm("Reboot the machine", body, cmd)
	return nil
}

// handleTimersKey handles the unattended-update screen.
func (a *app) handleTimersKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode = modePending
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.timerCursor = min(a.timerCursor+1, max(len(a.model.Timers)-1, 0))
		return a, nil
	case "k", "up":
		a.timerCursor = max(a.timerCursor-1, 0)
		return a, nil
	case "e":
		return a, a.confirmTimer(updates.TimerEnable)
	case "d":
		return a, a.confirmTimer(updates.TimerDisable)
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	}
	return a, nil
}

// confirmTimer previews enabling or disabling the selected unit.
func (a *app) confirmTimer(action string) tea.Cmd {
	if a.timerCursor < 0 || a.timerCursor >= len(a.model.Timers) {
		a.setStatus(ui.StatusWarn, "no unit selected")
		return nil
	}
	timer := a.model.Timers[a.timerCursor]
	if !timer.Present {
		a.setStatusf(ui.StatusWarn, "%s does not exist on this machine",
			timer.Unit)
		return nil
	}
	cmd, err := a.backend.BuildTimerAction(action, timer.Unit)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	body := cmd.Description + "."
	if action == updates.TimerEnable {
		body += "\nFrom then on this machine upgrades packages without asking, " +
			"which is exactly what this tool exists to make visible."
	} else {
		body += "\nNothing will upgrade this machine on its own after that."
	}
	a.previous = modeTimers
	a.openConfirm(cmd.Description, body, cmd)
	return nil
}

// handleScrollScreen is the shared scrolling of the read-only screens.
func (a *app) handleScrollScreen(msg tea.KeyMsg, lines int) (tea.Model, tea.Cmd) {
	height := a.paneHeight()
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode, a.scroll = modePending, 0
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.scroll = min(a.scroll+1, max(lines-height, 0))
	case "k", "up":
		a.scroll = max(a.scroll-1, 0)
	case "g", "home":
		a.scroll = 0
	case "G", "end":
		a.scroll = max(lines-height, 0)
	case "pgdown", "ctrl+f":
		a.scroll = min(a.scroll+height, max(lines-height, 0))
	case "pgup", "ctrl+b":
		a.scroll = max(a.scroll-height, 0)
	}
	return a, nil
}

// openConfirm shows one command and what it does.
func (a *app) openConfirm(title, body string, cmd updates.Command) {
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    body,
		Command: a.backend.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: pending{title: title, commands: []updates.Command{cmd}},
	}
}

// previewAll renders every command of a sequence, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(commands []updates.Command) string {
	previews := make([]string, 0, len(commands))
	for _, cmd := range commands {
		previews = append(previews, a.backend.Preview(cmd))
	}
	return strings.Join(previews, "\n$ ")
}

// restartSentence says, in one line, what the upgrade would leave behind.
func restartSentence(restart updates.Restart) string {
	switch restart.Class {
	case updates.RestartReboot:
		sentence := "A reboot is needed afterwards"
		if restart.Reason != "" {
			// The reason is quoted from whatever classified it, and some of
			// them end in a full stop of their own.
			sentence += ": " + strings.TrimRight(restart.Reason, ".")
		}
		return sentence + "."
	case updates.RestartServices:
		return "These services keep the old code open until they restart: " +
			strings.Join(restart.Services, ", ") + "."
	default:
		return "Nothing running has to be restarted afterwards."
	}
}

// applyFilter recomputes the visible packages from the current filter.
func (a *app) applyFilter() {
	if a.filter == "" {
		a.visible = a.model.Pending
		a.clampCursor()
		return
	}
	needle := strings.ToLower(a.filter)
	var kept []updates.Package
	for _, p := range a.model.Pending {
		if strings.Contains(strings.ToLower(packageHaystack(p)), needle) {
			kept = append(kept, p)
		}
	}
	a.visible = kept
	a.clampCursor()
}

// packageHaystack is the text the filter matches against.
func packageHaystack(p updates.Package) string {
	return strings.Join([]string{
		p.Name, p.Arch, p.Current, p.New, p.Repo, p.Group, p.SecurityRef,
	}, " ")
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset within range.
func (a *app) clampCursor() {
	if len(a.visible) == 0 {
		a.cursor, a.offset = 0, 0
		return
	}
	a.cursor = min(max(a.cursor, 0), len(a.visible)-1)

	height := a.tableHeight()
	if a.cursor < a.offset {
		a.offset = a.cursor
	}
	if a.cursor >= a.offset+height {
		a.offset = a.cursor - height + 1
	}
	a.offset = max(min(a.offset, max(len(a.visible)-height, 0)), 0)
}

// firstLine keeps status messages to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
