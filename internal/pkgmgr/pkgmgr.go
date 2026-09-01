// Package pkgmgr is the package-manager backend of tui-update, and the only
// place in the repository that starts a process.
//
// Everything about reaching the machine — resolving the binaries, applying the
// privilege prefix, bounding each call, turning a failure into one readable
// line — belongs to the kit runner. What is left here is the translation
// between three package managers' output and the manager-neutral model in
// internal/updates, and the assembly of the argv that a confirm dialog will
// show before it runs.
//
// One manager is active on a machine. Which one is decided by the binary that
// is installed, cross-checked against /etc/os-release, so a Debian container
// on an Arch host does not end up driving the host's pacman. That decision is
// the same one every tool in the family makes, so it is the kit's pkgmgr that
// makes it here too; what this package adds is what only an update tool needs.
//
//	pacman   Arch and Omarchy. checkupdates when pacman-contrib and fakeroot
//	         are both installed and `pacman -Qu` when they are not, plus
//	         Omarchy Server's own update wrapper when the machine has it.
//	apt      Debian and Ubuntu, with needrestart and /var/run/reboot-required.
//	dnf      Fedora and RHEL, dnf4 and dnf5, with needs-restarting.
//
// Around them sit systemctl, for the unattended-update timers; snapper, for
// the snapshot the plan takes first; and rpm, which is where dnf's "current
// version" column comes from.
//
// Every read here answers from what is already on disk. None of them
// refreshes the manager's metadata: a refresh writes to a root-owned cache,
// and `--check` has to be something an ordinary user can run.
package pkgmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tui-tools/tui-kit/compat"
	kit "github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// ErrNotAvailable reports that no supported package manager was found on this
// machine.
var ErrNotAvailable = kit.ErrNotAvailable

// historyLimit is how many past transactions the history screen shows.
const historyLimit = 20

// installHint is what the runner appends when a binary this backend wanted is
// not on the machine. The kit's own detection error carries the family's
// version of the same sentence.
const installHint = "tui-update drives pacman, apt or dnf; " +
	"or use --demo to explore the UI"

// managerBinary is the binary each manager is driven through here. It is not
// the kit's detection table: this one names the binary tui-update's own reads
// go to, which on Debian is `apt` for the upgradable list rather than the
// `apt-get` the plan writes with.
var managerBinary = map[string]string{
	updates.ManagerPacman: "pacman",
	updates.ManagerAPT:    "apt",
	updates.ManagerDNF:    "dnf",
}

// Detect picks the package manager this machine runs, and names the
// distribution it belongs to.
//
// The decision itself — which manager binaries are installed, cross-checked
// against /etc/os-release so a Debian container on an Arch host does not drive
// the host's pacman — is the kit's, and identical in every tool of the family.
// What is left here is the translation into the manager-neutral strings
// internal/updates is written in.
func Detect() (string, string, error) {
	manager, distro, err := kit.Detect()
	if err != nil {
		return "", distro.ID, err
	}
	return manager.String(), distro.ID, nil
}

// Real drives the machine's package manager. It satisfies updates.Backend.
type Real struct {
	manager string
	distro  string
	// runners holds one runner per binary, keyed by the name that appears in
	// a Command's Argv[0]. A binary that is not installed is simply absent,
	// and the code that wanted it says so where its answer would have been.
	runners map[string]*runner.Runner
	// caps gates the reads that only exist on a known manager version. It
	// comes from the manifest, so no version number is written into this
	// file.
	caps compat.Caps
	// omarchy reports that the machine carries Omarchy Server's own update
	// wrapper, which changes both how an upgrade is applied and how what it
	// leaves behind is classified.
	omarchy bool
	// noCheckupdates explains, on pacman, why `checkupdates` cannot be used
	// here. It is empty when it can be.
	noCheckupdates string
	// hold is what the last Load found about pinning packages here, kept so
	// BuildHold can refuse before building a command that would fail.
	hold updates.HoldSupport
}

// NewReal locates the binaries and, when not running as root, validates the
// configured privilege prefix. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run the commands directly.
//
// Reading is unprivileged: `checkupdates`, `apt list --upgradable` and
// `dnf check-update` all answer to any user against the metadata already on
// disk. Only the classifiers that read other processes' memory maps, and of
// course the upgrade itself, escalate.
func NewReal(sudoPrefix []string, caps compat.Caps) (*Real, error) {
	manager, distro, err := Detect()
	if err != nil {
		return nil, err
	}
	real := &Real{
		manager: manager,
		distro:  distro,
		runners: map[string]*runner.Runner{},
		caps:    caps,
	}

	// The reads that need no privilege, and the manager itself, which needs
	// one only when it is asked to change something.
	unprivileged := false
	for _, bin := range []string{
		"pacman", "checkupdates", "apt", "apt-get", "apt-mark", "dnf", "rpm",
		"systemctl", "snapper", OmarchyUpdate,
	} {
		real.add(bin, sudoPrefix, &unprivileged)
	}
	// The classifiers read /proc/<pid>/maps, which needs root to see every
	// process. They are registered as unprivileged readers all the same, so
	// escalatedRead can try the complete answer first and fall back to the
	// partial one instead of to nothing at all.
	for _, bin := range []string{"needrestart", "needs-restarting", OmarchyRestart} {
		real.add(bin, sudoPrefix, &unprivileged)
	}

	if real.runners[managerBinary[manager]] == nil {
		return nil, fmt.Errorf("pkgmgr: %s was detected but cannot be run: %w",
			manager, ErrNotAvailable)
	}
	real.omarchy = manager == updates.ManagerPacman &&
		real.runners[OmarchyUpdate] != nil
	if manager == updates.ManagerPacman {
		real.noCheckupdates = checkupdatesUnavailable(real.has("checkupdates"))
	}
	return real, nil
}

