package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-update/internal/updates"
)

// Layout constants: the rows the table cannot use.
const (
	headerLines = 2
	footerLines = 2
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// tableHeight is the number of package rows that fit on screen.
func (a *app) tableHeight() int {
	// header + table header + footer + status line.
	return max(a.height-headerLines-footerLines-2, minTableHeight)
}

// paneHeight is the number of text lines the scrolling screens fit.
func (a *app) paneHeight() int {
	return max(a.height-headerLines-footerLines-1, minTableHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeFilter:
		return a.input.View(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(
			ui.HelpScreen(a.theme, "tui-update — keys", helpKeys(), a.width),
			a.width, a.height)
	case modePlan:
		return a.paneView(a.detailTitle("plan"), a.planLines(), a.planHelpKeys())
	case modeApply:
		return a.paneView(a.detailTitle("apply"), a.applyLog, a.applyHelpKeys())
	case modeHistory:
		return a.paneView(a.detailTitle("history"), a.historyLines(),
			a.readOnlyHelpKeys())
	case modeTimers:
		return a.timersView()
	}
	return a.pendingView()
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// detailTitle is the subtitle extra naming the screen being shown.
func (a *app) detailTitle(name string) string { return name }

// pendingView renders the opening screen: header, package table, help bar,
// status.
func (a *app) pendingView() string {
	header := a.headerView("")

	var body string
	switch {
	case a.loading && len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "reading the pending updates…",
			a.width, a.tableHeight()+1)
	case len(a.visible) == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "no package matches "+strconv.Quote(a.filter),
			a.width, a.tableHeight()+1)
	case len(a.visible) == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read the pending updates — see the message below",
			a.width, a.tableHeight()+1)
	case len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "this machine is up to date",
			a.width, a.tableHeight()+1)
	default:
		body = a.pendingTable()
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width)
	return strings.Join([]string{header, body, help, status}, "\n")
}

// headerView renders the facts at the top of every screen.
func (a *app) headerView(subtitleExtra string) string {
	t := a.theme

	pendingValue := strconv.Itoa(len(a.model.Pending)) + " pending"
	pendingStyle := t.OK
	if len(a.model.Pending) > 0 {
		pendingStyle = t.Warn
	}
	facts := []ui.Fact{{Label: "updates", Value: pendingValue, Style: &pendingStyle}}

	if a.caps.SecurityMetadata {
		securityValue, securityStyle := strconv.Itoa(a.model.SecurityCount), t.Base
		if a.model.SecurityCount > 0 {
			securityStyle = t.Danger
		}
		facts = append(facts, ui.Fact{Label: "security",
			Value: securityValue, Style: &securityStyle})
	}

	restartValue, restartStyle := a.model.Restart.Class, t.OK
	switch a.model.Restart.Class {
	case updates.RestartReboot:
		restartValue, restartStyle = "reboot", t.Danger
	case updates.RestartServices:
		restartValue, restartStyle = "services", t.Warn
	case "":
		restartValue, restartStyle = "—", t.Muted
	}
	facts = append(facts, ui.Fact{Label: "restart",
		Value: restartValue, Style: &restartStyle})

	snapshot, snapshotStyle := "no", t.Muted
	if a.model.Snapshot.Available {
		snapshot, snapshotStyle = "yes ("+a.model.Snapshot.Config+")", t.OK
	}
	facts = append(facts, ui.Fact{Label: "snapshot",
		Value: snapshot, Style: &snapshotStyle})

	// The manager version, when it was probed: quiet on a tested version,
	// coloured on one nobody has run against.
	if a.backendCompat.Backend != "" {
		facts = append(facts, ui.CompatFact(t, a.backendCompat))
	}

	subtitle := a.backend.Describe()
	if subtitleExtra != "" {
		subtitle += "  ·  " + subtitleExtra
	}
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-update", Subtitle: subtitle, Facts: facts}.
		Render(t, a.width)
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(len(a.visible))
	if a.filter != "" {
		return count + " of " + strconv.Itoa(len(a.model.Pending)) +
			" packages  ·  ? for help"
	}
	return count + " packages  ·  enter for the plan  ·  ? for help"
}

