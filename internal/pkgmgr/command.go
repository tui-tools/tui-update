package pkgmgr

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-update/internal/updates"
)

// The version-gated capabilities of the backends, named the way the manifest
// names them. A tool asks the compat set for these instead of comparing
// version numbers in the code.
const (
	// FeatureDNF5 is the dnf5 rewrite, which ships as `dnf` from Fedora 41.
	// It moved `needs-restarting` out of the standalone plugin and into the
	// main binary, where it refreshes repository metadata before answering —
	// which is exactly what a read path must not do.
	FeatureDNF5 = "dnf5"
	// FeatureAPTSolver3 is apt's newer dependency solver, which changes the
	// wording around a simulated upgrade but not the `Inst`/`Conf` lines the
	// plan is parsed from.
	FeatureAPTSolver3 = "solver3"
)

// The paths and unit names the backends read. They are constants rather than
// literals scattered through the code because a smoke test asserts on them.
const (
	// PacmanLog is the transaction log pacman writes.
	PacmanLog = "/var/log/pacman.log"
	// APTHistoryLog is apt's own history.
	APTHistoryLog = "/var/log/apt/history.log"
	// RebootRequiredFile is the flag Debian and Ubuntu drop when a package
	// asked for a reboot.
	RebootRequiredFile = "/var/run/reboot-required"
	// RebootRequiredPkgs names the packages that dropped it.
	RebootRequiredPkgs = "/var/run/reboot-required.pkgs"
	// SnapperConfigDir holds one file per snapper configuration.
	SnapperConfigDir = "/etc/snapper/configs"
	// SnapperRootConfig is the configuration covering /, the only one a
	// pre-upgrade snapshot is worth taking on.
	SnapperRootConfig = "root"
)

// The unattended-update units, one per manager plus the Omarchy Server one.
const (
	UnitOmarchyTimer = "omarchy-server-update.timer"
	UnitAPTDaily     = "apt-daily-upgrade.timer"
	UnitUnattended   = "unattended-upgrades.service"
	UnitDNFAutomatic = "dnf-automatic.timer"
)

// The Omarchy Server wrappers. On a machine that has them they are what
// really performs an upgrade, and their restart pass is the only classifier
// on Arch that reads the process table rather than guessing from names.
const (
	OmarchyUpdate  = "omarchy-server-update"
	OmarchyRestart = "omarchy-server-update-restart"
)

// searchPaths are the locations a non-root PATH commonly omits. The
// administrative half of this list lives in an sbin directory on Debian.
var searchPaths = map[string][]string{
	"pacman":           {"/usr/bin/pacman", "/bin/pacman"},
	"checkupdates":     {"/usr/bin/checkupdates", "/bin/checkupdates"},
	"apt":              {"/usr/bin/apt", "/bin/apt"},
	"apt-get":          {"/usr/bin/apt-get", "/bin/apt-get"},
	"dnf":              {"/usr/bin/dnf", "/bin/dnf"},
	"rpm":              {"/usr/bin/rpm", "/bin/rpm"},
	"needrestart":      {"/usr/sbin/needrestart", "/sbin/needrestart", "/usr/bin/needrestart"},
	"needs-restarting": {"/usr/bin/needs-restarting", "/bin/needs-restarting"},
	"snapper":          {"/usr/bin/snapper", "/bin/snapper"},
	"systemctl":        {"/usr/bin/systemctl", "/bin/systemctl"},
	OmarchyUpdate:      {"/usr/bin/" + OmarchyUpdate, "/usr/local/bin/" + OmarchyUpdate},
	OmarchyRestart:     {"/usr/bin/" + OmarchyRestart, "/usr/local/bin/" + OmarchyRestart},
}

