package pkgmgr

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// demoManager is the manager the default sample machine runs. dnf is the one
// with the most to show — security advisories, per-package sizes, a real
// transaction history — so it is the one plain --demo drives.
const demoManager = updates.ManagerDNF

// The sample machines --demo can drive, named the way --demo-machine takes
// them. Each one builds and previews its commands exactly as the real backend
// would on that kind of machine, so the behaviour a manager has that the
// others do not — apt's non-interactive upgrade, Omarchy's updater handed the
// terminal — is demonstrable too.
const (
	// DemoFedora is the default: dnf on Fedora.
	DemoFedora = "fedora"
	// DemoUbuntu is apt on Ubuntu, with needrestart installed.
	DemoUbuntu = "ubuntu"
	// DemoOmarchy is pacman on Omarchy Server, whose upgrade is
	// omarchy-server-update run as a hand-off.
	DemoOmarchy = "omarchy"
)

// DemoMachines lists the sample machines, the default first.
func DemoMachines() []string { return []string{DemoFedora, DemoUbuntu, DemoOmarchy} }

// Fake is an in-memory package manager. It backs --demo and the tests: every
// key works, every command is built and previewed exactly as the real backend
// builds it, and nothing reaches the system.
//
// The commands are recorded rather than run, and a hook applies to the
// in-memory model the change the real command would have made — so applying
// the upgrade in --demo really does empty the pending list, and the argv the
// confirm dialog displayed is the argv a test can assert on.
type Fake struct {
	model updates.Model
	run   *runner.Fake
	// machine is one of the Demo* names; manager and omarchy follow from it.
	machine string
	manager string
	omarchy bool
	// versionlock reports that the sample machine carries the dnf plugin that
	// pins a package at a version. It is a field rather than a constant so the
	// machine without it — where holding a package is refused with the name of
	// the package to install — is as demonstrable as the one with it.
	versionlock bool
	// handedOff are the hand-off steps that ran, in order, which is what a
	// test of the hand-off path asserts on.
	handedOff []updates.Command
}

// NewFake builds the sample machine: fourteen pending updates including a
// kernel and an openssl security fix, a reboot already required, two services
// holding old code open, a snapper root configuration to snapshot into, one
// package already held, and the dnf versionlock plugin installed.
func NewFake() *Fake { return newFake(DemoFedora, true) }

// NewFakeWithoutVersionlock is the same sample machine with the dnf
// versionlock plugin missing, which is what `--demo-no-versionlock` drives.
// Every other key behaves identically; holding a package is refused, naming
// the package that would fix it.
func NewFakeWithoutVersionlock() *Fake { return newFake(DemoFedora, false) }

// NewFakeMachine builds one of the DemoMachines by name.
func NewFakeMachine(machine string) (*Fake, error) {
	for _, known := range DemoMachines() {
		if machine == known {
			return newFake(machine, true), nil
		}
	}
	return nil, fmt.Errorf("pkgmgr: no sample machine named %q (have %s)",
		machine, strings.Join(DemoMachines(), ", "))
}

// newFake builds a sample machine in one of its two hold configurations.
func newFake(machine string, versionlock bool) *Fake {
	f := &Fake{machine: machine, versionlock: versionlock}
	switch machine {
	case DemoUbuntu:
		f.manager = updates.ManagerAPT
	case DemoOmarchy:
		f.manager, f.omarchy = updates.ManagerPacman, true
	default:
		f.machine, f.manager = DemoFedora, demoManager
	}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	f.reset()
	return f
}

// reset builds the sample state. It is a function rather than a literal so
// --demo starts from the same machine every time, however it was left.
func (f *Fake) reset() {
	switch f.machine {
	case DemoUbuntu:
		f.resetUbuntu()
	case DemoOmarchy:
		f.resetOmarchy()
	default:
		f.resetFedora()
	}
}