// pendingTable renders the update list, dropping columns on narrow terminals.
func (a *app) pendingTable() string {
	columns := []ui.Column{
		{Title: "PACKAGE", Width: 20, Flex: true},
		{Title: "CURRENT → NEW", Width: 28, Flex: true},
	}
	showRepo := a.width >= 72
	showSize := a.width >= 92 && a.caps.PerPackageSize
	showSecurity := a.width >= 84
	if showRepo {
		columns = append(columns, ui.Column{Title: "REPO", Width: 14})
	}
	if showSize {
		columns = append(columns, ui.Column{Title: "SIZE", Width: 10})
	}
	if showSecurity {
		columns = append(columns, ui.Column{Title: "SEC", Width: 4})
	}

	rows := make([][]string, 0, len(a.visible))
	styles := make([]*lipgloss.Style, 0, len(a.visible))
	for _, p := range a.visible {
		row := []string{packageCell(p), p.Transition()}
		if showRepo {
			row = append(row, p.Repo)
		}
		if showSize {
			row = append(row, orDash(p.Size))
		}
		if showSecurity {
			row = append(row, securityCell(p, a.caps))
		}
		rows = append(rows, row)
		styles = append(styles, a.packageStyle(p))
	}

	return ui.Table{
		Columns:  columns,
		Rows:     rows,
		Styles:   styles,
		Selected: a.cursor,
		Offset:   a.offset,
		Height:   a.tableHeight(),
	}.Render(a.theme, a.width)
}

// packageCell renders the name column, marking the group that put the package
// at the top of the list.
func packageCell(p updates.Package) string {
	label := p.Label()
	switch p.Group {
	case updates.GroupKernel:
		label += " ·k"
	case updates.GroupFirmware:
		label += " ·f"
	case updates.GroupCore:
		label += " ·c"
	}
	// Two different facts, so two different marks: ·held is a decision
	// somebody made here with `apt-mark hold` or `dnf versionlock`, and
	// ·ignored is the manager declining to upgrade the package on its own.
	if p.Held {
		label += " ·held"
	}
	if p.Ignored {
		label += " ·ignored"
	}
	return label
}

// securityCell renders the security column. A manager that publishes no
// security metadata gets "n/a" rather than "no": the difference between "this
// is not a security fix" and "nobody here can tell you" is the whole point of
// the column.
func securityCell(p updates.Package, caps updates.Capabilities) string {
	if !caps.SecurityMetadata {
		return "n/a"
	}
	if p.Security {
		return "yes"
	}
	return "—"
}

// orDash renders an empty value as a visible placeholder.
func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// packageStyle colors a row by what it will cost to apply: a kernel or a
// security fix should not look like a vim update.
func (a *app) packageStyle(p updates.Package) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case p.Ignored || p.Held:
		// A package nothing is going to upgrade is not worth the colour of one
		// that is, whichever of the two reasons is holding it back.
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	case p.Security:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case p.Group == updates.GroupKernel || p.Group == updates.GroupFirmware:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case p.Group == updates.GroupCore:
		style = a.theme.Row.Foreground(a.theme.Info.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// paneView renders one of the scrolling text screens.
func (a *app) paneView(title string, lines []string, hints []ui.KeyHint) string {
	header := a.headerView(title)

	height := a.paneHeight()
	offset := min(a.scroll, max(len(lines)-height, 0))
	a.scroll = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, hints, a.width)
	position := strconv.Itoa(offset+1) + "–" + strconv.Itoa(end) +
		" of " + strconv.Itoa(len(lines)) + " lines  ·  esc to go back"
	status := ui.StatusLine(a.theme, a.statusKind, a.status, position, a.width)
	return strings.Join([]string{header,
		strings.Join(body, "\n"), help, status}, "\n")
}