// checkupdatesUnavailable says why `checkupdates` cannot be used on this
// machine, and returns "" when it can.
//
// checkupdates is how pacman-contrib lists pending updates without touching
// the real sync database: it copies the database somewhere private and
// synchronises the copy, and it does that copy under `fakeroot`. So the
// checkupdates script being installed is not enough — fakeroot has to be
// there too, and it is a separate package that a server image has no other
// reason to carry. Omarchy Server 4.0.1 ships one without the other, where
// every call fails with "Cannot find the fakeroot binary".
//
// It is checked before the call rather than after it because the failing call
// is the one that goes through `sudo`, and an image that will never satisfy
// it should not produce a sudo complaint on every refresh.
func checkupdatesUnavailable(installed bool) string {
	if !installed {
		return "it is not installed (pacman-contrib)"
	}
	if !available("fakeroot") {
		return "fakeroot is not installed, and checkupdates builds its " +
			"private database under it"
	}
	return ""
}

// staleListNote is what the screen says when the pending list came from
// `pacman -Qu` instead of checkupdates: the same packages, compared against
// the sync database already on disk rather than a freshly synchronised copy.
func staleListNote(reason string) string {
	return "checkupdates unavailable (" + reason + "); pending list may be " +
		"stale — the plan's -Syu refreshes"
}

// add builds one runner, ignoring a binary the machine does not have.
func (r *Real) add(bin string, sudoPrefix []string, privilegedReads *bool) {
	run, err := runner.New(runner.Options{
		Bin:             bin,
		SearchPaths:     searchPaths[bin],
		SudoPrefix:      sudoPrefix,
		InstallHint:     installHint,
		PrivilegedReads: privilegedReads,
	})
	if err != nil {
		return
	}
	r.runners[bin] = run
}

// Name identifies the backend. It is the manifest's backend name, which is
// what the version probe is keyed on.
func (r *Real) Name() string { return r.manager }

// Describe names the manager and how it is reached, for the header.
func (r *Real) Describe() string {
	describe := r.runners[managerBinary[r.manager]].Describe()
	if r.distro != "" {
		describe += " on " + r.distro
	}
	if r.omarchy {
		describe += "  ·  " + OmarchyUpdate
	}
	return describe
}

// Capabilities reports what this backend supports on this machine.
func (r *Real) Capabilities() updates.Capabilities {
	caps := CapabilitiesFor(r.manager)
	caps.Snapshots = caps.Snapshots && r.runners["snapper"] != nil
	caps.Timers = caps.Timers && r.runners["systemctl"] != nil
	return caps
}

// CapabilitiesFor is what a manager can do, before the machine is consulted.
// It is shared by the real and the fake backend, so --demo behaves exactly
// like a real run.
func CapabilitiesFor(manager string) updates.Capabilities {
	caps := updates.Capabilities{
		Manager:   manager,
		Snapshots: true,
		History:   true,
		Timers:    true,
	}
	switch manager {
	case updates.ManagerAPT:
		// apt knows which updates are security updates — the pocket says so —
		// and still has no upgrade that applies only those. SecurityUpgrade
		// stays false here on purpose; the README says why at length.
		caps.SecurityMetadata = true
		caps.DistUpgrade = true
	case updates.ManagerDNF:
		caps.SecurityMetadata = true
		caps.SecurityUpgrade = true
		caps.PerPackageSize = true
	}
	return caps
}

// Preview renders the exact command line Run will execute. Every command goes
// through the runner of its own binary, so the preview carries the privilege
// prefix that binary will really be called with.
func (r *Real) Preview(cmd updates.Command) string {
	if run := r.runnerFor(cmd); run != nil {
		return run.Preview(cmd)
	}
	return cmd.String()
}

// runnerFor picks the runner that owns a command, by its argv[0].
func (r *Real) runnerFor(cmd updates.Command) *runner.Runner {
	if len(cmd.Argv) == 0 {
		return nil
	}
	return r.runners[cmd.Argv[0]]
}

// Run executes a previewed command.
func (r *Real) Run(ctx context.Context, cmd updates.Command) (string, error) {
	run := r.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("pkgmgr: %q is not available on this machine",
			firstArg(cmd))
	}
	return run.Run(ctx, cmd)
}

// firstArg names the binary a command wanted, for an error message.
func firstArg(cmd updates.Command) string {
	if len(cmd.Argv) == 0 {
		return "(empty command)"
	}
	return cmd.Argv[0]
}

