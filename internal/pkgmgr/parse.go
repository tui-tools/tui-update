package pkgmgr

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-update/internal/updates"
)

// splitLines splits command output into lines, dropping the empty element a
// trailing newline produces.
func splitLines(text string) []string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// ---------------------------------------------------------------- pacman ---

// pacmanPendingRe matches the one line shape `checkupdates` and `pacman -Qu`
// both print:
//
//	glibc 2.42-1 -> 2.42-2
//	tui-update 0.2.0-1 -> 0.3.0-1 [ignored]
//
// The two commands are interchangeable here on purpose: checkupdates is a
// shell script around a private sync database, and what it prints is what
// `pacman -Qu` prints against it.
var pacmanPendingRe = regexp.MustCompile(
	`^(\S+)\s+(\S+)\s+->\s+(\S+)(\s+\[ignored\])?\s*$`)

// ParsePacmanPending reads the output of `checkupdates` or `pacman -Qu`.
func ParsePacmanPending(out string) []updates.Package {
	var packages []updates.Package
	for _, line := range splitLines(out) {
		match := pacmanPendingRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		packages = append(packages, updates.Package{
			Name:    match[1],
			Current: match[2],
			New:     match[3],
			Ignored: match[4] != "",
			Group:   GroupFor(match[1]),
		})
	}
	updates.SortPackages(packages)
	return packages
}

// pacmanLogTransactionRe matches the line pacman writes when it starts, which
// is the only line of the log carrying the command that was run:
//
//	[2026-08-24T06:12:03-0300] [PACMAN] Running 'pacman -Syu --noconfirm'
var pacmanLogTransactionRe = regexp.MustCompile(
	`^\[([^\]]+)\]\s+\[PACMAN\]\s+Running\s+'(.*)'\s*$`)

// pacmanLogActionRe matches what the transaction did to one package:
//
//	[2026-08-24T06:12:31-0300] [ALPM] upgraded openssl (3.5.1-1 -> 3.5.2-1)
var pacmanLogActionRe = regexp.MustCompile(
	`^\[([^\]]+)\]\s+\[ALPM\]\s+(upgraded|installed|removed|reinstalled|downgraded)\s+(\S+)`)

// ParsePacmanLog turns /var/log/pacman.log into transactions, newest first.
//
// pacman keeps no transaction database, only this log, so a transaction is
// reconstructed: a `Running '…'` line opens one and the `[ALPM]` lines under
// it are counted into it. Actions before the first Running line — a pacman
// run from a script that did not log its command — are gathered into an
// unnamed transaction rather than dropped.
func ParsePacmanLog(text string, limit int) []updates.Transaction {
	var transactions []updates.Transaction
	current := -1
	for _, line := range splitLines(text) {
		if match := pacmanLogTransactionRe.FindStringSubmatch(line); match != nil {
			transactions = append(transactions, updates.Transaction{
				ID:      match[1],
				When:    match[1],
				Command: match[2],
			})
			current = len(transactions) - 1
			continue
		}
		match := pacmanLogActionRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if current < 0 {
			transactions = append(transactions, updates.Transaction{
				ID:      match[1],
				When:    match[1],
				Command: "(not logged)",
			})
			current = len(transactions) - 1
		}
		transactions[current].Detail = bump(transactions[current].Detail, match[2])
	}

	// Newest first, and only as many as the screen asked for.
	transactions = withPackageChanges(transactions)
	reverse(transactions)
	if limit > 0 && len(transactions) > limit {
		transactions = transactions[:limit]
	}
	return transactions
}

// withPackageChanges drops the logged pacman runs that changed no package.
//
// Every `pacman -Sy` writes a `Running '…'` line and nothing else, and on
// Omarchy Server a single update runs pacman three times — a bare `-Sy`, a
// keyring refresh, then the upgrade — so a history built from the log alone
// is mostly rows with an empty detail column. apt and dnf list transactions
// rather than invocations, and this is what makes pacman's screen agree with
// theirs.
func withPackageChanges(transactions []updates.Transaction) []updates.Transaction {
	kept := transactions[:0]
	for _, transaction := range transactions {
		if transaction.Detail == "" {
			continue
		}
		kept = append(kept, transaction)
	}
	return kept
}