// unitNameRe is the set of characters a systemd unit name may contain. The
// unit is the one argument of a timer action that comes from the model and
// ends up in an argv, so it is validated before a command exists.
var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.\\:-]{0,127}\.(timer|service)$`)

// checkUnit rejects a unit name that is not a plausible systemd unit.
func checkUnit(unit string) error {
	if !unitNameRe.MatchString(unit) {
		return fmt.Errorf("pkgmgr: %q is not a systemd unit name", unit)
	}
	return nil
}

// snapperConfigRe is the set of characters a snapper configuration name may
// contain. Only "root" is ever used today, and the check is here so that
// stays true.
var snapperConfigRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// The pending-list reads, one per manager. Each is unprivileged and each
// answers from the metadata already on disk: none of them may refresh it,
// because a refresh needs root and `--check` has to work as an ordinary user.
//
// BuildPendingPacman prefers `checkupdates`, which pacman-contrib ships
// precisely so a pending list can be read without touching the real sync
// database. `pacman -Qu` is the fallback: it answers from whatever the last
// `-Sy` left behind, which is older but never wrong about what is installed.
func BuildPendingPacman(hasCheckupdates bool) updates.Command {
	if hasCheckupdates {
		return updates.Command{
			Argv:        []string{"checkupdates"},
			Description: "List the pending updates against a private sync database",
		}
	}
	return updates.Command{
		Argv:        []string{"pacman", "-Qu"},
		Description: "List the pending updates from the sync database on disk",
	}
}

// BuildPendingAPT reads the upgradable list from the apt lists already
// fetched. `apt list` warns that its interface is unstable; the line shape it
// prints has not changed since apt 1.0 and is pinned by a parser test.
func BuildPendingAPT() updates.Command {
	return updates.Command{
		Argv:        []string{"apt", "list", "--upgradable"},
		Description: "List the upgradable packages from the apt lists on disk",
	}
}

// BuildPendingDNF reads the pending list. `dnf check-update` exits 100 when
// there are updates, 0 when there are none and something else on a real
// failure, which is why its exit code is interpreted rather than treated as
// success or error.
func BuildPendingDNF() updates.Command {
	return updates.Command{
		Argv:        []string{"dnf", "check-update", "-q", "--cacheonly"},
		Description: "List the pending updates from the dnf cache",
	}
}

// BuildInstalledVersionsDNF asks rpm what is installed today. dnf's own
// `check-update` prints only the version that would be installed, so the
// "current → new" column needs a second read; rpm answers it for every
// pending package in one call, from the local database.
func BuildInstalledVersionsDNF(names []string) (updates.Command, error) {
	if len(names) == 0 {
		return updates.Command{}, fmt.Errorf("pkgmgr: no package to query")
	}
	for _, name := range names {
		if err := checkPackageName(name); err != nil {
			return updates.Command{}, err
		}
	}
	argv := append([]string{
		"rpm", "-q", "--qf",
		`%{NAME}.%{ARCH}|%|EPOCH?{%{EPOCH}:}|%{VERSION}-%{RELEASE}` + "\n",
	}, names...)
	return updates.Command{
		Argv:        argv,
		Description: "Read the installed versions from the rpm database",
	}, nil
}

// BuildSizesDNF reads the download size of each pending update. check-update
// does not print one, and repoquery answers from the same cache.
func BuildSizesDNF() updates.Command {
	return updates.Command{
		Argv: []string{
			"dnf", "repoquery", "--upgrades", "--latest-limit", "1",
			"-q", "--cacheonly",
			"--qf", `%{name}.%{arch}|%{evr}|%{downloadsize}` + "\n",
		},
		Description: "Read the download size of each pending update",
	}
}

// BuildSecurityDNF lists the advisories that are security fixes.
func BuildSecurityDNF() updates.Command {
	return updates.Command{
		Argv:        []string{"dnf", "updateinfo", "list", "--security", "-q", "--cacheonly"},
		Description: "List the pending security advisories",
	}
}

// packageNameRe is the set of characters a package name may contain across
// the three managers. Package names come from the manager's own output and
// end up in an rpm argv, so they are validated first.
var packageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,127}$`)

// checkPackageName rejects a name that is not a plausible package name.
func checkPackageName(name string) error {
	if !packageNameRe.MatchString(name) {
		return fmt.Errorf("pkgmgr: %q is not a package name", name)
	}
	return nil
}

// BuildRefresh is the metadata refresh that has to happen before an upgrade.
// It is a privileged write to the manager's cache, so it is part of the
// previewed sequence rather than something the read path does behind the
// user's back.
//
// pacman has no separate refresh step: `-Syu` does it in the same
// transaction, so this returns false there.
func BuildRefresh(manager string) (updates.Command, bool) {
	switch manager {
	case updates.ManagerAPT:
		return updates.Command{
			Argv:        []string{"apt-get", "update"},
			Description: "Refresh the apt package lists",
		}, true
	case updates.ManagerDNF:
		return updates.Command{
			Argv:        []string{"dnf", "makecache", "--refresh", "-q"},
			Description: "Refresh the dnf metadata",
		}, true
	default:
		return updates.Command{}, false
	}
}