// read runs one of the Build* read commands through the runner that owns it,
// returning the output even when the command exited non-zero.
//
// The second return value is the error, and callers are expected to look at
// the output first: `dnf check-update` exits 100 when there are updates and
// `needs-restarting -r` exits 1 when a reboot is needed, so on this backend a
// non-zero exit is often the answer rather than a failure.
func (r *Real) read(ctx context.Context, cmd updates.Command) (string, error) {
	run := r.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("pkgmgr: %q is not installed", firstArg(cmd))
	}
	return run.Read(ctx, cmd.Argv...)
}

// escalatedRead runs a read that sees more as root than as anyone else.
//
// The restart classifiers are all of that shape: they walk /proc looking for
// processes still holding a replaced file open, and a process another user
// owns is invisible without root. So the escalated call is tried first, and
// its failure — no `sudo -n`, or none configured — falls back to the plain
// one rather than to no answer.
//
// The second return value reports whether the answer is the complete one, so
// the screen can say which it got instead of presenting a partial scan as the
// whole truth.
func (r *Real) escalatedRead(ctx context.Context,
	cmd updates.Command) (string, bool, error) {
	run := r.runnerFor(cmd)
	if run == nil {
		return "", false, fmt.Errorf("pkgmgr: %q is not installed", firstArg(cmd))
	}
	if run.Privileged() {
		out, err := run.Run(ctx, cmd)
		if err == nil {
			return out, true, nil
		}
		// A command that answered on its own exit code — `needs-restarting -r`
		// exits 1 when a reboot is needed — still produced the answer.
		if strings.TrimSpace(out) != "" && !isEscalationFailure(out, err) {
			return out, true, nil
		}
	}
	out, err := run.Read(ctx, cmd.Argv...)
	return out, false, err
}

// isEscalationFailure recognises the two ways `sudo -n` refuses: it wants a
// password, or the user may not run the command at all. Both mean the output
// is sudo's complaint rather than the tool's answer.
func isEscalationFailure(out string, err error) bool {
	text := out + " " + err.Error()
	return strings.Contains(text, "password is required") ||
		strings.Contains(text, "a password is required") ||
		strings.Contains(text, "is not allowed to run") ||
		strings.Contains(text, "may not run")
}

// partialScanNote is what the plan says when the classifier could only see
// this user's own processes.
const partialScanNote = " — read without root, so only this user's own " +
	"processes were scanned"

// has reports whether a binary is available on this machine.
func (r *Real) has(bin string) bool { return r.runners[bin] != nil }

// Load reads the pending updates and everything around them.
//
// The read is layered, and every layer is allowed to fail on its own: a
// machine whose classifier needs a privilege `sudo -n` cannot grant still
// shows its pending list, and says in the plan how the classification was
// reached instead.
//
// The pending list is a layer like the others. When it cannot be read the
// model carries the reason in PendingError and the rest of it is still filled
// in: the snapshot support, the unattended-update timers, the restart probe
// and the history are read from somewhere else entirely, and losing all of
// them to one broken command is how a missing dependency turns into a blank
// screen. Load itself returns an error only when the manager is one it does
// not know.
func (r *Real) Load(ctx context.Context) (updates.Model, error) {
	model := updates.Model{Manager: r.manager, Distro: r.distro}

	pending, notes, err := r.loadPending(ctx)
	if err != nil {
		if errors.Is(err, errUnknownManager) {
			return updates.Model{}, err
		}
		model.PendingError = runner.FirstLine(err.Error())
		notes = append(notes, "the pending list could not be read ("+
			model.PendingError+"), so everything below describes the machine "+
			"but nothing above lists what is waiting")
	}
	model.Pending = pending
	for _, p := range pending {
		if p.Security {
			model.SecurityCount++
		}
	}

	model.Restart = MergeRestart(ClassifyFromPackages(pending), r.probeRestart(ctx))
	model.Snapshot = r.loadSnapshot(ctx)
	model.Timers = r.loadTimers(ctx)

	holds, support := r.loadHolds(ctx)
	model.Hold = support
	r.hold = support
	for i := range model.Pending {
		model.Pending[i].Held = holds[model.Pending[i].Name]
	}

	model.Notes = append(notes, r.notes()...)
	return model, nil
}

// loadHolds reads which packages this machine has pinned, and decides whether
// pinning is possible here at all.
//
// The two answers come from one read on purpose. On dnf the same call that
// lists the locks is what proves the versionlock plugin is installed: asking
// for the list is how the plugin's absence is discovered, and discovering it
// here is what lets the hold key refuse with the package name to install
// instead of letting `dnf versionlock add` fail in front of the user with a
// message about an unknown command.
func (r *Real) loadHolds(ctx context.Context) (map[string]bool, updates.HoldSupport) {
	cmd, ok := BuildHoldsRead(r.manager)
	if !ok {
		return nil, updates.HoldSupport{
			Reason: r.manager + " has no command that pins a package at a " +
				"version; on Arch that is the IgnorePkg line of " +
				"/etc/pacman.conf, which is a file to edit rather than a " +
				"command to run",
		}
	}
	if !r.has(cmd.Argv[0]) {
		return nil, updates.HoldSupport{
			Reason: cmd.Argv[0] + " is not installed, so no package can be held",
		}
	}

	out, err := r.read(ctx, cmd)
	if r.manager == updates.ManagerDNF && VersionlockMissing(out, err) {
		pkg := VersionlockPackage(r.distro)
		return nil, updates.HoldSupport{
			Reason: "the dnf versionlock plugin is not installed, so this " +
				"machine has no way to pin a package at a version",
			Hint: "install " + pkg,
		}
	}
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, updates.HoldSupport{
			Reason: "the held packages could not be read (" +
				runner.FirstLine(err.Error()) + ")",
		}
	}

	holds := map[string]bool{}
	switch r.manager {
	case updates.ManagerAPT:
		holds = ParseAPTHolds(out)
	case updates.ManagerDNF:
		holds = ParseDNFVersionlock(out)
	}
	return holds, updates.HoldSupport{
		Available: true,
		Reason:    holdReason(r.manager),
	}
}

