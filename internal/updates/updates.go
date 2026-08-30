// Package updates defines the manager-agnostic model tui-update renders and
// the interface every package manager implementation satisfies. The UI knows
// only these types: it never builds a pacman, apt or dnf argv itself.
// Mutations are Command values produced by the backend, shown in a preview
// dialog and only then executed.
package updates

import (
	"context"
	"sort"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// Command is a single invocation the user is about to run. Argv excludes any
// privilege wrapper: the backend adds it when previewing and when executing.
//
// It is an alias rather than a type of its own, so a backend hands the very
// value the confirm dialog displayed straight to the kit runner, with no
// conversion in between. That identity is what makes the preview a promise.
type Command = runner.Command

// The package managers tui-update drives. One is active on a machine: they
// are detected by binary plus /etc/os-release, never both at once.
const (
	ManagerPacman = "pacman"
	ManagerAPT    = "apt"
	ManagerDNF    = "dnf"
)

// Groups order the pending list. A kernel or a firmware update is the reason
// a reboot ends up on the plan, so it is shown first rather than buried in an
// alphabetical list of a hundred packages.
const (
	// GroupKernel is the kernel and everything that boots with it.
	GroupKernel = "kernel"
	// GroupFirmware is firmware and CPU microcode.
	GroupFirmware = "firmware"
	// GroupCore is the userspace nothing survives a change of: glibc,
	// systemd, the message bus.
	GroupCore = "core"
	// GroupOther is everything else.
	GroupOther = "other"
)

// groupRank orders the groups for display.
var groupRank = map[string]int{
	GroupKernel:   0,
	GroupFirmware: 1,
	GroupCore:     2,
	GroupOther:    3,
}

// GroupRank is the display order of a group, so the UI sorts without knowing
// the names.
func GroupRank(group string) int {
	if rank, ok := groupRank[group]; ok {
		return rank
	}
	return len(groupRank)
}

// Package is one pending update, as the manager reports it.
type Package struct {
	// Name is the package name, without the architecture suffix.
	Name string
	// Arch is the architecture, empty on a manager that does not report one.
	Arch string
	// Current is the installed version, empty when the manager does not say
	// what is installed today.
	Current string
	// New is the version that would be installed.
	New string
	// Repo is the repository the new version comes from.
	Repo string
	// Size is the download size, already rendered, empty when the manager
	// does not expose it.
	Size string
	// Security reports that the update carries a security fix.
	Security bool
	// SecurityRef is what said so: an advisory id on dnf, the pocket name on
	// apt. Empty on a manager that publishes no security metadata.
	SecurityRef string
	// Group is one of the Group* constants.
	Group string
	// Ignored reports a package the manager itself is holding back.
	Ignored bool
}

// Label renders the package for a one-line summary.
func (p Package) Label() string {
	if p.Arch == "" {
		return p.Name
	}
	return p.Name + "." + p.Arch
}

// Transition renders "current → new", falling back to the new version alone
// when the manager did not report what is installed.
func (p Package) Transition() string {
	if p.Current == "" {
		return p.New
	}
	return p.Current + " → " + p.New
}

// SortPackages orders the pending list: kernel and firmware first, then the
// core packages, then everything else, alphabetically within each group.
func SortPackages(packages []Package) {
	sort.SliceStable(packages, func(i, j int) bool {
		a, b := GroupRank(packages[i].Group), GroupRank(packages[j].Group)
		if a != b {
			return a < b
		}
		return packages[i].Name < packages[j].Name
	})
}

// The restart classes, in increasing severity. They are the whole point of
// the plan screen: an upgrade that needs nothing, one that needs a handful of
// services bounced, and one that is only really applied after a reboot.
const (
	// RestartNone means nothing running has to be touched.
	RestartNone = "none"
	// RestartServices means some services keep old code mapped until they
	// are restarted.
	RestartServices = "services"
	// RestartReboot means the kernel, glibc, systemd or firmware changed.
	RestartReboot = "reboot"
)

// Restart is what the upgrade would leave behind: what has to be restarted,
// and whether the machine has to be rebooted for the change to be real.
type Restart struct {
	// Class is one of RestartNone, RestartServices or RestartReboot.
	Class string
	// Services are the units that keep old code mapped, short names.
	Services []string
	// Reason is why a reboot is called for, in the classifier's own words.
	Reason string
	// RebootRequired is the manager's own signal — /var/run/reboot-required
	// on apt, `needs-restarting -r` on dnf — as opposed to a guess made from
	// the package list.
	RebootRequired bool
	// Source names what produced this classification, so a reader can tell a
	// real answer from the fallback.
	Source string
	// Detail is what the source could not do, when it could not: the
	// classifier needs root on most machines and `sudo -n` does not always
	// answer.
	Detail string
}

// Snapshot is the pre-upgrade snapshot the plan would take, when the machine
// has somewhere to take one.
type Snapshot struct {
	// Available reports that a snapshot can be taken before the upgrade.
	Available bool
	// Config is the snapper configuration used ("root").
	Config string
	// Reason explains Available either way, in one sentence.
	Reason string
	// Pre and Post are the exact commands, previewed like every other.
	Pre  Command
	Post Command
}

// Timer is one unattended-update mechanism the machine carries: the systemd
// timer or service that upgrades packages without anyone asking.
type Timer struct {
	// Unit is the systemd unit name.
	Unit string
	// Present reports that the unit exists on this machine at all.
	Present bool
	// Enabled reports that it starts on boot; Active that it is running now.
	Enabled bool
	Active  bool
	// State is systemctl's own word for it ("enabled", "disabled",
	// "not-found"), shown rather than interpreted.
	State string
	// Description is one line saying what turning it on means.
	Description string
}

// Transaction is one entry of the manager's own history, read-only.
type Transaction struct {
	// ID is the manager's identifier: a dnf transaction id, or the timestamp
	// pacman and apt log.
	ID string
	// When is the date and time, as the log wrote it.
	When string
	// Command is the command line that was run.
	Command string
	// Detail is the one-line summary: how many packages, or which ones.
	Detail string
}

// Model is the whole picture tui-update renders.
type Model struct {
	// Manager is one of the Manager* constants.
	Manager string
	// Distro is the machine's ID from /etc/os-release, so the header can say
	// which machine the manager was detected on.
	Distro string
	// Pending is the update list, already sorted by SortPackages.
	Pending []Package
	// SecurityCount is how many of them carry a security fix.
	SecurityCount int
	// Restart is the classification of what the upgrade would leave behind.
	Restart Restart
	// Snapshot is what would be taken before the upgrade.
	Snapshot Snapshot
	// Timers are the unattended-update mechanisms found on the machine.
	Timers []Timer
	// Notes are facts about this machine worth showing once: an
	// omarchy-server-update wrapper found, a metadata cache nobody has
	// refreshed, a classifier that needed a privilege it did not get.
	Notes []string
}

// Security returns the pending packages that carry a security fix.
func (m Model) Security() []Package {
	var out []Package
	for _, p := range m.Pending {
		if p.Security {
			out = append(out, p)
		}
	}
	return out
}

// Grouped returns the pending packages of one group.
func (m Model) Grouped(group string) []Package {
	var out []Package
	for _, p := range m.Pending {
		if p.Group == group {
			out = append(out, p)
		}
	}
	return out
}

// Timer returns the timer with that unit name.
func (m Model) Timer(unit string) (Timer, bool) {
	for _, t := range m.Timers {
		if t.Unit == unit {
			return t, true
		}
	}
	return Timer{}, false
}

// Plan is the upgrade the user is about to approve: what the manager's own
// dry run said, what would have to restart afterwards, whether a snapshot is
// taken first, and the exact command sequence that applies all of it.
type Plan struct {
	// Title is the one-line summary the confirm dialog is headed with.
	Title string
	// DryRun is the manager's own simulation output, verbatim.
	DryRun string
	// Restart and Snapshot are the model's, re-read as part of planning.
	Restart  Restart
	Snapshot Snapshot
	// Commands run in order, and are what the confirm dialog shows. The
	// snapshot commands are already in it when one is taken.
	Commands []Command
	// Notes are the caveats that apply to this plan.
	Notes []string
}

// Preview renders every command of the plan, one per line, without a runner:
// it is the plain text form, for tests and for the plan screen's body.
func (p Plan) Preview() string {
	lines := make([]string, 0, len(p.Commands))
	for _, cmd := range p.Commands {
		lines = append(lines, cmd.String())
	}
	return strings.Join(lines, "\n")
}

// Capabilities tells the UI what a backend supports, so the key map and the
// screens are built from the backend rather than hardcoded.
type Capabilities struct {
	// Manager is the manager name, echoed so a Capabilities value stands
	// alone.
	Manager string
	// SecurityMetadata reports that the manager publishes which updates are
	// security fixes. Without it the column reads "n/a" rather than "no".
	SecurityMetadata bool
	// DistUpgrade reports that the manager separates a plain upgrade from one
	// allowed to add and remove packages (apt's dist-upgrade).
	DistUpgrade bool
	// Snapshots reports that a pre-upgrade snapshot can be taken here.
	Snapshots bool
	// History reports that the manager keeps a transaction log.
	History bool
	// Timers reports that the machine has an unattended-update mechanism to
	// enable or disable.
	Timers bool
	// PerPackageSize reports that the pending list carries a size per
	// package.
	PerPackageSize bool
}

// The upgrade modes BuildUpgrade accepts.
const (
	// UpgradeDefault is the plain upgrade: no package is added or removed.
	UpgradeDefault = "upgrade"
	// UpgradeDist is apt's dist-upgrade, which may add and remove packages
	// to satisfy a change in dependencies.
	UpgradeDist = "dist-upgrade"
)

// The timer actions BuildTimerAction accepts.
const (
	TimerEnable  = "enable"
	TimerDisable = "disable"
)

// Backend is the boundary between the UI and the machine. Load and History
// read; Plan turns the pending list into a previewable sequence; Run executes
// a Command the user confirmed. Nothing else may mutate the system.
type Backend interface {
	// Name is the manager identifier ("pacman", "apt", "dnf").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Capabilities reports what this backend supports.
	Capabilities() Capabilities

	// Preview renders the exact command line Run will execute, privilege
	// wrapper included. This is the text shown in the confirm dialog.
	Preview(cmd Command) string

	// Load reads the pending updates and everything around them. It never
	// refreshes the manager's metadata: a read that needs root would turn
	// `--check` into a privileged operation.
	Load(ctx context.Context) (Model, error)
	// Plan runs the manager's own dry run and assembles the sequence that
	// would apply it. mode is one of the Upgrade* constants.
	Plan(ctx context.Context, mode string) (Plan, error)
	// History returns the manager's last transactions, newest first.
	History(ctx context.Context) ([]Transaction, error)
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd Command) (string, error)

	// BuildTimerAction enables or disables an unattended-update unit.
	BuildTimerAction(action, unit string) (Command, error)
	// BuildReboot is the reboot the tool offers but never takes by itself.
	BuildReboot() (Command, error)
}