// BuildSimulate is the manager's own dry run: what it would do, in its own
// words, printed into the plan screen.
//
// pacman has none. `pacman -Syu --print` would refresh the real sync database
// to answer, which a read path must not do, so on pacman the pending list is
// the plan and this returns false. Named here so the gap is visible.
func BuildSimulate(manager, mode string) (updates.Command, bool) {
	switch manager {
	case updates.ManagerAPT:
		verb := "upgrade"
		if mode == updates.UpgradeDist {
			verb = "dist-upgrade"
		}
		return updates.Command{
			Argv:        []string{"apt-get", "-s", verb},
			Description: "Simulate the upgrade without changing anything",
		}, true
	case updates.ManagerDNF:
		return updates.Command{
			Argv:        []string{"dnf", "upgrade", "--assumeno", "--cacheonly"},
			Description: "Resolve the upgrade transaction without applying it",
		}, true
	default:
		return updates.Command{}, false
	}
}

// BuildUpgrade is the command that actually applies the updates.
//
// On Arch it is `pacman -Syu --noconfirm`, unless the machine carries
// Omarchy Server's own wrapper: that one runs the same pacman transaction and
// then classifies and restarts what changed, so driving pacman directly there
// would skip the half of the job the machine was set up to do. `--no-reboot`
// is passed because tui-update never reboots by itself; the reboot is offered
// as its own confirmed action.
func BuildUpgrade(manager, mode string, omarchy bool) (updates.Command, error) {
	switch manager {
	case updates.ManagerPacman:
		if omarchy {
			return updates.Command{
				Argv: []string{OmarchyUpdate, "run", "--no-reboot"},
				Description: "Upgrade every package through " + OmarchyUpdate +
					", which restarts what changed and never reboots on its own",
				Destructive: true,
			}, nil
		}
		return updates.Command{
			Argv:        []string{"pacman", "-Syu", "--noconfirm"},
			Description: "Synchronise the databases and upgrade every package",
			Destructive: true,
		}, nil
	case updates.ManagerAPT:
		verb := "upgrade"
		description := "Upgrade every package, adding and removing nothing"
		if mode == updates.UpgradeDist {
			verb = "dist-upgrade"
			description = "Upgrade every package, adding and removing what the " +
				"new dependencies need"
		}
		return updates.Command{
			Argv:        []string{"apt-get", "-y", verb},
			Description: description,
			Destructive: true,
		}, nil
	case updates.ManagerDNF:
		return updates.Command{
			Argv:        []string{"dnf", "-y", "upgrade"},
			Description: "Upgrade every package",
			Destructive: true,
		}, nil
	default:
		return updates.Command{}, fmt.Errorf("pkgmgr: unknown package manager %q", manager)
	}
}

// BuildSnapshot builds the pre or post snapshot of an upgrade.
//
// snapper's pre/post pair is what makes the snapshot useful rather than
// decorative: the two are linked, so `snapper status <pre>..<post>` lists
// exactly what the upgrade changed on disk. The pre snapshot is taken before
// the upgrade runs and the post one after it succeeds.
func BuildSnapshot(config, kind, description string) (updates.Command, error) {
	if !snapperConfigRe.MatchString(config) {
		return updates.Command{}, fmt.Errorf(
			"pkgmgr: %q is not a snapper configuration name", config)
	}
	if kind != "pre" && kind != "post" {
		return updates.Command{}, fmt.Errorf(
			"pkgmgr: a snapshot is pre or post, not %q", kind)
	}
	if strings.ContainsAny(description, "\n\r") {
		return updates.Command{}, fmt.Errorf(
			"pkgmgr: a snapshot description is one line")
	}
	return updates.Command{
		Argv: []string{
			"snapper", "create", "-c", config, "-t", kind,
			"-d", description, "--print-number",
		},
		Description: "Take a " + kind + "-upgrade snapshot of the " + config +
			" subvolume",
	}, nil
}

// BuildSnapperConfigs lists the snapper configurations, which is how the plan
// learns whether there is a root subvolume to snapshot.
func BuildSnapperConfigs() updates.Command {
	return updates.Command{
		Argv:        []string{"snapper", "list-configs"},
		Description: "List the snapper configurations",
	}
}