// holdReason is the one line the plan and the confirm dialog use to say how a
// hold is really made on this manager.
func holdReason(manager string) string {
	if manager == updates.ManagerAPT {
		return "held with `apt-mark hold`, which is the dpkg selection every " +
			"apt front end honours"
	}
	return "locked with `dnf versionlock`, which excludes the package from " +
		"the available set — so a locked package stops appearing in the " +
		"pending list at all"
}

// errUnknownManager is the one pending-list failure that is not about the
// machine: a manager this build has no reader for at all. It is a programming
// error rather than a fact worth reporting on a screen, so it is the only one
// Load refuses to continue past.
var errUnknownManager = fmt.Errorf("pkgmgr: unknown package manager")

// notes are the facts about this machine worth stating once.
func (r *Real) notes() []string {
	var notes []string
	if r.omarchy {
		notes = append(notes, OmarchyUpdate+" is installed, so an upgrade "+
			"runs through it and it classifies what changed")
	}
	if !CapabilitiesFor(r.manager).SecurityMetadata {
		notes = append(notes, r.manager+" publishes no security metadata, "+
			"so no update here can be marked as a security fix")
	}
	return notes
}

// loadPending reads the pending update list for the active manager, along
// with anything about how it was read that the screen should say out loud.
func (r *Real) loadPending(ctx context.Context) ([]updates.Package, []string, error) {
	switch r.manager {
	case updates.ManagerPacman:
		return r.loadPendingPacman(ctx)
	case updates.ManagerAPT:
		packages, err := r.loadPendingAPT(ctx)
		return packages, nil, err
	case updates.ManagerDNF:
		packages, err := r.loadPendingDNF(ctx)
		return packages, nil, err
	default:
		return nil, nil, fmt.Errorf("%w %q", errUnknownManager, r.manager)
	}
}

// loadPendingPacman prefers checkupdates and falls back to `pacman -Qu`.
//
// The fallback is taken twice over: before the call, when the machine is
// missing something checkupdates needs, and after it, when it failed anyway
// for a reason nobody predicted. Either way the answer comes from pacman's
// own query against the sync database on disk, and the note says which it was
// — an older list is worth having, an empty screen is not.
func (r *Real) loadPendingPacman(ctx context.Context) ([]updates.Package,
	[]string, error) {
	var notes []string
	if r.noCheckupdates == "" {
		packages, _, err := r.readPacmanPending(ctx, true)
		if err == nil {
			return packages, nil, nil
		}
		notes = append(notes, staleListNote(runner.FirstLine(err.Error())))
	} else {
		notes = append(notes, staleListNote(r.noCheckupdates))
	}

	packages, out, err := r.readPacmanPending(ctx, false)
	if err != nil {
		return nil, notes, err
	}
	if syncDatabaseMissing(out) {
		notes = append(notes, "the pacman sync databases have never been "+
			"downloaded on this machine, so `pacman -Qu` has nothing to compare "+
			"the installed versions against and reports nothing pending")
	}
	return packages, notes, nil
}

// readPacmanPending runs one of the two pending-list commands.
//
// Both exit non-zero when there is nothing to do, which is not a failure: an
// empty list is the answer. So a failure is only a failure when it produced
// neither a parseable list nor one of the "up to date" replies.
func (r *Real) readPacmanPending(ctx context.Context,
	useCheckupdates bool) ([]updates.Package, string, error) {
	out, err := r.read(ctx, BuildPendingPacman(useCheckupdates))
	packages := ParsePacmanPending(out)
	if len(packages) == 0 && err != nil && !isNothingToDo(withoutPacmanWarnings(out)) {
		return nil, out, err
	}
	return packages, out, nil
}

