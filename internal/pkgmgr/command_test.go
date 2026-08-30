package pkgmgr

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/updates"
)

// TestArgvTable is the whole family contract in one place: the command line
// the confirm dialog shows is the command line that runs, so every argv this
// tool can build is pinned here by string equality.
//
// A change to any of these is a change to what a user reads before agreeing to
// it, and it should be as loud as changing a screen.
func TestArgvTable(t *testing.T) {
	tests := []struct {
		name string
		cmd  updates.Command
		want string
	}{
		// pacman
		{"pacman pending, checkupdates", BuildPendingPacman(true),
			"checkupdates"},
		{"pacman pending, no checkupdates", BuildPendingPacman(false),
			"pacman -Qu"},
		{"pacman upgrade", must(BuildUpgrade(updates.ManagerPacman,
			updates.UpgradeDefault, false)),
			"pacman -Syu --noconfirm"},
		{"pacman upgrade on omarchy", must(BuildUpgrade(updates.ManagerPacman,
			updates.UpgradeDefault, true)),
			"omarchy-server-update run --no-reboot"},
		{"omarchy restart probe", must(BuildRestartProbe("omarchy")),
			"omarchy-server-update-restart --dry-run --since-offset 0"},

		// apt
		{"apt pending", BuildPendingAPT(), "apt list --upgradable"},
		{"apt refresh", mustRefresh(t, updates.ManagerAPT), "apt-get update"},
		{"apt simulate", mustSimulate(t, updates.ManagerAPT,
			updates.UpgradeDefault), "apt-get -s upgrade"},
		{"apt simulate dist", mustSimulate(t, updates.ManagerAPT,
			updates.UpgradeDist), "apt-get -s dist-upgrade"},
		{"apt upgrade", must(BuildUpgrade(updates.ManagerAPT,
			updates.UpgradeDefault, false)), "apt-get -y upgrade"},
		{"apt dist-upgrade", must(BuildUpgrade(updates.ManagerAPT,
			updates.UpgradeDist, false)), "apt-get -y dist-upgrade"},
		{"needrestart", must(BuildRestartProbe("needrestart")),
			"needrestart -b"},

		// dnf
		{"dnf pending", BuildPendingDNF(),
			"dnf check-update -q --cacheonly"},
		{"dnf sizes", BuildSizesDNF(),
			"dnf repoquery --upgrades --latest-limit 1 -q --cacheonly " +
				"--qf %{name}.%{arch}|%{evr}|%{downloadsize}\n"},
		{"dnf security", BuildSecurityDNF(),
			"dnf updateinfo list --security -q --cacheonly"},
		{"dnf refresh", mustRefresh(t, updates.ManagerDNF),
			"dnf makecache --refresh -q"},
		{"dnf simulate", mustSimulate(t, updates.ManagerDNF,
			updates.UpgradeDefault), "dnf upgrade --assumeno --cacheonly"},
		{"dnf upgrade", must(BuildUpgrade(updates.ManagerDNF,
			updates.UpgradeDefault, false)), "dnf -y upgrade"},
		{"dnf history", BuildHistoryDNF(), "dnf history list -q"},
		{"needs-restarting services",
			must(BuildRestartProbe("needs-restarting-services")),
			"needs-restarting -s"},
		{"needs-restarting reboot",
			must(BuildRestartProbe("needs-restarting-reboot")),
			"needs-restarting -r"},

		// shared
		{"snapshot pre", must(BuildSnapshot("root", "pre", "before")),
			"snapper create -c root -t pre -d before --print-number"},
		{"snapshot post", must(BuildSnapshot("root", "post", "after")),
			"snapper create -c root -t post -d after --print-number"},
		{"snapper configs", BuildSnapperConfigs(), "snapper list-configs"},
		{"timer enable", must(BuildTimerAction(updates.TimerEnable,
			UnitDNFAutomatic)),
			"systemctl enable --now dnf-automatic.timer"},
		{"timer disable", must(BuildTimerAction(updates.TimerDisable,
			UnitAPTDaily)),
			"systemctl disable --now apt-daily-upgrade.timer"},
		{"timer state", must(BuildTimerState(UnitOmarchyTimer, "is-enabled")),
			"systemctl is-enabled omarchy-server-update.timer"},
		{"reboot", must(BuildReboot()), "systemctl reboot"},
	}
	for _, test := range tests {
		if got := test.cmd.String(); got != test.want {
			t.Errorf("%s:\n got %q\nwant %q", test.name, got, test.want)
		}
		if test.cmd.Description == "" {
			t.Errorf("%s: a command with no description cannot be confirmed",
				test.name)
		}
	}
}