// bump adds one to the count of an action in a "2 upgraded, 1 installed"
// summary, keeping the actions in the order they were first seen.
func bump(summary, action string) string {
	parts := strings.Split(summary, ", ")
	for i, part := range parts {
		fields := strings.SplitN(part, " ", 2)
		if len(fields) != 2 || fields[1] != action {
			continue
		}
		count, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		parts[i] = strconv.Itoa(count+1) + " " + action
		return strings.Join(parts, ", ")
	}
	if summary == "" {
		return "1 " + action
	}
	return summary + ", 1 " + action
}

// reverse flips a transaction list in place, newest first.
func reverse(transactions []updates.Transaction) {
	for i, j := 0, len(transactions)-1; i < j; i, j = i+1, j-1 {
		transactions[i], transactions[j] = transactions[j], transactions[i]
	}
}

// omarchyReportRe matches the four key/value lines
// `omarchy-server-update-restart` ends with, which are the only
// machine-readable output any of the Omarchy Server wrappers produce.
var omarchyReportRe = regexp.MustCompile(
	`^(restarted|restart failed|deferred|reboot required):\s*(.*)$`)

// ParseOmarchyRestart reads that report.
//
//	restarted: sshd nginx
//	deferred: dbus.service (deny-list)
//	reboot required: glibc 2.42-1 -> 2.42-2
//
// `restarted` and `restart failed` are space-separated short unit names or
// the literal "none". `deferred` is free text — the units keep their .service
// suffix and carry a parenthesised reason — so it is kept whole rather than
// split. `reboot required` is "no" or a "; "-joined list of reasons.
//
// The probe is run with --dry-run, so "restarted" is what the wrapper *would*
// restart: that is precisely the list this screen wants.
func ParseOmarchyRestart(out string) updates.Restart {
	restart := updates.Restart{Source: OmarchyRestart}
	for _, line := range splitLines(out) {
		match := omarchyReportRe.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		value := strings.TrimSpace(match[2])
		switch match[1] {
		case "restarted", "restart failed":
			if value == "none" || value == "" {
				continue
			}
			restart.Services = append(restart.Services,
				strings.Fields(value)...)
		case "deferred":
			if value == "none" || value == "" {
				continue
			}
			restart.Detail = "deferred: " + value
		case "reboot required":
			if value == "no" || value == "" {
				continue
			}
			restart.RebootRequired = true
			restart.Reason = value
		}
	}
	switch {
	case restart.RebootRequired:
		restart.Class = updates.RestartReboot
	case len(restart.Services) > 0:
		restart.Class = updates.RestartServices
	default:
		restart.Class = updates.RestartNone
	}
	return restart
}

// ------------------------------------------------------------------- apt ---

// aptUpgradableRe matches a line of `apt list --upgradable`:
//
//	libc6/noble-updates,noble-security 2.39-0ubuntu8.6 amd64 [upgradable from: 2.39-0ubuntu8.4]
//
// The pocket list after the slash is what carries the security flag, which is
// why it is captured whole rather than only its first element.
var aptUpgradableRe = regexp.MustCompile(
	`^(\S+)/(\S+)\s+(\S+)\s+(\S+)\s+\[upgradable from:\s*([^\]]+)\]\s*$`)

// ParseAPTUpgradable reads `apt list --upgradable`.
//
// apt exposes security updates as a pocket rather than as metadata: an update
// that came from `<release>-security` is a security update, and there is no
// advisory id to show alongside it. The pocket name is kept as the reference
// so the screen can say where the claim came from.
func ParseAPTUpgradable(out string) []updates.Package {
	var packages []updates.Package
	for _, line := range splitLines(out) {
		match := aptUpgradableRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		pockets := match[2]
		security, ref := aptSecurityPocket(pockets)
		packages = append(packages, updates.Package{
			Name:        match[1],
			Repo:        pockets,
			New:         match[3],
			Arch:        match[4],
			Current:     strings.TrimSpace(match[5]),
			Security:    security,
			SecurityRef: ref,
			Group:       GroupFor(match[1]),
		})
	}
	updates.SortPackages(packages)
	return packages
}

// aptSecurityPocket reports whether any pocket of an upgradable line is a
// security pocket, and names it.
func aptSecurityPocket(pockets string) (bool, string) {
	for _, pocket := range strings.Split(pockets, ",") {
		pocket = strings.TrimSpace(pocket)
		if strings.HasSuffix(pocket, "-security") ||
			strings.Contains(pocket, "-security/") {
			return true, pocket
		}
	}
	return false, ""
}