// withoutPacmanWarnings drops the lines pacman writes to complain rather than
// to answer.
//
// It exists because `pacman -Qu` exits non-zero when nothing is upgradable,
// which is an answer, and the way to tell that apart from a failure is that
// it printed no packages. On a machine whose sync databases were never
// downloaded it prints a warning per repository instead, and those lines are
// enough to make an empty answer look like output from a failed command.
func withoutPacmanWarnings(out string) string {
	var kept []string
	for _, line := range splitLines(out) {
		if strings.HasPrefix(strings.TrimSpace(line), "warning:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// syncDatabaseMissing recognises pacman warning that a repository's database
// is not on disk at all. It happens on a freshly built image where no
// `pacman -Sy` has ever run: `pacman -Qu` still succeeds, and still reports
// nothing, because it has nothing to compare against.
func syncDatabaseMissing(out string) bool {
	return strings.Contains(out, "database file for") &&
		strings.Contains(out, "does not exist")
}

// isNothingToDo recognises the empty answers pacman and dnf give.
func isNothingToDo(out string) bool {
	trimmed := strings.TrimSpace(out)
	return trimmed == "" ||
		strings.Contains(trimmed, "there is nothing to do") ||
		strings.Contains(trimmed, "Nothing to do")
}

// loadPendingAPT reads `apt list --upgradable`.
func (r *Real) loadPendingAPT(ctx context.Context) ([]updates.Package, error) {
	out, err := r.read(ctx, BuildPendingAPT())
	if err != nil && !strings.Contains(out, "Listing") {
		return nil, err
	}
	return ParseAPTUpgradable(out), nil
}

// loadPendingDNF reads `dnf check-update` and fills in the two columns it
// does not print: what is installed today, from rpm, and the download size,
// from repoquery.
//
// check-update exits 100 when there are updates, which the runner reports as
// a failure. The output is what decides: a table that parsed is the answer,
// and an error with nothing parseable behind it is a real failure.
func (r *Real) loadPendingDNF(ctx context.Context) ([]updates.Package, error) {
	out, err := r.read(ctx, BuildPendingDNF())
	packages := ParseDNFCheckUpdate(out)
	if len(packages) == 0 {
		if err != nil && !isNothingToDo(out) {
			return nil, err
		}
		return nil, nil
	}

	names := make([]string, 0, len(packages))
	for _, p := range packages {
		names = append(names, p.Name)
	}
	if cmd, buildErr := BuildInstalledVersionsDNF(names); buildErr == nil {
		if rpmOut, rpmErr := r.read(ctx, cmd); rpmErr == nil || rpmOut != "" {
			// The `key|version` shape is the one the kit asks dpkg-query
			// and rpm for too, so the reader is shared.
			installed := kit.ParsePipedVersions(rpmOut)
			for i := range packages {
				packages[i].Current = installed[packages[i].Label()]
			}
		}
	}
	if sizeOut, sizeErr := r.read(ctx, BuildSizesDNF()); sizeErr == nil {
		sizes := ParseDNFSizes(sizeOut)
		for i := range packages {
			if size, ok := sizes[packages[i].Label()]; ok {
				packages[i].Size = humanSize(size)
			}
		}
	}
	if secOut, secErr := r.read(ctx, BuildSecurityDNF()); secErr == nil {
		advisories := ParseDNFSecurity(secOut)
		for i := range packages {
			if advisory, ok := advisories[packages[i].Label()]; ok {
				packages[i].Security = true
				packages[i].SecurityRef = advisory.ID
				if advisory.Severity != "" {
					packages[i].SecurityRef += " (" + advisory.Severity + ")"
				}
			}
		}
	}
	return packages, nil
}

// probeRestart asks the machine what would have to restart.
//
// Each manager has its own classifier and every one of them reads the process
// table, which needs root. When that read cannot be made — no `sudo -n`, or
// the tool is not installed — the returned Restart carries only a Detail
// saying so, and MergeRestart leaves the name-based classification standing.
func (r *Real) probeRestart(ctx context.Context) updates.Restart {
	switch r.manager {
	case updates.ManagerPacman:
		return r.probeRestartPacman(ctx)
	case updates.ManagerAPT:
		return r.probeRestartAPT(ctx)
	case updates.ManagerDNF:
		return r.probeRestartDNF(ctx)
	default:
		return updates.Restart{}
	}
}

// probeRestartPacman uses the Omarchy Server restart pass when the machine
// has it. Plain Arch has no equivalent, so there the package list is all
// there is, which the plan screen says out loud.
func (r *Real) probeRestartPacman(ctx context.Context) updates.Restart {
	if !r.has(OmarchyRestart) {
		return updates.Restart{
			Detail: "no restart classifier on this machine: Arch ships none, " +
				"and " + OmarchyRestart + " is not installed",
		}
	}
	cmd, err := BuildRestartProbe("omarchy")
	if err != nil {
		return updates.Restart{Detail: err.Error()}
	}
	out, root, err := r.escalatedRead(ctx, cmd)
	if !strings.Contains(out, "reboot required:") {
		return updates.Restart{Detail: unreachable(OmarchyRestart, err)}
	}
	restart := ParseOmarchyRestart(out)
	if !root {
		// The wrapper refuses to run as anything but root, so a fallback
		// read here produced its usage error rather than a classification.
		restart.Detail += partialScanNote
	}
	return restart
}

// probeRestartAPT reads needrestart, and Debian's own reboot-required flag.
func (r *Real) probeRestartAPT(ctx context.Context) updates.Restart {
	restart := updates.Restart{}
	if names, ok := readRebootRequired(); ok {
		restart.RebootRequired = true
		restart.Source = RebootRequiredFile
		restart.Reason = "the machine already has " + RebootRequiredFile
		if len(names) > 0 {
			restart.Reason += ", set by " + strings.Join(names, ", ")
		}
	}
	if !r.has("needrestart") {
		if restart.Source == "" {
			restart.Detail = "needrestart is not installed, so no service " +
				"could be checked against what it has mapped"
		}
		return restart
	}
	cmd, err := BuildRestartProbe("needrestart")
	if err != nil {
		return restart
	}
	out, root, err := r.escalatedRead(ctx, cmd)
	if !strings.Contains(out, "NEEDRESTART-") {
		restart.Detail = unreachable("needrestart", err)
		return restart
	}
	merged := MergeRestart(restart, ParseNeedrestart(out))
	if !root {
		merged.Detail += partialScanNote
	}
	return merged
}

// readRebootRequired reports whether Debian's flag file is there, and which
// packages set it.
func readRebootRequired() ([]string, bool) {
	if _, err := os.Stat(RebootRequiredFile); err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(RebootRequiredPkgs)
	if err != nil {
		return nil, true
	}
	return ParseRebootRequiredPkgs(string(raw)), true
}

// probeRestartDNF uses needs-restarting: `-r` for the reboot verdict and `-s`
// for the services.
//
// The standalone binary is preferred over `dnf needs-restarting`. On dnf5 the
// subcommand refreshes repository metadata before answering — a privileged
// write on a read path, and the one thing the smoke test asserts never
// happens — while the plugin binary answers from /proc alone.
func (r *Real) probeRestartDNF(ctx context.Context) updates.Restart {
	restart := updates.Restart{}
	if !r.has("needs-restarting") {
		restart.Detail = "needs-restarting is not installed (dnf-plugins-core), " +
			"so nothing read the running processes"
		if r.caps.Has(FeatureDNF5) {
			restart.Detail += "; `dnf needs-restarting` was not used in its " +
				"place because on dnf5 it refreshes the metadata first"
		}
		return restart
	}
	restart.Source = "needs-restarting"

	if cmd, err := BuildRestartProbe("needs-restarting-reboot"); err == nil {
		out, _, _ := r.escalatedRead(ctx, cmd)
		// The exit code says it too (1 when a reboot is needed), but the
		// runner does not carry one, and the sentence is stable and is what
		// the screen would quote anyway.
		if strings.Contains(out, "Reboot is required") {
			restart.RebootRequired = true
			restart.Reason = runner.FirstLine(out)
		}
	}
	if cmd, err := BuildRestartProbe("needs-restarting-services"); err == nil {
		out, root, readErr := r.escalatedRead(ctx, cmd)
		if readErr != nil && strings.TrimSpace(out) == "" {
			restart.Detail = unreachable("needs-restarting -s", readErr)
		} else {
			restart.Services = ParseNeedsRestartingServices(out)
			if !root {
				restart.Detail = "needs-restarting -s" + partialScanNote
			}
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

// unreachable turns a failed probe into one sentence for the plan screen.
func unreachable(what string, err error) string {
	return what + " could not be read (" + runner.FirstLine(err.Error()) +
		"), so the classification comes from the package names alone"
}

// loadSnapshot decides whether a pre-upgrade snapshot can be taken, and
// builds the exact commands that would take it.
//
// The answer is yes only when snapper is installed and has a configuration
// covering the root subvolume: a snapshot of /home before a kernel upgrade is
// not the thing anyone wanted, and a machine on ext4 has nowhere to put one
// at all.
func (r *Real) loadSnapshot(ctx context.Context) updates.Snapshot {
	if !r.has("snapper") {
		return updates.Snapshot{
			Reason: "snapper is not installed, so nothing can be snapshotted " +
				"before the upgrade",
		}
	}
	out, err := r.read(ctx, BuildSnapperConfigs())
	if err != nil {
		return updates.Snapshot{
			Reason: "snapper is installed but its configurations could not be " +
				"read (" + runner.FirstLine(err.Error()) + ")",
		}
	}
	for _, config := range ParseSnapperConfigs(out) {
		if config != SnapperRootConfig {
			continue
		}
		return snapshotFor(config, r.manager)
	}
	return updates.Snapshot{
		Reason: "snapper has no " + SnapperRootConfig + " configuration, so " +
			"the root subvolume cannot be snapshotted",
	}
}

// snapshotFor builds the pre/post pair for a configuration.
func snapshotFor(config, manager string) updates.Snapshot {
	pre, err := BuildSnapshot(config, "pre", "tui-update: before the "+manager+" upgrade")
	if err != nil {
		return updates.Snapshot{Reason: err.Error()}
	}
	post, err := BuildSnapshot(config, "post", "tui-update: after the "+manager+" upgrade")
	if err != nil {
		return updates.Snapshot{Reason: err.Error()}
	}
	return updates.Snapshot{
		Available: true,
		Config:    config,
		Reason: "snapper has a " + config + " configuration, so the root " +
			"subvolume is snapshotted before and after",
		Pre:  pre,
		Post: post,
	}
}

// unitsFor is the unattended-update mechanism each manager ships, with one
// line saying what leaving it on means.
func unitsFor(manager string, omarchy bool) []updates.Timer {
	switch manager {
	case updates.ManagerPacman:
		if !omarchy {
			return nil
		}
		return []updates.Timer{{
			Unit:        UnitOmarchyTimer,
			Description: "Omarchy Server's daily unattended upgrade",
		}}
	case updates.ManagerAPT:
		return []updates.Timer{
			{
				Unit:        UnitAPTDaily,
				Description: "apt's daily unattended upgrade",
			},
			{
				Unit:        UnitUnattended,
				Description: "the unattended-upgrades service the timer drives",
			},
		}
	case updates.ManagerDNF:
		return []updates.Timer{{
			Unit:        UnitDNFAutomatic,
			Description: "dnf's automatic upgrade timer",
		}}
	default:
		return nil
	}
}

// loadTimers reads the state of each unattended-update unit.
func (r *Real) loadTimers(ctx context.Context) []updates.Timer {
	if !r.has("systemctl") {
		return nil
	}
	timers := unitsFor(r.manager, r.omarchy)
	for i := range timers {
		timers[i] = r.readTimer(ctx, timers[i])
	}
	return timers
}

// readTimer fills in one unit's state. `systemctl is-enabled` exits non-zero
// for a disabled or missing unit, which is an answer rather than a failure,
// so the word it printed is what is kept.
func (r *Real) readTimer(ctx context.Context, timer updates.Timer) updates.Timer {
	enabledCmd, err := BuildTimerState(timer.Unit, "is-enabled")
	if err != nil {
		return timer
	}
	state, _ := r.read(ctx, enabledCmd)
	timer.State = runner.FirstLine(strings.TrimSpace(state))
	timer.Present = timer.State != "" && timer.State != "not-found"
	timer.Enabled = timer.State == "enabled" || timer.State == "enabled-runtime" ||
		timer.State == "static" || timer.State == "alias"

	activeCmd, err := BuildTimerState(timer.Unit, "is-active")
	if err != nil {
		return timer
	}
	active, _ := r.read(ctx, activeCmd)
	timer.Active = strings.TrimSpace(runner.FirstLine(active)) == "active"
	return timer
}

// Plan assembles the sequence that applies the pending updates.
//
// The order is the whole argument of the screen: snapshot, refresh, upgrade,
// snapshot. The snapshot comes first because it is only worth anything before
// the change; the refresh is in the sequence rather than in the read path
// because it writes to a root-owned cache; and nothing reboots, ever — the
// reboot is offered afterwards as its own confirmed command.
func (r *Real) Plan(ctx context.Context, opts updates.PlanOptions) (updates.Plan, error) {
	model, err := r.Load(ctx)
	if err != nil {
		return updates.Plan{}, err
	}
	if model.PendingError != "" {
		return updates.Plan{}, fmt.Errorf(
			"pkgmgr: the pending list could not be read, so there is nothing to "+
				"plan from: %s", model.PendingError)
	}
	if err := planAllowed(opts.Mode, len(model.Pending),
		model.SecurityCount); err != nil {
		return updates.Plan{}, err
	}

	plan := assemblePlan(r.manager, opts, model, r.omarchy)
	if plan.Error != nil {
		return updates.Plan{}, plan.Error
	}
	plan.Plan.DryRun, plan.Plan.Notes = r.dryRun(ctx, opts.Mode, model)
	return withPlanNotes(plan.Plan, opts, model), nil
}

// planAllowed refuses a plan there is nothing to build. A mode nobody can act
// on is refused here rather than producing a command that would upgrade
// everything, which is the failure worth being loud about: a reader who asked
// for the security fixes must never be handed the full upgrade instead.
func planAllowed(mode string, pending, security int) error {
	if pending == 0 {
		return fmt.Errorf("pkgmgr: there is nothing to upgrade")
	}
	if mode == updates.UpgradeSecurity && security == 0 {
		return fmt.Errorf("pkgmgr: nothing pending here is a security fix, " +
			"so a security-only upgrade would do nothing")
	}
	return nil
}

// builtPlan is the assembled sequence, or the reason it could not be built.
type builtPlan struct {
	Plan  updates.Plan
	Error error
}

// assemblePlan builds the command sequence, and is shared by the real backend
// and the demo one so --demo produces the very same order: snapshot, refresh,
// upgrade, snapshot, and never a reboot.
func assemblePlan(manager string, opts updates.PlanOptions,
	model updates.Model, omarchy bool) builtPlan {
	take := opts.Snapshot && model.Snapshot.Available
	plan := updates.Plan{
		Title: planTitle(manager, opts.Mode, len(model.Pending),
			model.SecurityCount),
		Mode:         opts.Mode,
		Restart:      model.Restart,
		Snapshot:     model.Snapshot,
		TakeSnapshot: take,
	}
	if take {
		plan.Commands = append(plan.Commands, model.Snapshot.Pre)
	}
	if refresh, ok := BuildRefresh(manager); ok {
		plan.Commands = append(plan.Commands, refresh)
	}
	upgrade, err := BuildUpgrade(manager, opts.Mode, omarchy)
	if err != nil {
		return builtPlan{Error: err}
	}
	plan.Commands = append(plan.Commands, upgrade)
	if take {
		plan.Commands = append(plan.Commands, model.Snapshot.Post)
	}
	return builtPlan{Plan: plan}
}

// withPlanNotes adds the caveats that apply to a plan however it was built.
func withPlanNotes(plan updates.Plan, opts updates.PlanOptions,
	model updates.Model) updates.Plan {
	switch {
	case !model.Snapshot.Available:
		plan.Notes = append(plan.Notes, "no snapshot: "+model.Snapshot.Reason)
	case !opts.Snapshot:
		plan.Notes = append(plan.Notes, "the snapshot was turned off for this "+
			"plan (s turns it back on); this machine could take one")
	}
	if opts.Mode == updates.UpgradeSecurity {
		plan.Notes = append(plan.Notes, "security-only: the manager resolves "+
			"the advisories, so packages with no advisory of their own can "+
			"still be pulled in as dependencies")
	}
	if model.Restart.Detail != "" {
		plan.Notes = append(plan.Notes, model.Restart.Detail)
	}
	// The model's own notes come last, so the caveats specific to this plan
	// are the ones read first.
	plan.Notes = append(plan.Notes, model.Notes...)
	return plan
}

// planTitle is the one line the confirm dialog is headed with.
func planTitle(manager, mode string, pending, security int) string {
	switch mode {
	case updates.UpgradeDist:
		title := fmt.Sprintf("Dist-upgrade %d package(s) with %s", pending, manager)
		if security > 0 {
			title += fmt.Sprintf(", %d of them security fixes", security)
		}
		return title
	case updates.UpgradeSecurity:
		return fmt.Sprintf(
			"Upgrade the %d security fix(es) of %d pending with %s",
			security, pending, manager)
	default:
		title := fmt.Sprintf("Upgrade %d package(s) with %s", pending, manager)
		if security > 0 {
			title += fmt.Sprintf(", %d of them security fixes", security)
		}
		return title
	}
}

// dryRun asks the manager to say what it would do.
//
// apt and dnf both simulate properly and their output is quoted verbatim.
// pacman cannot: every way of asking it what `-Syu` would do begins by
// synchronising the real database, which is a privileged write, so there the
// pending list stands in for a simulation and the note says as much.
func (r *Real) dryRun(ctx context.Context, mode string,
	model updates.Model) (string, []string) {
	cmd, ok := BuildSimulate(r.manager, mode)
	if !ok {
		return pendingAsText(model.Pending),
			[]string{"pacman has no dry run that does not first synchronise " +
				"the databases, which needs root, so the list above is the " +
				"pending list rather than a simulated transaction"}
	}
	out, err := r.read(ctx, cmd)
	if strings.TrimSpace(out) == "" && err != nil {
		return pendingAsText(model.Pending),
			[]string{"the simulation could not be run (" +
				runner.FirstLine(err.Error()) + "), so the list above is the " +
				"pending list"}
	}
	return out, nil
}

// pendingAsText renders the pending list the way a dry run would have.
func pendingAsText(packages []updates.Package) string {
	lines := make([]string, 0, len(packages))
	for _, p := range packages {
		lines = append(lines, p.Label()+"  "+p.Transition())
	}
	return strings.Join(lines, "\n")
}

// History returns the manager's last transactions, newest first.
func (r *Real) History(ctx context.Context) ([]updates.Transaction, error) {
	switch r.manager {
	case updates.ManagerPacman:
		raw, err := readLog(PacmanLog)
		if err != nil {
			return nil, err
		}
		return ParsePacmanLog(raw, historyLimit), nil
	case updates.ManagerAPT:
		raw, err := readLog(APTHistoryLog)
		if err != nil {
			return nil, err
		}
		return ParseAPTHistory(raw, historyLimit), nil
	case updates.ManagerDNF:
		out, err := r.read(ctx, BuildHistoryDNF())
		if err != nil && strings.TrimSpace(out) == "" {
			return nil, err
		}
		return ParseDNFHistory(out, historyLimit), nil
	default:
		return nil, fmt.Errorf("pkgmgr: unknown package manager %q", r.manager)
	}
}

// readLog reads a manager's transaction log, saying what is missing rather
// than repeating the syscall's own wording.
//
// A machine can legitimately have no log: an image built by installing into a
// chroot and then cleaned, which is how Omarchy Server's cloud image is made,
// starts life without one and grows it on the first upgrade. That is an empty
// history, not a broken tool, and the sentence says so.
func readLog(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a constant path in this package
	if err == nil {
		return string(raw), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s does not exist yet, so this machine has no "+
			"recorded transactions", path)
	}
	if errors.Is(err, os.ErrPermission) {
		return "", fmt.Errorf("%s cannot be read by this user", path)
	}
	return "", err
}

// BuildTimerAction enables or disables an unattended-update unit.
func (r *Real) BuildTimerAction(action, unit string) (updates.Command, error) {
	return BuildTimerAction(action, unit)
}

// BuildHold pins a package at its installed version, or lifts that pin.
//
// The refusal is made here rather than left to the command: a machine without
// the dnf versionlock plugin would otherwise be handed a `dnf versionlock add`
// that fails complaining about an unknown command, which names neither the
// plugin nor the package that carries it.
func (r *Real) BuildHold(action, name string) (updates.Command, error) {
	if err := holdRefusal(r.hold); err != nil {
		return updates.Command{}, err
	}
	return BuildHold(r.manager, action, name)
}

// holdRefusal turns an unavailable hold into the sentence the status line
// shows, hint included.
func holdRefusal(support updates.HoldSupport) error {
	if support.Available {
		return nil
	}
	message := support.Reason
	if message == "" {
		message = "this machine cannot hold a package at a version"
	}
	if support.Hint != "" {
		message += " — " + support.Hint
	}
	return fmt.Errorf("pkgmgr: %s", message)
}

// BuildReboot is the reboot the tool offers and never takes by itself.
func (r *Real) BuildReboot() (updates.Command, error) { return BuildReboot() }