// BuildRestartProbe reads which services keep old code mapped, and whether a
// reboot is called for. Each manager has its own answer, and every one of
// them is a read.
//
//	apt   needrestart -b               a stable key/value report
//	dnf   needs-restarting -s          the standalone plugin binary
//	      needs-restarting -r          exit 1 when a reboot is needed
//	arch  omarchy-server-update-restart --dry-run --since-offset 0
//
// The Omarchy one is the only classifier on Arch that reads the process table
// instead of guessing from package names, and it refuses to run as anything
// but root — so it is reached through the escalation prefix, and its absence
// falls back to the name heuristic rather than failing.
func BuildRestartProbe(kind string) (updates.Command, error) {
	switch kind {
	case "needrestart":
		return updates.Command{
			Argv:        []string{"needrestart", "-b"},
			Description: "Report which services still map replaced files",
		}, nil
	case "needs-restarting-services":
		return updates.Command{
			Argv:        []string{"needs-restarting", "-s"},
			Description: "Report which services still map replaced files",
		}, nil
	case "needs-restarting-reboot":
		return updates.Command{
			Argv:        []string{"needs-restarting", "-r"},
			Description: "Report whether a reboot is needed",
		}, nil
	case "dnf-needs-restarting-services":
		return updates.Command{
			Argv:        []string{"dnf", "needs-restarting", "-s"},
			Description: "Report which services still map replaced files",
		}, nil
	case "omarchy":
		return updates.Command{
			Argv: []string{
				OmarchyRestart, "--dry-run", "--since-offset", "0",
			},
			Description: "Classify what the last transaction would restart, " +
				"restarting nothing",
		}, nil
	default:
		return updates.Command{}, fmt.Errorf("pkgmgr: unknown restart probe %q", kind)
	}
}

// BuildHistoryDNF lists the last transactions. pacman and apt keep theirs in
// a plain text log, read from the filesystem instead.
func BuildHistoryDNF() updates.Command {
	return updates.Command{
		Argv:        []string{"dnf", "history", "list", "-q"},
		Description: "List the last dnf transactions",
	}
}

// BuildTimerAction enables or disables an unattended-update unit.
//
// `--now` is deliberate on both sides: a user who turns unattended updates
// off means now, not at the next boot, and a timer left running after being
// disabled is the kind of surprise this tool exists to prevent.
func BuildTimerAction(action, unit string) (updates.Command, error) {
	if err := checkUnit(unit); err != nil {
		return updates.Command{}, err
	}
	switch action {
	case updates.TimerEnable:
		return updates.Command{
			Argv:        []string{"systemctl", "enable", "--now", unit},
			Description: "Enable " + unit + " and start it now",
		}, nil
	case updates.TimerDisable:
		return updates.Command{
			Argv:        []string{"systemctl", "disable", "--now", unit},
			Description: "Disable " + unit + " and stop it now",
			Destructive: true,
		}, nil
	default:
		return updates.Command{}, fmt.Errorf("pkgmgr: unknown timer action %q", action)
	}
}

// BuildTimerState reads a unit's enabled and active state. Two reads rather
// than one `systemctl show`, because both answer on a unit that does not
// exist and the words they print are the ones the screen displays.
func BuildTimerState(unit, what string) (updates.Command, error) {
	if err := checkUnit(unit); err != nil {
		return updates.Command{}, err
	}
	if what != "is-enabled" && what != "is-active" {
		return updates.Command{}, fmt.Errorf("pkgmgr: unknown unit query %q", what)
	}
	return updates.Command{
		Argv:        []string{"systemctl", what, unit},
		Description: "Read whether " + unit + " is " + strings.TrimPrefix(what, "is-"),
	}, nil
}

// BuildReboot is the reboot tui-update offers and never takes by itself. It
// is built like every other command, so the same confirm dialog stands in
// front of it.
func BuildReboot() (updates.Command, error) {
	return updates.Command{
		Argv:        []string{"systemctl", "reboot"},
		Description: "Reboot the machine now",
		Destructive: true,
	}, nil
}

// Package name patterns that decide a package's group, and with it whether an
// upgrade ends in a reboot.
//
// They are the fallback classifier: on a machine with needrestart,
// needs-restarting or the Omarchy restart pass, the real answer comes from
// whatever is mapped into the running processes. Here there is only the list
// of names, which is why the sets are the conservative ones — the packages
// whose replacement is never picked up by a running system.
var (
	// kernelNames are the kernels and the tooling that builds their boot
	// images, across the three distributions.
	kernelNames = []string{
		"kernel", "kernel-core", "kernel-modules", "kernel-modules-core",
		"kernel-modules-extra",
		"linux", "linux-lts", "linux-zen", "linux-hardened", "linux-rt",
		"linux-rt-lts",
		"linux-image-generic", "linux-image-amd64", "linux-image-arm64",
		"linux-generic", "linux-headers-generic",
		"mkinitcpio", "dracut", "booster", "initramfs-tools",
		"grub", "grub2-common", "limine", "systemd-boot",
	}
	// firmwareNames are firmware blobs and CPU microcode: loaded once, at
	// boot, and never re-read by a running kernel.
	firmwareNames = []string{
		"linux-firmware", "amd-ucode", "intel-ucode",
		"amd64-microcode", "intel-microcode", "microcode_ctl",
	}
	// coreNames are the pieces of userspace that every process holds open:
	// nothing running picks up a new one without being restarted, and PID 1
	// cannot always be restarted at all.
	coreNames = []string{
		"glibc", "libc6", "libc-bin", "glibc-common",
		"systemd", "systemd-libs", "systemd-sysv", "systemd-sysvcompat",
		"systemd-udev", "udev",
		"dbus", "dbus-broker", "dbus-daemon", "dbus-common",
		"openssl", "libssl3", "libssl1.1", "openssl-libs",
		"zlib", "zlib1g", "libgcc", "libstdc++6",
	}
)