// resetFedora builds the default sample machine: dnf on Fedora.
func (f *Fake) resetFedora() {
	pending := []updates.Package{
		{
			Name: "kernel", Arch: "x86_64",
			Current: "6.14.7-300.fc42", New: "6.14.9-300.fc42",
			Repo: "updates", Size: "62.4 MiB",
			Security: true, SecurityRef: "FEDORA-2026-7c5d8e0a11 (Moderate)",
		},
		{
			Name: "kernel-core", Arch: "x86_64",
			Current: "6.14.7-300.fc42", New: "6.14.9-300.fc42",
			Repo: "updates", Size: "17.1 MiB",
		},
		{
			Name: "kernel-modules", Arch: "x86_64",
			Current: "6.14.7-300.fc42", New: "6.14.9-300.fc42",
			Repo: "updates", Size: "58.9 MiB",
		},
		{
			Name: "linux-firmware", Arch: "noarch",
			Current: "20260812-1.fc42", New: "20260826-1.fc42",
			Repo: "updates", Size: "412.7 MiB",
		},
		{
			Name: "microcode_ctl", Arch: "x86_64",
			Current: "2:2.1-63.1.fc42", New: "2:2.1-64.fc42",
			Repo: "updates", Size: "3.9 MiB",
		},
		{
			Name: "glibc", Arch: "x86_64",
			Current: "2.41-4.fc42", New: "2.41-5.fc42",
			Repo: "updates", Size: "2.2 MiB",
		},
		{
			Name: "systemd", Arch: "x86_64",
			Current: "257.7-1.fc42", New: "257.8-1.fc42",
			Repo: "updates", Size: "11.4 MiB",
		},
		{
			Name: "openssl", Arch: "x86_64",
			Current: "3.2.4-2.fc42", New: "3.2.4-3.fc42",
			Repo: "updates", Size: "1.1 MiB",
			Security: true, SecurityRef: "FEDORA-2026-9a1f2b3c4d (Important)",
		},
		{
			Name: "openssl-libs", Arch: "x86_64",
			Current: "3.2.4-2.fc42", New: "3.2.4-3.fc42",
			Repo: "updates", Size: "2.4 MiB",
			Security: true, SecurityRef: "FEDORA-2026-9a1f2b3c4d (Important)",
		},
		{
			Name: "nginx", Arch: "x86_64",
			Current: "1.27.4-1.fc42", New: "1.27.5-1.fc42",
			Repo: "updates", Size: "1.6 MiB",
			// Already held, so the sample machine shows both halves of the
			// hold key: H lifts this one and places one on any other row.
			Held: true,
		},
		{
			Name: "openssh-server", Arch: "x86_64",
			Current: "9.9p2-3.fc42", New: "9.9p2-4.fc42",
			Repo: "updates", Size: "465.0 KiB",
		},
		{
			Name: "curl", Arch: "x86_64",
			Current: "8.11.1-4.fc42", New: "8.11.1-5.fc42",
			Repo: "updates", Size: "310.2 KiB",
		},
		{
			Name: "git-core", Arch: "x86_64",
			Current: "2.49.0-1.fc42", New: "2.49.1-1.fc42",
			Repo: "updates", Size: "4.8 MiB",
		},
		{
			Name: "vim-minimal", Arch: "x86_64",
			Current: "2:9.1.1200-1.fc42", New: "2:9.1.1450-1.fc42",
			Repo: "updates", Size: "788.1 KiB",
		},
	}
	pending, security := groupPending(pending)
	f.model = updates.Model{
		Manager:       demoManager,
		Distro:        "fedora",
		Pending:       pending,
		SecurityCount: security,
		Restart: updates.Restart{
			Class:          updates.RestartReboot,
			Services:       []string{"sshd", "nginx"},
			Reason:         "Reboot is required to fully utilize these updates.",
			RebootRequired: true,
			Source:         "needs-restarting",
		},
		Snapshot: snapshotFor(SnapperRootConfig, demoManager),
		Timers: []updates.Timer{{
			Unit:        UnitDNFAutomatic,
			Present:     true,
			Enabled:     false,
			Active:      false,
			State:       "disabled",
			Description: "dnf's automatic upgrade timer",
		}},
		Hold: f.holdSupport(),
		Notes: []string{
			"this is the sample machine: nothing here touches your system",
		},
	}
}

// groupPending classifies, sorts and counts a sample pending list.
func groupPending(pending []updates.Package) ([]updates.Package, int) {
	for i := range pending {
		pending[i].Group = GroupFor(pending[i].Name)
	}
	updates.SortPackages(pending)
	security := 0
	for _, p := range pending {
		if p.Security {
			security++
		}
	}
	return pending, security
}

// sampleTimers is the manager's unattended-update units, all disabled, the
// state a freshly installed server is usually found in.
func sampleTimers(manager string, omarchy bool) []updates.Timer {
	timers := unitsFor(manager, omarchy)
	for i := range timers {
		timers[i].Present, timers[i].State = true, "disabled"
	}
	return timers
}