// TestRPMQueryArgv pins the rpm format string separately: it is the one argv
// carrying a template, and the `%|EPOCH?{…}|` conditional is what keeps the
// installed version comparable with the one dnf prints.
func TestRPMQueryArgv(t *testing.T) {
	cmd, err := BuildInstalledVersionsDNF([]string{"glibc", "kernel-core"})
	if err != nil {
		t.Fatalf("BuildInstalledVersionsDNF: %v", err)
	}
	want := "rpm -q --qf %{NAME}.%{ARCH}|%|EPOCH?{%{EPOCH}:}|%{VERSION}-%{RELEASE}\n" +
		" glibc kernel-core"
	if got := cmd.String(); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// TestDestructiveCommands: every command that can change the machine must be
// marked, because that is what paints the confirm dialog in the danger colour.
func TestDestructiveCommands(t *testing.T) {
	for _, manager := range []string{
		updates.ManagerPacman, updates.ManagerAPT, updates.ManagerDNF,
	} {
		cmd := must(BuildUpgrade(manager, updates.UpgradeDefault, false))
		if !cmd.Destructive {
			t.Errorf("%s: an upgrade is a destructive change", manager)
		}
	}
	if !must(BuildReboot()).Destructive {
		t.Errorf("a reboot is a destructive change")
	}
	if !must(BuildTimerAction(updates.TimerDisable, UnitDNFAutomatic)).
		Destructive {
		t.Errorf("turning unattended updates off is a change worth confirming")
	}
	// A read never is.
	for _, cmd := range []updates.Command{
		BuildPendingAPT(), BuildPendingDNF(), BuildSnapperConfigs(),
		BuildHistoryDNF(), BuildPendingPacman(true),
	} {
		if cmd.Destructive {
			t.Errorf("%q is a read and must not be marked destructive",
				cmd.String())
		}
	}
}

// TestPacmanHasNoRefreshOrSimulation pins the two gaps the plan screen has to
// explain: pacman cannot be asked what it would do without first writing to
// the sync database.
func TestPacmanHasNoRefreshOrSimulation(t *testing.T) {
	if _, ok := BuildRefresh(updates.ManagerPacman); ok {
		t.Errorf("pacman refreshes inside -Syu; there is no separate step")
	}
	if _, ok := BuildSimulate(updates.ManagerPacman,
		updates.UpgradeDefault); ok {
		t.Errorf("pacman has no dry run that does not synchronise first")
	}
}

func TestBuildRejects(t *testing.T) {
	// The unit is the one argument of a timer action that ends up in an argv.
	for _, unit := range []string{
		"", "sshd", "sshd.socket", "a b.timer", "../../etc.timer",
		"x;reboot.service",
	} {
		if _, err := BuildTimerAction(updates.TimerEnable, unit); err == nil {
			t.Errorf("BuildTimerAction accepted %q", unit)
		}
	}
	if _, err := BuildTimerAction("restart", UnitDNFAutomatic); err == nil {
		t.Errorf("BuildTimerAction accepted an unknown action")
	}
	if _, err := BuildTimerState(UnitDNFAutomatic, "is-broken"); err == nil {
		t.Errorf("BuildTimerState accepted an unknown query")
	}

	// The snapper configuration and the snapshot kind, likewise.
	for _, config := range []string{"", "root subvol", "../home", "a/b"} {
		if _, err := BuildSnapshot(config, "pre", "x"); err == nil {
			t.Errorf("BuildSnapshot accepted config %q", config)
		}
	}
	if _, err := BuildSnapshot("root", "middle", "x"); err == nil {
		t.Errorf("BuildSnapshot accepted an unknown kind")
	}
	if _, err := BuildSnapshot("root", "pre", "one\ntwo"); err == nil {
		t.Errorf("BuildSnapshot accepted a multi-line description")
	}

	// Package names reach an rpm argv.
	for _, name := range []string{"", "glibc; reboot", "../etc", "a b"} {
		if _, err := BuildInstalledVersionsDNF([]string{name}); err == nil {
			t.Errorf("BuildInstalledVersionsDNF accepted %q", name)
		}
	}
	if _, err := BuildInstalledVersionsDNF(nil); err == nil {
		t.Errorf("BuildInstalledVersionsDNF accepted an empty list")
	}

	if _, err := BuildUpgrade("zypper", updates.UpgradeDefault, false); err == nil {
		t.Errorf("BuildUpgrade accepted an unsupported manager")
	}
	if _, err := BuildRestartProbe("guess"); err == nil {
		t.Errorf("BuildRestartProbe accepted an unknown probe")
	}
}

func TestGroupFor(t *testing.T) {
	tests := map[string]string{
		"linux":                          updates.GroupKernel,
		"kernel":                         updates.GroupKernel,
		"kernel-core":                    updates.GroupKernel,
		"linux-image-6.8.0-51-generic":   updates.GroupKernel,
		"linux-image-generic":            updates.GroupKernel,
		"mkinitcpio":                     updates.GroupKernel,
		"linux-firmware":                 updates.GroupFirmware,
		"iwlwifi-firmware":               updates.GroupFirmware,
		"amd-ucode":                      updates.GroupFirmware,
		"intel-microcode":                updates.GroupFirmware,
		"glibc":                          updates.GroupCore,
		"libc6":                          updates.GroupCore,
		"systemd":                        updates.GroupCore,
		"openssl":                        updates.GroupCore,
		"vim":                            updates.GroupOther,
		"nginx":                          updates.GroupOther,
		"linux-firmware-whence-whatever": updates.GroupFirmware,
	}
	for name, want := range tests {
		if got := GroupFor(name); got != want {
			t.Errorf("GroupFor(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSortPutsKernelFirst is what the pending screen promises: whatever else
// is in the list, the packages that decide whether this ends in a reboot are
// at the top.
func TestSortPutsKernelFirst(t *testing.T) {
	packages := []updates.Package{
		{Name: "vim", Group: updates.GroupOther},
		{Name: "glibc", Group: updates.GroupCore},
		{Name: "amd-ucode", Group: updates.GroupFirmware},
		{Name: "aardvark", Group: updates.GroupOther},
		{Name: "linux", Group: updates.GroupKernel},
	}
	updates.SortPackages(packages)
	want := []string{"linux", "amd-ucode", "glibc", "aardvark", "vim"}
	for i, name := range want {
		if packages[i].Name != name {
			t.Errorf("position %d = %q, want %q", i, packages[i].Name, name)
		}
	}
}

func TestClassifyFromPackages(t *testing.T) {
	none := ClassifyFromPackages([]updates.Package{
		{Name: "vim", Group: updates.GroupOther},
	})
	if none.Class != updates.RestartNone {
		t.Errorf("a vim upgrade classified as %q", none.Class)
	}
	reboot := ClassifyFromPackages([]updates.Package{
		{Name: "vim", Group: updates.GroupOther},
		{Name: "linux", Current: "6.16.1-1", New: "6.16.3-1",
			Group: updates.GroupKernel},
	})
	if reboot.Class != updates.RestartReboot {
		t.Errorf("a kernel upgrade classified as %q", reboot.Class)
	}
	if !strings.Contains(reboot.Reason, "linux 6.16.1-1 → 6.16.3-1") {
		t.Errorf("reason = %q, want the version change named", reboot.Reason)
	}
	if reboot.Source == "" {
		t.Errorf("a classification with no source cannot be judged by a reader")
	}
}

// TestMergeRestart pins how the two answers are combined: the probe read the
// process table so it owns the service list, and the reboot verdict is the
// union, because a kernel in the pending list means a reboot whether or not
// the probe has noticed yet.
func TestMergeRestart(t *testing.T) {
	fallback := updates.Restart{
		Class: updates.RestartReboot, Reason: "linux 1 → 2",
		Source: "the pending package list",
	}
	probed := updates.Restart{
		Class: updates.RestartServices, Services: []string{"sshd"},
		Source: "needrestart -b",
	}
	merged := MergeRestart(fallback, probed)
	if merged.Class != updates.RestartReboot {
		t.Errorf("class = %q, want the more severe of the two", merged.Class)
	}
	if len(merged.Services) != 1 || merged.Services[0] != "sshd" {
		t.Errorf("services = %v, want the probe's", merged.Services)
	}
	if merged.Source != "needrestart -b" {
		t.Errorf("source = %q, want the probe's", merged.Source)
	}
	if merged.Reason != "linux 1 → 2" {
		t.Errorf("reason = %q, want the fallback's kept", merged.Reason)
	}

	// A probe that found a reboot wins over a quiet package list.
	merged = MergeRestart(
		updates.Restart{Class: updates.RestartNone},
		updates.Restart{Class: updates.RestartReboot, RebootRequired: true,
			Reason: "/var/run/reboot-required"},
	)
	if merged.Class != updates.RestartReboot || !merged.RebootRequired {
		t.Errorf("merged = %+v", merged)
	}

	// Two quiet answers stay quiet.
	merged = MergeRestart(updates.Restart{Class: updates.RestartNone},
		updates.Restart{Class: updates.RestartNone})
	if merged.Class != updates.RestartNone {
		t.Errorf("class = %q, want none", merged.Class)
	}
}

func TestCapabilitiesPerManager(t *testing.T) {
	if !CapabilitiesFor(updates.ManagerAPT).DistUpgrade {
		t.Errorf("apt is the manager that distinguishes a dist-upgrade")
	}
	if CapabilitiesFor(updates.ManagerDNF).DistUpgrade {
		t.Errorf("dnf has one kind of upgrade")
	}
	if CapabilitiesFor(updates.ManagerPacman).SecurityMetadata {
		t.Errorf("pacman publishes no security metadata")
	}
	if !CapabilitiesFor(updates.ManagerDNF).PerPackageSize {
		t.Errorf("dnf's repoquery reports a size per package")
	}
	if CapabilitiesFor(updates.ManagerAPT).PerPackageSize {
		t.Errorf("apt reports no per-package size")
	}
}

// TestUnitsPerManager pins which unattended-update unit each machine is asked
// about, since enabling one is a change to how the machine behaves when
// nobody is watching.
func TestUnitsPerManager(t *testing.T) {
	if units := unitsFor(updates.ManagerPacman, false); len(units) != 0 {
		t.Errorf("plain Arch has no unattended-update unit: %v", units)
	}
	if units := unitsFor(updates.ManagerPacman, true); len(units) != 1 ||
		units[0].Unit != UnitOmarchyTimer {
		t.Errorf("omarchy units = %v", units)
	}
	if units := unitsFor(updates.ManagerAPT, false); len(units) != 2 {
		t.Errorf("apt units = %v, want the timer and the service", units)
	}
	if units := unitsFor(updates.ManagerDNF, false); len(units) != 1 ||
		units[0].Unit != UnitDNFAutomatic {
		t.Errorf("dnf units = %v", units)
	}
}

func TestSnapshotForBuildsBothHalves(t *testing.T) {
	snapshot := snapshotFor(SnapperRootConfig, updates.ManagerDNF)
	if !snapshot.Available || snapshot.Config != "root" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !strings.Contains(snapshot.Pre.String(), "-t pre") ||
		!strings.Contains(snapshot.Post.String(), "-t post") {
		t.Errorf("pre/post = %q, %q", snapshot.Pre, snapshot.Post)
	}
	if snapshot.Reason == "" {
		t.Errorf("a snapshot decision with no reason cannot be read")
	}
}

// must unwraps a builder that cannot fail on the arguments this table passes
// it. It panics rather than taking a *testing.T, so a whole multi-value call
// fits in one table entry.
func must(cmd updates.Command, err error) updates.Command {
	if err != nil {
		panic("building a command: " + err.Error())
	}
	return cmd
}

// mustRefresh and mustSimulate unwrap the two builders that report absence
// with a boolean rather than an error.
func mustRefresh(t *testing.T, manager string) updates.Command {
	t.Helper()
	cmd, ok := BuildRefresh(manager)
	if !ok {
		t.Fatalf("%s has no refresh step", manager)
	}
	return cmd
}

func mustSimulate(t *testing.T, manager, mode string) updates.Command {
	t.Helper()
	cmd, ok := BuildSimulate(manager, mode)
	if !ok {
		t.Fatalf("%s has no simulation", manager)
	}
	return cmd
}