// prefixGroups catch the families whose members are named by version:
// kernel-6.14.9, linux-image-6.8.0-51-generic, iwlwifi-firmware.
var prefixGroups = []struct {
	prefix string
	suffix string
	group  string
}{
	{prefix: "kernel-", group: updates.GroupKernel},
	{prefix: "linux-image-", group: updates.GroupKernel},
	{prefix: "linux-modules-", group: updates.GroupKernel},
	{prefix: "linux-headers-", group: updates.GroupKernel},
	{prefix: "linux-firmware-", group: updates.GroupFirmware},
	{suffix: "-firmware", group: updates.GroupFirmware},
	{suffix: "-ucode", group: updates.GroupFirmware},
	{suffix: "-microcode", group: updates.GroupFirmware},
}

// GroupFor classifies a package name. It is deliberately name-based and
// deliberately conservative: a package it does not recognise is "other",
// because over-reporting a reboot trains people to ignore the one that
// mattered.
func GroupFor(name string) string {
	lower := strings.ToLower(name)
	for _, known := range kernelNames {
		if lower == known {
			return updates.GroupKernel
		}
	}
	for _, known := range firmwareNames {
		if lower == known {
			return updates.GroupFirmware
		}
	}
	for _, known := range coreNames {
		if lower == known {
			return updates.GroupCore
		}
	}
	for _, rule := range prefixGroups {
		if rule.prefix != "" && strings.HasPrefix(lower, rule.prefix) {
			return rule.group
		}
		if rule.suffix != "" && strings.HasSuffix(lower, rule.suffix) {
			return rule.group
		}
	}
	return updates.GroupOther
}

// ClassifyFromPackages is the fallback restart classifier: what the pending
// list alone says about whether this upgrade ends in a reboot.
//
// It is used when nothing better answered — no needrestart, no
// needs-restarting, no Omarchy restart pass, or one of those that needed a
// privilege `sudo -n` could not grant. Source says which it was, so the screen
// can be honest about how the answer was reached.
func ClassifyFromPackages(packages []updates.Package) updates.Restart {
	var reasons []string
	for _, p := range packages {
		switch p.Group {
		case updates.GroupKernel, updates.GroupFirmware, updates.GroupCore:
			reasons = append(reasons, p.Name+" "+p.Transition())
		}
	}
	if len(reasons) == 0 {
		return updates.Restart{
			Class:  updates.RestartNone,
			Source: "the pending package list",
		}
	}
	return updates.Restart{
		Class:  updates.RestartReboot,
		Reason: strings.Join(reasons, "; "),
		Source: "the pending package list",
	}
}

// MergeRestart folds a probe's answer into the name-based classification.
//
// The probe wins on services, because it read the process table and the name
// list cannot. The reboot verdict is the union: a kernel in the pending list
// means a reboot whether or not the probe has noticed yet, and a probe that
// says a reboot is required means one whether or not the name list saw why.
func MergeRestart(fallback, probed updates.Restart) updates.Restart {
	merged := fallback
	if probed.Source != "" {
		merged.Source = probed.Source
	}
	if probed.Detail != "" {
		merged.Detail = probed.Detail
	}
	if len(probed.Services) > 0 {
		merged.Services = probed.Services
	}
	if probed.RebootRequired {
		merged.RebootRequired = true
		if probed.Reason != "" {
			merged.Reason = probed.Reason
		}
	}
	switch {
	case merged.RebootRequired || fallback.Class == updates.RestartReboot ||
		probed.Class == updates.RestartReboot:
		merged.Class = updates.RestartReboot
	case len(merged.Services) > 0:
		merged.Class = updates.RestartServices
	default:
		merged.Class = updates.RestartNone
	}
	return merged
}