// resetUbuntu builds the apt sample machine: Ubuntu with needrestart, the
// machine an upgrade used to fail on when a package asked a question.
func (f *Fake) resetUbuntu() {
	pending, security := groupPending([]updates.Package{
		{
			Name: "linux-image-generic", Arch: "amd64",
			Current: "6.8.0-84.84", New: "6.8.0-85.85",
			Repo: "noble-updates",
		},
		{
			Name: "linux-firmware", Arch: "amd64",
			Current: "20240318.git3b128b60-0ubuntu2.17",
			New:     "20240318.git3b128b60-0ubuntu2.18", Repo: "noble-updates",
		},
		{
			Name: "libc6", Arch: "amd64",
			Current: "2.39-0ubuntu8.5", New: "2.39-0ubuntu8.6",
			Repo: "noble-security", Security: true, SecurityRef: "noble-security",
		},
		{
			Name: "systemd", Arch: "amd64",
			Current: "255.4-1ubuntu8.10", New: "255.4-1ubuntu8.11",
			Repo: "noble-updates",
		},
		{
			Name: "openssl", Arch: "amd64",
			Current: "3.0.13-0ubuntu3.5", New: "3.0.13-0ubuntu3.6",
			Repo: "noble-security", Security: true, SecurityRef: "noble-security",
		},
		{
			Name: "openssh-server", Arch: "amd64",
			Current: "1:9.6p1-3ubuntu13.13", New: "1:9.6p1-3ubuntu13.14",
			Repo: "noble-updates",
		},
		{
			// A package whose config file this machine has changed: the one
			// dpkg would have stopped to ask about.
			Name: "chrony", Arch: "amd64",
			Current: "4.5-1ubuntu4.1", New: "4.5-1ubuntu4.2",
			Repo: "noble-updates",
		},
		{
			Name: "nginx", Arch: "amd64",
			Current: "1.24.0-2ubuntu7.4", New: "1.24.0-2ubuntu7.5",
			Repo: "noble-updates", Held: true,
		},
		{
			Name: "curl", Arch: "amd64",
			Current: "8.5.0-2ubuntu10.6", New: "8.5.0-2ubuntu10.7",
			Repo: "noble-security", Security: true, SecurityRef: "noble-security",
		},
	})
	f.model = updates.Model{
		Manager:       updates.ManagerAPT,
		Distro:        "ubuntu",
		Pending:       pending,
		SecurityCount: security,
		Restart: updates.Restart{
			Class:    updates.RestartReboot,
			Services: []string{"ssh.service", "chrony.service"},
			Reason:   "linux-image-generic 6.8.0-84.84 → 6.8.0-85.85",
			Source:   "needrestart",
		},
		Snapshot: snapshotFor(SnapperRootConfig, updates.ManagerAPT),
		Timers:   sampleTimers(updates.ManagerAPT, false),
		Hold: updates.HoldSupport{
			Available: true,
			Reason:    holdReason(updates.ManagerAPT),
		},
		Notes: []string{
			"this is the sample machine: nothing here touches your system",
		},
	}
}

// resetOmarchy builds the pacman sample machine: Omarchy Server, whose
// upgrade is its own updater, run with the terminal handed over.
func (f *Fake) resetOmarchy() {
	pending, security := groupPending([]updates.Package{
		{Name: "linux", Current: "6.16.7.arch1-1", New: "6.16.8.arch1-1", Repo: "core"},
		{Name: "linux-firmware", Current: "20250808-1", New: "20250917-1", Repo: "core"},
		{Name: "intel-ucode", Current: "20250812-1", New: "20250912-1", Repo: "extra"},
		{Name: "glibc", Current: "2.42+r3+gbc13db73937-1", New: "2.42+r17+g2f4a8c1b1a6-1", Repo: "core"},
		{Name: "systemd", Current: "257.8-2", New: "257.9-1", Repo: "core"},
		{Name: "openssl", Current: "3.5.2-1", New: "3.5.3-1", Repo: "core"},
		{Name: "openssh", Current: "10.0p1-4", New: "10.1p1-1", Repo: "core"},
		{Name: "curl", Current: "8.15.0-1", New: "8.16.0-1", Repo: "core"},
		{Name: "neovim", Current: "0.11.3-1", New: "0.11.4-1", Repo: "extra"},
	})
	f.model = updates.Model{
		Manager:       updates.ManagerPacman,
		Distro:        "arch",
		Pending:       pending,
		SecurityCount: security,
		Restart: updates.Restart{
			Class:    updates.RestartReboot,
			Services: []string{"sshd.service"},
			Reason:   "linux 6.16.7.arch1-1 → 6.16.8.arch1-1",
			Source:   OmarchyRestart,
		},
		Snapshot: snapshotFor(SnapperRootConfig, updates.ManagerPacman),
		Timers:   sampleTimers(updates.ManagerPacman, true),
		Hold: updates.HoldSupport{
			Reason: updates.ManagerPacman + " has no command that pins a " +
				"package at a version; on Arch that is the IgnorePkg line of " +
				"/etc/pacman.conf, which is a file to edit rather than a " +
				"command to run",
		},
		Notes: []string{
			"this is the sample machine: nothing here touches your system",
			OmarchyUpdate + " is installed, so an upgrade runs through it " +
				"and it classifies what changed",
			updates.ManagerPacman + " publishes no security metadata, so no " +
				"update here can be marked as a security fix",
		},
	}
}