// aptInstLineRe matches the machine-readable half of `apt-get -s upgrade`:
//
//	Inst libc6 [2.39-0ubuntu8.4] (2.39-0ubuntu8.6 Ubuntu:24.04/noble-updates [amd64])
//
// It is the same information as `apt list --upgradable` in a different shape,
// and it is what the plan screen quotes: apt's own account of the transaction
// it would perform.
var aptInstLineRe = regexp.MustCompile(
	`^Inst\s+(\S+)\s+\[([^\]]*)\]\s+\(([^\s]+)\s+(.*)\s+\[([^\]]+)\]\)\s*$`)

// aptSummaryRe matches the counts apt prints at the end of a simulation.
var aptSummaryRe = regexp.MustCompile(
	`^(\d+) upgraded, (\d+) newly installed, (\d+) to remove and (\d+) not upgraded\.$`)

// APTPlan is what a simulated apt run said it would do.
type APTPlan struct {
	// Packages are the Inst lines, parsed.
	Packages []updates.Package
	// Upgraded, Installed, Removed and Held are apt's own counts.
	Upgraded, Installed, Removed, Held int
	// Download and Disk are the "Need to get" and "After this operation"
	// lines, kept as apt rendered them.
	Download string
	Disk     string
}

// aptDownloadRe and aptDiskRe match the two size lines apt prints.
var (
	aptDownloadRe = regexp.MustCompile(`^Need to get ([0-9.,]+ ?[kMG]?B)`)
	aptDiskRe     = regexp.MustCompile(
		`^After this operation, ([0-9.,]+ ?[kMG]?B) of (additional )?disk space`)
)

// ParseAPTSimulation reads `apt-get -s upgrade`.
func ParseAPTSimulation(out string) APTPlan {
	var plan APTPlan
	for _, line := range splitLines(out) {
		line = strings.TrimSpace(line)
		if match := aptInstLineRe.FindStringSubmatch(line); match != nil {
			pockets := aptPockets(match[4])
			security, ref := aptSecurityPocket(pockets)
			plan.Packages = append(plan.Packages, updates.Package{
				Name:        match[1],
				Current:     match[2],
				New:         match[3],
				Repo:        pockets,
				Arch:        match[5],
				Security:    security,
				SecurityRef: ref,
				Group:       GroupFor(match[1]),
			})
			continue
		}
		if match := aptSummaryRe.FindStringSubmatch(line); match != nil {
			plan.Upgraded, _ = strconv.Atoi(match[1])
			plan.Installed, _ = strconv.Atoi(match[2])
			plan.Removed, _ = strconv.Atoi(match[3])
			plan.Held, _ = strconv.Atoi(match[4])
			continue
		}
		if match := aptDownloadRe.FindStringSubmatch(line); match != nil {
			plan.Download = match[1]
			continue
		}
		if match := aptDiskRe.FindStringSubmatch(line); match != nil {
			plan.Disk = match[1]
		}
	}
	updates.SortPackages(plan.Packages)
	return plan
}

// aptPockets turns the origin field of an Inst line — "Ubuntu:24.04/noble-updates,
// Ubuntu:24.04/noble-security" — into the pocket list `apt list --upgradable`
// would have printed.
func aptPockets(origins string) string {
	var pockets []string
	for _, origin := range strings.Split(origins, ",") {
		origin = strings.TrimSpace(origin)
		if i := strings.LastIndex(origin, "/"); i >= 0 {
			origin = origin[i+1:]
		}
		if origin != "" {
			pockets = append(pockets, origin)
		}
	}
	return strings.Join(pockets, ",")
}

// needrestartSvcRe matches the one line of `needrestart -b` this tool needs.
// The batch format is documented as stable and is what every wrapper around
// needrestart parses.
var needrestartSvcRe = regexp.MustCompile(`^NEEDRESTART-SVC:\s*(\S+)\s*$`)

// needrestartKstaRe matches the kernel status: 0 unknown, 1 up to date,
// 2 ABI-compatible upgrade pending, 3 a version upgrade pending.
var needrestartKstaRe = regexp.MustCompile(`^NEEDRESTART-KSTA:\s*(\d+)\s*$`)

