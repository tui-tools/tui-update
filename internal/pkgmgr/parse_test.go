package pkgmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/updates"
)

// fixture reads a captured command output.
//
// The dnf ones were captured on a real Fedora 42 host running dnf5, and the
// pacman ones whose names end in a condition — no-fakeroot, no-sync-db,
// dryrun-clean — on a real Omarchy Server 4.0.1 guest in the lab. The rest of
// the apt and pacman set is written by hand against the documented line
// shapes; every one of them is pinned by a test that names the shape it is
// asserting.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// find returns the parsed package with that name.
func find(t *testing.T, packages []updates.Package, name string) updates.Package {
	t.Helper()
	for _, p := range packages {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no package named %q in %d parsed", name, len(packages))
	return updates.Package{}
}

// ------------------------------------------------------------------ dnf ---

func TestParseDNFCheckUpdate(t *testing.T) {
	packages := ParseDNFCheckUpdate(fixture(t, "dnf5-check-update.txt"))
	if len(packages) != 10 {
		t.Fatalf("parsed %d packages, want 10", len(packages))
	}
	code := find(t, packages, "code")
	if code.Arch != "x86_64" || code.New != "1.135.0-1787669223.el8" ||
		code.Repo != "code" {
		t.Errorf("code = %+v", code)
	}
	// The epoch is part of the version dnf prints and must survive.
	modprobe := find(t, packages, "gpu-modprobe")
	if modprobe.New != "3:590.48.01-1.fc42" {
		t.Errorf("gpu-modprobe new = %q, want the epoch kept", modprobe.New)
	}
}

// TestParseDNFCheckUpdateSkipsErrors pins the one thing that makes the
// three-column shape ambiguous: dnf5 prints its errors on stdout, and an
// error line has three space-separated fields too.
func TestParseDNFCheckUpdateSkipsErrors(t *testing.T) {
	out := "Error: Cache-only enabled but no cache for 'example-chat'\n" +
		"gh.x86_64 2.98.0-1 gh-cli\n"
	packages := ParseDNFCheckUpdate(out)
	if len(packages) != 1 || packages[0].Name != "gh" {
		t.Errorf("parsed %+v, want only gh", packages)
	}
}

func TestParseDNFCheckUpdateStopsAtObsoleting(t *testing.T) {
	out := "gh.x86_64 2.98.0-1 gh-cli\n\nObsoleting Packages\n" +
		"old-thing.x86_64 1.0-1 updates\n"
	if packages := ParseDNFCheckUpdate(out); len(packages) != 1 {
		t.Errorf("parsed %d packages, want the obsoleting section skipped",
			len(packages))
	}
}

func TestParseDNFSizes(t *testing.T) {
	sizes := ParseDNFSizes(fixture(t, "dnf5-repoquery-upgrades.txt"))
	if got := sizes["gh.x86_64"]; got != 15499377 {
		t.Errorf("gh size = %d", got)
	}
	if got := humanSize(sizes["gh.x86_64"]); got != "14.8 MiB" {
		t.Errorf("humanSize = %q", got)
	}
}

func TestParseDNFSecurity(t *testing.T) {
	advisories := ParseDNFSecurity(fixture(t, "dnf5-updateinfo-security.txt"))
	if len(advisories) != 2 {
		t.Fatalf("parsed %d advisories, want 2", len(advisories))
	}
	openssl, ok := advisories["openssl.x86_64"]
	if !ok {
		t.Fatalf("openssl.x86_64 is missing from %v", advisories)
	}
	if openssl.ID != "FEDORA-2026-9a1f2b3c4d" || openssl.Severity != "Important" {
		t.Errorf("openssl advisory = %+v", openssl)
	}
	// The NEVRA of a kernel carries dots in the release, which is what makes
	// peeling it from the right the only way that works.
	if _, ok := advisories["kernel.x86_64"]; !ok {
		t.Errorf("kernel.x86_64 is missing from %v", advisories)
	}
}

func TestParseDNFHistory(t *testing.T) {
	transactions := ParseDNFHistory(fixture(t, "dnf-history-list.txt"), 20)
	if len(transactions) == 0 {
		t.Fatal("parsed no transactions")
	}
	first := transactions[0]
	if first.ID != "143" || first.When != "2026-06-19 16:49:43" {
		t.Errorf("newest transaction = %+v", first)
	}
	if first.Command != "dnf install example-dkms" {
		t.Errorf("command = %q", first.Command)
	}
	if got := ParseDNFHistory(fixture(t, "dnf-history-list.txt"), 3); len(got) != 3 {
		t.Errorf("limit ignored: %d transactions", len(got))
	}
}

func TestParseNeedsRestartingServices(t *testing.T) {
	services := ParseNeedsRestartingServices(fixture(t, "needs-restarting-s.txt"))
	want := []string{"sshd", "nginx", "dbus-broker"}
	if len(services) != len(want) {
		t.Fatalf("parsed %v, want %v", services, want)
	}
	for i := range want {
		if services[i] != want[i] {
			t.Errorf("service %d = %q, want %q", i, services[i], want[i])
		}
	}
}

// TestNeedsRestartingRebootWording pins the sentence the dnf backend keys the
// reboot verdict on, captured from this Fedora host.
func TestNeedsRestartingRebootWording(t *testing.T) {
	none := fixture(t, "needs-restarting-r-none.txt")
	if strings.Contains(none, "Reboot is required") {
		t.Errorf("the no-reboot output must not contain the reboot sentence:\n%s",
			none)
	}
}

// ------------------------------------------------------------------ apt ---

func TestParseAPTUpgradable(t *testing.T) {
	packages := ParseAPTUpgradable(fixture(t, "apt-list-upgradable.txt"))
	if len(packages) != 6 {
		t.Fatalf("parsed %d packages, want 6", len(packages))
	}
	libc := find(t, packages, "libc6")
	if libc.Current != "2.39-0ubuntu8.4" || libc.New != "2.39-0ubuntu8.6" {
		t.Errorf("libc6 = %+v", libc)
	}
	if libc.Arch != "amd64" || libc.Repo != "noble-updates,noble-security" {
		t.Errorf("libc6 repo/arch = %+v", libc)
	}
	if !libc.Security || libc.SecurityRef != "noble-security" {
		t.Errorf("libc6 security = %v %q, want the security pocket",
			libc.Security, libc.SecurityRef)
	}
	// A package in updates alone is not a security fix.
	if vim := find(t, packages, "vim-tiny"); vim.Security {
		t.Errorf("vim-tiny was marked as a security update")
	}
	// The kernel and glibc sort to the top.
	if packages[0].Group != updates.GroupKernel {
		t.Errorf("first package is %+v, want a kernel", packages[0])
	}
}

func TestParseAPTSimulation(t *testing.T) {
	plan := ParseAPTSimulation(fixture(t, "apt-get-s-upgrade.txt"))
	if plan.Upgraded != 6 || plan.Installed != 0 || plan.Removed != 0 {
		t.Errorf("counts = %+v", plan)
	}
	if plan.Download != "84.2 MB" {
		t.Errorf("download = %q", plan.Download)
	}
	if plan.Disk != "1,024 B" {
		t.Errorf("disk = %q", plan.Disk)
	}
	if len(plan.Packages) != 6 {
		t.Fatalf("parsed %d Inst lines, want 6", len(plan.Packages))
	}
	ssh := find(t, plan.Packages, "openssh-server")
	if ssh.Current != "1:9.6p1-3ubuntu13.4" || ssh.New != "1:9.6p1-3ubuntu13.5" {
		t.Errorf("openssh-server = %+v", ssh)
	}
	// The origin field is turned back into the pocket list, which is what
	// carries the security flag.
	if ssh.Repo != "noble-security" || !ssh.Security {
		t.Errorf("openssh-server repo = %q, security = %v",
			ssh.Repo, ssh.Security)
	}
	// `Conf` lines are not updates and must not be counted twice.
	for _, p := range plan.Packages {
		if p.Name == "Conf" {
			t.Errorf("a Conf line was parsed as a package")
		}
	}
}

func TestParseNeedrestart(t *testing.T) {
	restart := ParseNeedrestart(fixture(t, "needrestart-b.txt"))
	if restart.Class != updates.RestartReboot {
		t.Errorf("class = %q, want a reboot (KSTA 3)", restart.Class)
	}
	if !restart.RebootRequired {
		t.Errorf("KSTA 3 means the running kernel is not the installed one")
	}
	want := []string{"ssh", "nginx", "cron"}
	if len(restart.Services) != len(want) {
		t.Fatalf("services = %v, want %v", restart.Services, want)
	}
	for i := range want {
		if restart.Services[i] != want[i] {
			t.Errorf("service %d = %q, want %q", i, restart.Services[i], want[i])
		}
	}
	if restart.Reason == "" {
		t.Errorf("a reboot with no reason cannot be explained to a user")
	}
}

func TestParseNeedrestartServicesOnly(t *testing.T) {
	out := "NEEDRESTART-VER: 3.6\nNEEDRESTART-KSTA: 1\n" +
		"NEEDRESTART-SVC: ssh.service\n"
	restart := ParseNeedrestart(out)
	if restart.Class != updates.RestartServices || restart.RebootRequired {
		t.Errorf("restart = %+v, want services only", restart)
	}
}

func TestParseAPTHistory(t *testing.T) {
	transactions := ParseAPTHistory(fixture(t, "apt-history.log"), 20)
	if len(transactions) != 3 {
		t.Fatalf("parsed %d transactions, want 3", len(transactions))
	}
	// Newest first.
	if transactions[0].When != "2026-08-25  09:11:05" {
		t.Errorf("newest = %+v", transactions[0])
	}
	if transactions[2].Command != "/usr/bin/unattended-upgrade" {
		t.Errorf("oldest command = %q", transactions[2].Command)
	}
	if transactions[2].Detail != "2 upgrade" {
		t.Errorf("oldest detail = %q, want both upgraded packages counted",
			transactions[2].Detail)
	}
}

func TestParseRebootRequiredPkgs(t *testing.T) {
	names := ParseRebootRequiredPkgs("linux-image-6.8.0-51-generic\nlibc6\nlibc6\n")
	if len(names) != 2 || names[0] != "linux-image-6.8.0-51-generic" {
		t.Errorf("names = %v, want the duplicate dropped", names)
	}
}

// --------------------------------------------------------------- pacman ---

func TestParsePacmanPending(t *testing.T) {
	for _, name := range []string{"pacman-checkupdates.txt", "pacman-qu.txt"} {
		packages := ParsePacmanPending(fixture(t, name))
		if len(packages) == 0 {
			t.Fatalf("%s: parsed nothing", name)
		}
		glibc := find(t, packages, "glibc")
		if glibc.Current != "2.42-1" || glibc.New != "2.42-2" {
			t.Errorf("%s: glibc = %+v", name, glibc)
		}
		if glibc.Group != updates.GroupCore {
			t.Errorf("%s: glibc group = %q", name, glibc.Group)
		}
		// The kernel and the firmware sort above everything else.
		if packages[0].Name != "linux" {
			t.Errorf("%s: first = %q, want linux", name, packages[0].Name)
		}
	}
}

func TestParsePacmanPendingIgnored(t *testing.T) {
	packages := ParsePacmanPending(fixture(t, "pacman-qu.txt"))
	nvidia := find(t, packages, "gpu-utils")
	if !nvidia.Ignored {
		t.Errorf("`[ignored]` was not recognised: %+v", nvidia)
	}
}

func TestParsePacmanLog(t *testing.T) {
	transactions := ParsePacmanLog(fixture(t, "pacman.log"), 20)
	if len(transactions) != 2 {
		t.Fatalf("parsed %d transactions, want 2", len(transactions))
	}
	// Newest first.
	if transactions[0].Command != "pacman -S htop" {
		t.Errorf("newest = %+v", transactions[0])
	}
	if transactions[0].Detail != "1 installed" {
		t.Errorf("newest detail = %q", transactions[0].Detail)
	}
	if transactions[1].Detail != "2 upgraded" {
		t.Errorf("oldest detail = %q", transactions[1].Detail)
	}
}

func TestParseOmarchyRestart(t *testing.T) {
	restart := ParseOmarchyRestart(fixture(t, "omarchy-restart-dryrun.txt"))
	if restart.Class != updates.RestartReboot || !restart.RebootRequired {
		t.Errorf("restart = %+v, want a reboot", restart)
	}
	if restart.Reason != "glibc 2.42-1 -> 2.42-2; "+
		"linux-firmware 20260812.b1a1e1c9-1 -> 20260826.0a2f66a1-1" {
		t.Errorf("reason = %q", restart.Reason)
	}
	// `deferred` is free text: the units keep their .service suffix and carry
	// a parenthesised reason, so it is never split into a token list.
	if restart.Detail == "" {
		t.Errorf("the deferred line was dropped")
	}
	if len(restart.Services) != 0 {
		t.Errorf("services = %v, want none (`restarted: none`)", restart.Services)
	}
}

func TestParseOmarchyRestartServices(t *testing.T) {
	restart := ParseOmarchyRestart(
		fixture(t, "omarchy-restart-dryrun-services.txt"))
	if restart.Class != updates.RestartServices {
		t.Errorf("class = %q", restart.Class)
	}
	if len(restart.Services) != 2 || restart.Services[0] != "sshd" {
		t.Errorf("services = %v", restart.Services)
	}
	if restart.RebootRequired {
		t.Errorf("`reboot required: no` was read as yes")
	}
}

// -------------------------------------------------------------- snapper ---

func TestParseSnapperConfigs(t *testing.T) {
	configs := ParseSnapperConfigs(fixture(t, "snapper-list-configs.txt"))
	if len(configs) != 2 || configs[0] != "root" || configs[1] != "home" {
		t.Errorf("configs = %v", configs)
	}
}

// -------------------------------------------------------------- helpers ---

func TestHumanSize(t *testing.T) {
	tests := map[int64]string{
		0: "0 B", 512: "512 B", 1024: "1.0 KiB",
		1536: "1.5 KiB", 15499377: "14.8 MiB",
	}
	for bytes, want := range tests {
		if got := humanSize(bytes); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}