// holdSupport is the sample machine's answer about pinning a package, in both
// of its configurations. The unavailable one carries the same hint the real
// backend builds, so the refusal a reader sees in --demo is the refusal they
// would see on their own Fedora box.
func (f *Fake) holdSupport() updates.HoldSupport {
	if f.versionlock {
		return updates.HoldSupport{
			Available: true,
			Reason:    holdReason(demoManager),
		}
	}
	return updates.HoldSupport{
		Reason: "the dnf versionlock plugin is not installed, so this " +
			"machine has no way to pin a package at a version",
		Hint: "install " + VersionlockPackage("fedora"),
	}
}

// demoHistory is the sample machine's transaction log.
func (f *Fake) demoHistory() []updates.Transaction {
	switch f.machine {
	case DemoUbuntu:
		return []updates.Transaction{
			{When: "2026-09-18 06:14:22", Command: "apt-get -y upgrade", Detail: "Upgrade: 23 package(s)"},
			{When: "2026-09-11 09:02:48", Command: "apt-get install nginx", Detail: "Install: 4 package(s)"},
			{When: "2026-09-04 06:21:07", Command: "apt-get -y upgrade", Detail: "Upgrade: 11 package(s)"},
		}
	case DemoOmarchy:
		return []updates.Transaction{
			{When: "2026-09-19 07:40:11", Command: "pacman -Syu", Detail: "31 upgraded"},
			{When: "2026-09-12 07:38:59", Command: "pacman -Syu", Detail: "12 upgraded"},
			{When: "2026-09-05 08:02:31", Command: "pacman -S tui-update", Detail: "1 installed"},
		}
	}
	return []updates.Transaction{
		{ID: "143", When: "2026-08-24 06:12:03", Command: "dnf -y upgrade", Detail: "37 altered"},
		{ID: "142", When: "2026-08-21 09:41:10", Command: "dnf install nginx", Detail: "6 altered"},
		{ID: "141", When: "2026-08-17 07:02:55", Command: "dnf -y upgrade", Detail: "12 altered"},
		{ID: "140", When: "2026-08-11 06:33:12", Command: "dnf remove podman", Detail: "3 altered"},
		{ID: "139", When: "2026-08-04 06:15:41", Command: "dnf -y upgrade", Detail: "58 altered"},
	}
}

// Name identifies the backend. It is the real backend's name, because --demo
// shows what the real one would show.
func (f *Fake) Name() string { return f.manager }

// Describe says plainly that nothing here is real.
func (f *Fake) Describe() string {
	describe := "demo (in-memory sample machine, " + f.manager + ")"
	if f.omarchy {
		describe += "  ·  " + OmarchyUpdate
	}
	return describe
}

// Capabilities reports the same capabilities as the real backend.
func (f *Fake) Capabilities() updates.Capabilities {
	return CapabilitiesFor(f.manager)
}

// Preview renders the command line the real backend would run: a hand-off
// without `-n` on the prefix, as Real.Preview renders it.
func (f *Fake) Preview(cmd updates.Command) string {
	if IsHandOff(cmd) {
		fake := *f.run
		fake.Prefix = runner.Join(handOffPrefix(strings.Fields(f.run.Prefix)))
		return fake.Preview(cmd)
	}
	return f.run.Preview(cmd)
}

// Load returns the sample machine.
func (f *Fake) Load(_ context.Context) (updates.Model, error) { return f.model, nil }

// History returns the sample machine's transactions.
func (f *Fake) History(_ context.Context) ([]updates.Transaction, error) {
	return f.demoHistory(), nil
}