// ParseNeedrestart reads `needrestart -b`.
func ParseNeedrestart(out string) updates.Restart {
	restart := updates.Restart{Source: "needrestart -b"}
	var expected string
	for _, line := range splitLines(out) {
		line = strings.TrimSpace(line)
		if match := needrestartSvcRe.FindStringSubmatch(line); match != nil {
			// A line that is nothing but the suffix would leave a blank
			// unit name in the sentence the plan screen prints and in the
			// restart command built from it.
			if unit := strings.TrimSuffix(match[1], ".service"); unit != "" {
				restart.Services = append(restart.Services, unit)
			}
			continue
		}
		if match := needrestartKstaRe.FindStringSubmatch(line); match != nil {
			// 2 and 3 both mean the running kernel is not the installed one.
			if match[1] == "2" || match[1] == "3" {
				restart.RebootRequired = true
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "NEEDRESTART-KEXP:"); ok {
			expected = strings.TrimSpace(value)
		}
	}
	if restart.RebootRequired && expected != "" {
		restart.Reason = "the installed kernel is " + expected +
			", which is not the one running"
	}
	switch {
	case restart.RebootRequired:
		restart.Class = updates.RestartReboot
	case len(restart.Services) > 0:
		restart.Class = updates.RestartServices
	default:
		restart.Class = updates.RestartNone
	}
	return restart
}

// aptHistoryStartRe and the fields around it match /var/log/apt/history.log,
// which is a series of RFC822-ish blocks separated by blank lines.
var aptHistoryFieldRe = regexp.MustCompile(`^([A-Za-z-]+):\s*(.*)$`)

// ParseAPTHistory reads /var/log/apt/history.log, newest first.
func ParseAPTHistory(text string, limit int) []updates.Transaction {
	var transactions []updates.Transaction
	var current *updates.Transaction
	for _, line := range splitLines(text) {
		match := aptHistoryFieldRe.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		key, value := match[1], strings.TrimSpace(match[2])
		switch key {
		case "Start-Date":
			// The date is the block's identity and the history screen's
			// first column. A blank one opens no transaction, and the
			// fields that follow it belong to nothing rather than to the
			// previous block.
			if value == "" {
				current = nil
				continue
			}
			transactions = append(transactions, updates.Transaction{
				ID: value, When: value,
			})
			current = &transactions[len(transactions)-1]
		case "Commandline":
			if current != nil {
				current.Command = value
			}
		case "Upgrade", "Install", "Remove", "Purge", "Downgrade":
			if current == nil {
				continue
			}
			count := len(strings.Split(value, "), "))
			current.Detail = appendDetail(current.Detail,
				strconv.Itoa(count)+" "+strings.ToLower(key))
		}
	}
	reverse(transactions)
	if limit > 0 && len(transactions) > limit {
		transactions = transactions[:limit]
	}
	return transactions
}

// appendDetail joins the pieces of a transaction summary.
func appendDetail(summary, piece string) string {
	if summary == "" {
		return piece
	}
	return summary + ", " + piece
}

// ParseRebootRequiredPkgs reads /var/run/reboot-required.pkgs, one package
// name per line, which is Debian's way of saying which package asked.
func ParseRebootRequiredPkgs(text string) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range splitLines(text) {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// ------------------------------------------------------------------- dnf ---

// dnfCheckUpdateRe matches a line of `dnf check-update`:
//
//	vim-enhanced.x86_64      2:9.1.1450-1.fc42     updates
//
// The columns are space-padded, and a very long name pushes the version onto
// the next line — a shape this deliberately skips rather than guesses at,
// because a half-read version in a confirm dialog is worse than a name that
// did not make the list.
var dnfCheckUpdateRe = regexp.MustCompile(`^(\S+)\.(\S+)\s+(\S+)\s+(\S+)\s*$`)

// dnfObsoletingRe marks the section `check-update` prints after the updates,
// which lists obsoleting packages in the same column shape. They are not
// updates, so parsing stops there.
var dnfObsoletingRe = regexp.MustCompile(`^Obsoleting\s+Packages?\s*$`)

// ParseDNFCheckUpdate reads `dnf check-update`. Both dnf4 and dnf5 print this
// same three-column table under `-q`.
func ParseDNFCheckUpdate(out string) []updates.Package {
	var packages []updates.Package
	for _, line := range splitLines(out) {
		if dnfObsoletingRe.MatchString(strings.TrimSpace(line)) {
			break
		}
		match := dnfCheckUpdateRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		// A dnf5 error line ("Error: Cache-only enabled but no cache for
		// 'x'") is three space-separated fields too; a package name never
		// ends in a colon.
		if strings.HasSuffix(match[1], ":") {
			continue
		}
		packages = append(packages, updates.Package{
			Name:  match[1],
			Arch:  match[2],
			New:   match[3],
			Repo:  match[4],
			Group: GroupFor(match[1]),
		})
	}
	updates.SortPackages(packages)
	return packages
}

// ParseDNFSizes reads `dnf repoquery --upgrades`, returning the download size
// in bytes keyed by "name.arch".
func ParseDNFSizes(out string) map[string]int64 {
	sizes := map[string]int64{}
	for _, line := range splitLines(out) {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 3 {
			continue
		}
		// The first field is the "name.arch" the caller looks a package up
		// by. A blank one is an entry no lookup could ever reach, and an
		// rpm name never carries whitespace, so a field that does is a line
		// this table did not produce.
		if fields[0] == "" || strings.ContainsAny(fields[0], " \t") {
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			continue
		}
		sizes[fields[0]] = size
	}
	return sizes
}

// dnfAdvisoryRe matches a line of `dnf updateinfo list --security`:
//
//	FEDORA-2026-9a1f2b3c4d security Important openssl-3.2.4-3.fc42.x86_64 2026-08-21 03:11:02
//
// The package column is a full NEVRA, so the name and the architecture are
// recovered from it rather than from a column of their own.
var dnfAdvisoryRe = regexp.MustCompile(
	`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s`)

// Advisory is one security advisory and the package it applies to.
type Advisory struct {
	// ID is the advisory identifier ("FEDORA-2026-9a1f2b3c4d").
	ID string
	// Severity is dnf's word for it ("Important").
	Severity string
	// Package is the "name.arch" the advisory covers.
	Package string
}

// ParseDNFSecurity reads `dnf updateinfo list --security`, keyed by
// "name.arch".
func ParseDNFSecurity(out string) map[string]Advisory {
	advisories := map[string]Advisory{}
	for _, line := range splitLines(out) {
		match := dnfAdvisoryRe.FindStringSubmatch(line)
		if match == nil || strings.EqualFold(match[1], "Name") {
			continue
		}
		key, ok := nevraNameArch(match[4])
		if !ok {
			continue
		}
		advisories[key] = Advisory{
			ID:       match[1],
			Severity: match[3],
			Package:  key,
		}
	}
	return advisories
}

// nevraNameArch turns "openssl-3.2.4-3.fc42.x86_64" into "openssl.x86_64".
//
// A NEVRA is name-version-release.arch with no delimiter that cannot also
// appear in a name, so it is peeled from the right: the architecture is the
// last dot-separated field, and the version and release are the last two
// hyphenated ones.
func nevraNameArch(nevra string) (string, bool) {
	dot := strings.LastIndex(nevra, ".")
	if dot < 0 {
		return "", false
	}
	arch := nevra[dot+1:]
	rest := nevra[:dot]
	release := strings.LastIndex(rest, "-")
	if release < 0 {
		return "", false
	}
	version := strings.LastIndex(rest[:release], "-")
	if version < 0 {
		return "", false
	}
	return rest[:version] + "." + arch, true
}

// dnfHistoryRe matches a line of `dnf history list`:
//
//	143 dnf install tui-update               2026-06-19 16:49:43                 1
//
// dnf4 and dnf5 pad the columns differently, so the line is read as
// "id, then the date, then what is left", rather than by column position.
var dnfHistoryRe = regexp.MustCompile(
	`^\s*(\d+)\s+(.*?)\s+(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s+(.*)$`)

// ParseDNFHistory reads `dnf history list`, which already prints newest
// first.
func ParseDNFHistory(out string, limit int) []updates.Transaction {
	var transactions []updates.Transaction
	for _, line := range splitLines(out) {
		match := dnfHistoryRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		transactions = append(transactions, updates.Transaction{
			ID:      match[1],
			Command: strings.TrimSpace(match[2]),
			When:    match[3],
			Detail:  strings.Join(strings.Fields(match[4]), " ") + " altered",
		})
		if limit > 0 && len(transactions) == limit {
			break
		}
	}
	return transactions
}

// ParseNeedsRestartingServices reads `needs-restarting -s`, one unit per
// line. The command writes its "Failed to read PID N's smaps." complaints to
// stderr, which the runner keeps out of this, but a stray non-unit line is
// skipped anyway.
func ParseNeedsRestartingServices(out string) []string {
	var services []string
	for _, line := range splitLines(out) {
		unit := strings.TrimSpace(line)
		if unit == "" || !strings.HasSuffix(unit, ".service") {
			continue
		}
		// A unit name has no whitespace in it, and ".service" on its own
		// would leave a blank one behind: either way the line is not a unit.
		name := strings.TrimSuffix(unit, ".service")
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		services = append(services, name)
	}
	return services
}

// ParseSnapperConfigs reads `snapper list-configs`, returning the
// configuration names.
func ParseSnapperConfigs(out string) []string {
	var configs []string
	for _, line := range splitLines(out) {
		name, _, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "Config" || strings.HasPrefix(name, "---") {
			continue
		}
		configs = append(configs, name)
	}
	return configs
}

// ---------------------------------------------------------------- holds ---

// ParseAPTHolds reads `apt-mark showhold`, which prints one package name per
// line and nothing else.
// A line that is not a package name is dropped rather than kept: apt-mark
// prints prose when nothing is held, and a name from here goes straight into
// `apt-mark hold <name>`, so the same rule that guards every other argv guards
// this one.
func ParseAPTHolds(out string) map[string]bool {
	holds := map[string]bool{}
	for _, line := range splitLines(out) {
		name := strings.TrimSpace(line)
		if checkPackageName(name) != nil {
			continue
		}
		holds[name] = true
	}
	return holds
}

// versionlockEntryRe splits a versionlock entry into the package name and the
// version it is pinned to.
//
// The entries are the specs the plugin writes into its own configuration, and
// what they look like depends on how the lock was made:
//
//	glibc-0:2.41-4.fc42.*     added from an installed package
//	kernel-6.14.9-300.fc42.*  the same without an epoch
//	nginx                     a whole-package exclusion
//
// The name is everything up to the last hyphen that starts a version, which is
// the one rule that holds across all three: a package name may contain
// hyphens, and a version always begins with a digit or with `<epoch>:`.
var versionlockEntryRe = regexp.MustCompile(`^(.+?)-(?:[0-9]+:)?[0-9][^-]*(?:-.*)?$`)

// ParseDNFVersionlock reads `dnf versionlock list`, returning the package
// names that are pinned.
//
// The versions are dropped on purpose: what the pending screen marks is that a
// package is held, and which version it is held at is the manager's business.
func ParseDNFVersionlock(out string) map[string]bool {
	holds := map[string]bool{}
	for _, line := range splitLines(out) {
		entry := strings.TrimSpace(line)
		// The plugin comments its own file, and dnf prefixes a warning or two
		// on a machine with an unreachable repository.
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		entry = strings.TrimSuffix(entry, ".*")
		name := entry
		if match := versionlockEntryRe.FindStringSubmatch(entry); match != nil {
			name = match[1]
		}
		name = withoutArch(name)
		// The name goes into `dnf versionlock delete <name>`, so anything that
		// is not a package name is dropped rather than carried into an argv.
		if checkPackageName(name) == nil {
			holds[name] = true
		}
	}
	return holds
}

// rpmArches are the architecture suffixes a versionlock entry can carry on a
// whole-package exclusion, which names no version to strip them off with.
var rpmArches = []string{
	".x86_64", ".aarch64", ".noarch", ".i686", ".armv7hl", ".ppc64le", ".s390x",
	".src",
}

// withoutArch drops the architecture suffix of a package name, so a hold
// matches the pending list, which keeps the name and the architecture apart.
func withoutArch(name string) string {
	for _, arch := range rpmArches {
		if strings.HasSuffix(name, arch) {
			return strings.TrimSuffix(name, arch)
		}
	}
	return name
}

// VersionlockMissing recognises dnf refusing a subcommand it has no plugin
// for. dnf4 answers "No such command: versionlock" and dnf5 complains about an
// unknown argument; neither of them says what to install, which is the whole
// reason this is detected rather than left to fail in front of the user.
func VersionlockMissing(out string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(out + " " + err.Error())
	return strings.Contains(text, "no such command") ||
		strings.Contains(text, "unknown argument") ||
		strings.Contains(text, "invalid choice") ||
		strings.Contains(text, "unknown command")
}

// humanSize renders a byte count the way a package manager would.
func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + units[i]
}