// planLines is the plan screen: what would run, in what order, and what it
// would leave behind.
func (a *app) planLines() []string {
	if len(a.plan.Commands) == 0 {
		return []string{"", "  (no plan yet — press p on the package list)"}
	}
	plan := a.plan
	lines := []string{plan.Title, ""}

	lines = append(lines, "Restart classification")
	lines = append(lines, "  class          "+plan.Restart.Class)
	if len(plan.Restart.Services) > 0 {
		lines = append(lines, "  services       "+
			strings.Join(plan.Restart.Services, " "))
	}
	if plan.Restart.Reason != "" {
		lines = append(lines, "  reboot because "+plan.Restart.Reason)
	}
	lines = append(lines, "  decided by     "+orDash(plan.Restart.Source))

	lines = append(lines, "", "Upgrade mode")
	lines = append(lines, "  "+upgradeModeLine(plan.Mode, a.caps))

	lines = append(lines, "", "Snapshot before")
	switch {
	case plan.TakeSnapshot:
		lines = append(lines,
			"  snapshot before: yes  (s turns it off)",
			"  "+plan.Snapshot.Pre.String(),
			"  "+plan.Snapshot.Post.String())
	case plan.Snapshot.Available:
		lines = append(lines, "  snapshot before: no  (turned off — s turns "+
			"it back on)")
	default:
		lines = append(lines, "  snapshot before: no")
	}
	lines = append(lines, "  "+plan.Snapshot.Reason)

	lines = append(lines, "", "The commands, in order")
	for i, cmd := range plan.Commands {
		lines = append(lines, "  "+strconv.Itoa(i+1)+". "+a.backend.Preview(cmd))
		lines = append(lines, "     "+cmd.Description)
	}
	lines = append(lines, "  "+strconv.Itoa(len(plan.Commands)+1)+
		". (nothing) — tui-update never reboots by itself")

	if len(plan.Notes) > 0 {
		lines = append(lines, "", "Notes")
		for _, note := range plan.Notes {
			lines = append(lines, "  · "+note)
		}
	}

	lines = append(lines, "", a.model.Manager+" dry run")
	if strings.TrimSpace(plan.DryRun) == "" {
		lines = append(lines, "  (nothing)")
	}
	for _, line := range strings.Split(strings.TrimRight(plan.DryRun, "\n"), "\n") {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, "  "+line)
	}
	return lines
}

// upgradeModeLine names the mode this plan was built for, and what else m can
// reach on this manager.
func upgradeModeLine(mode string, caps updates.Capabilities) string {
	line := orDash(mode)
	modes := updates.UpgradeModes(caps)
	if len(modes) < 2 {
		return line + "  (this manager has only one kind of upgrade)"
	}
	return line + "  (m cycles: " + strings.Join(modes, " → ") + ")"
}

// historyLines renders the manager's transaction log.
func (a *app) historyLines() []string {
	lines := []string{
		"The last " + strconv.Itoa(len(a.history)) + " transactions, newest first",
		"", historySource(a.model.Manager), "",
	}
	if len(a.history) == 0 {
		lines = append(lines, "  (nothing was read: the log is empty or "+
			"unreadable by this user)")
		return lines
	}
	for _, transaction := range a.history {
		lines = append(lines,
			"  "+ui.Pad(transaction.ID, 6)+" "+transaction.When)
		lines = append(lines, "         "+orDash(transaction.Command))
		if transaction.Detail != "" {
			lines = append(lines, "         "+transaction.Detail)
		}
		lines = append(lines, "")
	}
	return lines
}

// historySource names where the history came from, so the screen is not a
// list of facts with no provenance.
func historySource(manager string) string {
	switch manager {
	case updates.ManagerPacman:
		return "read from /var/log/pacman.log"
	case updates.ManagerAPT:
		return "read from /var/log/apt/history.log"
	case updates.ManagerDNF:
		return "read from `dnf history list`"
	default:
		return ""
	}
}

// timersView renders the unattended-update screen.
func (a *app) timersView() string {
	header := a.headerView("timers")

	var body string
	if len(a.model.Timers) == 0 {
		body = ui.EmptyState(a.theme,
			"this machine has no unattended-update unit to manage",
			a.width, a.tableHeight()+1)
	} else {
		columns := []ui.Column{
			{Title: "UNIT", Width: 30, Flex: true},
			{Title: "STATE", Width: 12},
			{Title: "RUNNING", Width: 9},
			{Title: "WHAT IT IS", Width: 30, Flex: true},
		}
		rows := make([][]string, 0, len(a.model.Timers))
		styles := make([]*lipgloss.Style, 0, len(a.model.Timers))
		for _, timer := range a.model.Timers {
			active := "no"
			if timer.Active {
				active = "yes"
			}
			rows = append(rows, []string{
				timer.Unit, orDash(timer.State), active, timer.Description,
			})
			style := a.theme.Row
			switch {
			case !timer.Present:
				style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
			case timer.Enabled:
				style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
			}
			styles = append(styles, &style)
		}
		body = ui.Table{
			Columns:  columns,
			Rows:     rows,
			Styles:   styles,
			Selected: a.timerCursor,
			Height:   a.tableHeight(),
		}.Render(a.theme, a.width)
	}

	help := ui.HelpBar(a.theme, a.timerHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status,
		"e enables, d disables — both previewed first", a.width)
	return strings.Join([]string{header, body, help, status}, "\n")
}

