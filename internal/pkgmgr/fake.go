package pkgmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// demoManager is the manager the sample machine runs. dnf is the one with the
// most to show — security advisories, per-package sizes, a real transaction
// history — so it is the one --demo drives.
const demoManager = updates.ManagerDNF

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
}

// NewFake builds the sample machine: fourteen pending updates including a
// kernel and an openssl security fix, a reboot already required, two services
// holding old code open, and a snapper root configuration to snapshot into.
func NewFake() *Fake {
	f := &Fake{}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	f.reset()
	return f
}

// reset builds the sample state. It is a function rather than a literal so
// --demo starts from the same machine every time, however it was left.
func (f *Fake) reset() {
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
		Notes: []string{
			"this is the sample machine: nothing here touches your system",
		},
	}
}

// demoHistory is the sample machine's transaction log.
func demoHistory() []updates.Transaction {
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
func (f *Fake) Name() string { return demoManager }

// Describe says plainly that nothing here is real.
func (f *Fake) Describe() string {
	return "demo (in-memory sample machine, " + demoManager + ")"
}

// Capabilities reports the same capabilities as the real backend.
func (f *Fake) Capabilities() updates.Capabilities {
	return CapabilitiesFor(demoManager)
}

// Preview renders the command line the real backend would run.
func (f *Fake) Preview(cmd updates.Command) string { return f.run.Preview(cmd) }

// Load returns the sample machine.
func (f *Fake) Load(_ context.Context) (updates.Model, error) { return f.model, nil }

// History returns the sample machine's transactions.
func (f *Fake) History(_ context.Context) ([]updates.Transaction, error) {
	return demoHistory(), nil
}

// Plan assembles the same sequence the real backend would, against the sample
// machine.
func (f *Fake) Plan(_ context.Context, mode string) (updates.Plan, error) {
	if len(f.model.Pending) == 0 {
		return updates.Plan{}, fmt.Errorf("pkgmgr: there is nothing to upgrade")
	}
	plan := updates.Plan{
		Title: planTitle(demoManager, mode, len(f.model.Pending),
			f.model.SecurityCount),
		Restart:  f.model.Restart,
		Snapshot: f.model.Snapshot,
		DryRun:   pendingAsText(f.model.Pending),
	}
	plan.Commands = append(plan.Commands, f.model.Snapshot.Pre)
	if refresh, ok := BuildRefresh(demoManager); ok {
		plan.Commands = append(plan.Commands, refresh)
	}
	upgrade, err := BuildUpgrade(demoManager, mode, false)
	if err != nil {
		return updates.Plan{}, err
	}
	plan.Commands = append(plan.Commands, upgrade, f.model.Snapshot.Post)
	plan.Notes = append(plan.Notes, f.model.Notes...)
	return plan, nil
}

// Run records the command and applies its effect to the sample machine.
func (f *Fake) Run(ctx context.Context, cmd updates.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Ran exposes the recorded commands, which is what a test asserts on.
func (f *Fake) Ran() []updates.Command { return f.run.Ran }

// apply is the hook the fake runner calls: it makes to the in-memory machine
// the change the real command would have made, so the demo stays coherent as
// keys are pressed.
func (f *Fake) apply(cmd updates.Command) (string, error) {
	argv := cmd.Argv
	if len(argv) < 2 {
		return "ok", nil
	}
	switch {
	case argv[0] == "dnf" && argv[1] == "-y":
		count := len(f.model.Pending)
		f.model.Pending, f.model.SecurityCount = nil, 0
		f.model.Restart.Services = nil
		return fmt.Sprintf("Upgraded %d packages. Complete!", count), nil
	case argv[0] == "dnf" && argv[1] == "makecache":
		return "Metadata cache created.", nil
	case argv[0] == "snapper" && argv[1] == "create":
		return "42", nil
	case argv[0] == "systemctl":
		return f.applyTimer(argv)
	default:
		return "ok", nil
	}
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