// Plan assembles the same sequence the real backend would, against the sample
// machine.
func (f *Fake) Plan(_ context.Context, opts updates.PlanOptions) (updates.Plan, error) {
	if err := planAllowed(opts.Mode, len(f.model.Pending),
		f.model.SecurityCount); err != nil {
		return updates.Plan{}, err
	}
	built := assemblePlan(f.manager, opts, f.model, f.omarchy)
	if built.Error != nil {
		return updates.Plan{}, built.Error
	}
	plan := built.Plan
	plan.DryRun = pendingAsText(f.demoDryRun(opts.Mode))
	return withPlanNotes(plan, opts, f.model), nil
}

// demoDryRun is the package list the sample machine's simulation would print:
// the security fixes alone in the security mode, everything otherwise. It is
// what makes the mode key visibly change the plan in --demo rather than only
// changing one word of the title.
func (f *Fake) demoDryRun(mode string) []updates.Package {
	if mode != updates.UpgradeSecurity {
		return f.model.Pending
	}
	return f.model.Security()
}

// Run records the command and applies its effect to the sample machine.
func (f *Fake) Run(ctx context.Context, cmd updates.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Ran exposes the recorded commands, which is what a test asserts on. A
// hand-off that ran is in it too, in its place in the sequence.
func (f *Fake) Ran() []updates.Command { return f.run.Ran }

// HandedOff exposes the hand-off steps that ran.
func (f *Fake) HandedOff() []updates.Command { return f.handedOff }

// HandOff prepares a hand-off on the sample machine. Run says, on the
// terminal it was given, what would have run there and waits for Enter, so
// --demo shows the screen being handed over and coming back; then it applies
// the command to the sample machine like any other.
func (f *Fake) HandOff(cmd updates.Command) (updates.Process, error) {
	if !IsHandOff(cmd) {
		return nil, fmt.Errorf("pkgmgr: %s is a runner step, not a hand-off",
			firstArg(cmd))
	}
	return &fakeProcess{fake: f, cmd: cmd}, nil
}

// fakeProcess is a hand-off on the sample machine.
type fakeProcess struct {
	fake   *Fake
	cmd    updates.Command
	stdin  io.Reader
	stdout io.Writer
}

// Run tells the handed-over terminal what the real hand-off would run, waits
// for Enter when there is a terminal to read it from, and applies the change.
func (p *fakeProcess) Run() error {
	if p.stdout != nil {
		_, _ = fmt.Fprintf(p.stdout, "\n  tui-update --demo handed the terminal "+
			"over for:\n\n    $ %s\n\n  On a real machine it runs here and asks "+
			"its questions on this terminal.\n  Press Enter to return to "+
			"tui-update. ", p.fake.Preview(p.cmd))
	}
	if p.stdin != nil {
		_, _ = bufio.NewReader(p.stdin).ReadString('\n')
	}
	p.fake.handedOff = append(p.fake.handedOff, p.cmd)
	_, err := p.fake.run.Run(context.Background(), p.cmd)
	return err
}

// SetStdin keeps the terminal's input, to wait for Enter on.
func (p *fakeProcess) SetStdin(r io.Reader) { p.stdin = r }

// SetStdout keeps the terminal's output, to say what would have run.
func (p *fakeProcess) SetStdout(w io.Writer) { p.stdout = w }

// SetStderr is not needed: the sample hand-off writes only to stdout.
func (p *fakeProcess) SetStderr(io.Writer) {}

// apply is the hook the fake runner calls: it makes to the in-memory machine
// the change the real command would have made, so the demo stays coherent as
// keys are pressed.
func (f *Fake) apply(cmd updates.Command) (string, error) {
	argv := cmd.Argv
	if len(argv) < 2 {
		return "ok", nil
	}
	switch {
	case argv[0] == "dnf" && argv[1] == "-y",
		argv[0] == "apt-get" && argv[1] == "-y",
		argv[0] == "pacman" && argv[1] == "-Syu",
		argv[0] == OmarchyUpdate && argv[1] == "run":
		return f.applyUpgrade(argv), nil
	case argv[0] == "apt-get" && argv[1] == "update":
		return "Reading package lists... Done", nil
	case argv[0] == "dnf" && argv[1] == "versionlock":
		return f.applyVersionlock(argv)
	case argv[0] == "dnf" && argv[1] == "makecache":
		return "Metadata cache created.", nil
	case argv[0] == "snapper" && argv[1] == "create":
		return f.applySnapshot(argv)
	case argv[0] == "systemctl":
		return f.applyTimer(argv)
	default:
		return "ok", nil
	}
}

// demoPreNumber is the number the sample machine's pre snapshot prints, and
// the post one the next.
const demoPreNumber = "42"

// applySnapshot answers a snapper create the way snapper does: a post snapshot
// without a real pre number is refused with snapper's own message, so a
// sequence that forgot to carry the number fails here as it would on a
// machine, not only in the lab.
func (f *Fake) applySnapshot(argv []string) (string, error) {
	for i, arg := range argv {
		if arg != "--pre-number" {
			continue
		}
		if i+1 >= len(argv) || argv[i+1] != demoPreNumber {
			return "Missing or invalid pre-number.",
				fmt.Errorf("snapper: Missing or invalid pre-number")
		}
		return "43", nil
	}
	for i, arg := range argv {
		if arg == "-t" && i+1 < len(argv) && argv[i+1] == "post" {
			return "Missing or invalid pre-number.",
				fmt.Errorf("snapper: Missing or invalid pre-number")
		}
	}
	return demoPreNumber, nil
}

// applyUpgrade empties the pending list, or only its security half when the
// argv carried --security. A held package survives either one: that is what
// being held means, and it is what makes the hold key's effect visible in the
// demo rather than only in the confirm dialog.
func (f *Fake) applyUpgrade(argv []string) string {
	securityOnly := false
	for _, arg := range argv {
		if arg == dnfSecurity {
			securityOnly = true
		}
	}
	var kept []updates.Package
	upgraded := 0
	for _, p := range f.model.Pending {
		if p.Held || (securityOnly && !p.Security) {
			kept = append(kept, p)
			continue
		}
		upgraded++
	}
	f.model.Pending = kept
	f.model.SecurityCount = 0
	for _, p := range kept {
		if p.Security {
			f.model.SecurityCount++
		}
	}
	if len(kept) == 0 {
		f.model.Restart.Services = nil
	}
	return fmt.Sprintf("Upgraded %d packages. Complete!", upgraded)
}

// applyVersionlock places or lifts a lock on the sample machine, refusing the
// whole subcommand the way a dnf without the plugin would.
func (f *Fake) applyVersionlock(argv []string) (string, error) {
	if !f.versionlock {
		//nolint:staticcheck // ST1005: this is dnf's own message, quoted
		return "", fmt.Errorf("No such command: versionlock. " +
			"Please use /usr/bin/dnf --help")
	}
	if len(argv) < 4 {
		return "ok", nil
	}
	action, name := argv[2], argv[3]
	for i := range f.model.Pending {
		if f.model.Pending[i].Name != name {
			continue
		}
		f.model.Pending[i].Held = action == "add"
		return "", nil
	}
	return "", fmt.Errorf("pkgmgr: %s is not in the pending list", name)
}

// applyTimer moves a timer to a new state, refusing a unit the sample machine
// does not have the way systemctl would.
func (f *Fake) applyTimer(argv []string) (string, error) {
	if len(argv) < 4 {
		return "ok", nil
	}
	unit := argv[len(argv)-1]
	for i := range f.model.Timers {
		timer := &f.model.Timers[i]
		if timer.Unit != unit {
			continue
		}
		timer.Enabled = argv[1] == "enable"
		timer.Active = timer.Enabled
		timer.State = "disabled"
		if timer.Enabled {
			timer.State = "enabled"
		}
		return "", nil
	}
	//nolint:staticcheck // ST1005: this is systemctl's own message, quoted
	return "", fmt.Errorf("Failed to %s unit: Unit %s does not exist",
		argv[1], unit)
}

// BuildTimerAction enables or disables an unattended-update unit.
func (f *Fake) BuildTimerAction(action, unit string) (updates.Command, error) {
	return BuildTimerAction(action, unit)
}

// BuildHold pins a package at its installed version on the sample machine, or
// refuses exactly as the real backend would when the plugin is missing.
func (f *Fake) BuildHold(action, name string) (updates.Command, error) {
	if err := holdRefusal(f.model.Hold); err != nil {
		return updates.Command{}, err
	}
	return BuildHold(f.manager, action, name)
}

// BuildReboot is the reboot the tool offers and never takes by itself.
func (f *Fake) BuildReboot() (updates.Command, error) { return BuildReboot() }

// DemoPreviewAll renders every command of a plan, for a test that wants the
// whole sequence as one string.
func DemoPreviewAll(f *Fake, plan updates.Plan) string {
	previews := make([]string, 0, len(plan.Commands))
	for _, cmd := range plan.Commands {
		previews = append(previews, f.Preview(cmd))
	}
	return strings.Join(previews, "\n")
}