// shortHelpKeys is the single-line hint bar of the package list.
func (a *app) shortHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "enter", Desc: "plan"},
		{Key: "U", Desc: "upgrade"},
		{Key: "h", Desc: "history"},
		{Key: "H", Desc: "hold"},
		{Key: "t", Desc: "timers"},
		{Key: "/", Desc: "filter"},
		{Key: "R", Desc: "re-read"},
		{Key: "?", Desc: "help"},
		{Key: "q", Desc: "quit"},
	}
}

// planHelpKeys is the hint bar of the plan screen.
func (a *app) planHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "U", Desc: "apply"}}
	if len(updates.UpgradeModes(a.caps)) > 1 {
		hints = append(hints, ui.KeyHint{Key: "m", Desc: "mode"})
	}
	if a.model.Snapshot.Available {
		hints = append(hints, ui.KeyHint{Key: "s", Desc: "snapshot"})
	}
	return append(hints,
		ui.KeyHint{Key: "j/k", Desc: "scroll"},
		ui.KeyHint{Key: "R", Desc: "re-plan"},
		ui.KeyHint{Key: "esc", Desc: "back"})
}

// applyHelpKeys is the hint bar of the apply screen. The reboot is offered
// only once the sequence has finished and only when it asked for one.
func (a *app) applyHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "j/k", Desc: "scroll"}}
	if a.applyDone && a.plan.Restart.Class == updates.RestartReboot {
		hints = append(hints, ui.KeyHint{Key: "R", Desc: "reboot"})
	}
	return append(hints, ui.KeyHint{Key: "esc", Desc: "back"})
}

// readOnlyHelpKeys is the hint bar of the history screen.
func (a *app) readOnlyHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "j/k", Desc: "scroll"},
		{Key: "g/G", Desc: "top/bottom"},
		{Key: "esc", Desc: "back"},
	}
}

// timerHelpKeys is the hint bar of the timers screen.
func (a *app) timerHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "e", Desc: "enable"},
		{Key: "d", Desc: "disable"},
		{Key: "j/k", Desc: "move"},
		{Key: "R", Desc: "re-read"},
		{Key: "esc", Desc: "back"},
	}
}

// helpKeys is the full key list shown on the help screen.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "↑/k, ↓/j", Desc: "move the selection, or scroll a text screen"},
		{Key: "g / G", Desc: "first / last"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "enter / p", Desc: "plan: what applying the updates would do"},
		{Key: "U", Desc: "apply the plan, after confirming the whole sequence"},
		{Key: "m", Desc: "on the plan: cycle upgrade, dist-upgrade (apt) and " +
			"security-only (dnf)"},
		{Key: "s", Desc: "on the plan: take the pre/post snapshot, or do not"},
		{Key: "h", Desc: "the package manager's own transaction history"},
		{Key: "H", Desc: "hold the selected package at its version, or lift " +
			"the hold"},
		{Key: "t", Desc: "the unattended-update timers, and enable/disable them"},
		{Key: "e / d", Desc: "on the timers screen: enable / disable the unit"},
		{Key: "R", Desc: "re-read; on a finished upgrade, offer the reboot"},
		{Key: "/", Desc: "filter the packages (esc clears)"},
		{Key: "esc", Desc: "leave the screen"},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "every change is previewed and confirmed first"},
		{Key: "note", Desc: "tui-update never reboots by itself"},
		{Key: "note", Desc: "reading never refreshes the manager's metadata"},
	}
}
