package pkgmgr

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/updates"
)

// This package is where output tui-update did not write becomes data it acts
// on: `checkupdates`, `apt-get -s upgrade`, `dnf updateinfo`, `needrestart
// -b`, two package manager logs. What comes out of a parser here is a package
// name on the pending list, a service name the plan says will be restarted, a
// reason the screen gives for a reboot. `go test` replays the seeds below on
// every commit, and `go test -fuzz=FuzzParseAPTSimulation ./internal/pkgmgr`
// explores past them locally — see tui-kit's templates/FUZZING.md for the
// family rule.
//
// The seeds are the captured fixtures the table tests use, so the corpus
// starts on the real line shapes and mutates from there instead of guessing
// them.

// seed adds every named testdata file to the corpus, plus the shapes a real
// capture never has: nothing, a lone separator, a truncated line.
func seed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add("|")
	f.Add(":")
	f.Add(" -> ")
}

// checkPackages asserts what every screen reading a pending list is allowed
// to assume: a name it can print and build a command around, a group the
// sort understands, and the sort order itself.
func checkPackages(t *testing.T, packages []updates.Package) {
	t.Helper()
	for _, p := range packages {
		if p.Name == "" {
			t.Fatalf("package with a blank name: %#v", p)
		}
		if strings.ContainsAny(p.Name, " \t\n") {
			t.Fatalf("package name carries whitespace: %q", p.Name)
		}
		if updates.GroupRank(p.Group) >= 4 {
			t.Fatalf("package %q in an unknown group %q", p.Name, p.Group)
		}
		// Both are rendered by the list and by the confirm dialog, so they
		// have to survive whatever the manager printed.
		_ = p.Label()
		_ = p.Transition()
	}
	for i := 1; i < len(packages); i++ {
		if updates.GroupRank(packages[i-1].Group) >
			updates.GroupRank(packages[i].Group) {
			t.Fatalf("groups out of order at %d: %q before %q",
				i, packages[i-1].Group, packages[i].Group)
		}
	}
}

// checkRestart asserts the classification the plan screen switches on, and
// the service names it joins into the sentence it shows.
func checkRestart(t *testing.T, r updates.Restart) {
	t.Helper()
	switch r.Class {
	case updates.RestartNone, updates.RestartServices, updates.RestartReboot:
	default:
		t.Fatalf("restart class is not one of the three: %q", r.Class)
	}
	if r.RebootRequired && r.Class != updates.RestartReboot {
		t.Fatalf("reboot required but classified %q", r.Class)
	}
	if r.Class == updates.RestartServices && len(r.Services) == 0 {
		t.Fatalf("classified as a service restart with no service")
	}
	checkServices(t, r.Services)
}

// checkServices asserts that every unit name is one systemctl could be given.
func checkServices(t *testing.T, services []string) {
	t.Helper()
	for _, unit := range services {
		if unit == "" {
			t.Fatalf("blank unit name in %q", services)
		}
		if strings.ContainsAny(unit, " \t\n") {
			t.Fatalf("unit name carries whitespace: %q", unit)
		}
	}
}

// checkTransactions asserts the history screen's contract: never more rows
// than were asked for, and every row has something in its columns.
func checkTransactions(t *testing.T, transactions []updates.Transaction, limit int) {
	t.Helper()
	if limit > 0 && len(transactions) > limit {
		t.Fatalf("limit %d ignored: %d transactions", limit, len(transactions))
	}
	for _, transaction := range transactions {
		if transaction.ID == "" && transaction.When == "" {
			t.Fatalf("transaction with neither an id nor a date: %#v",
				transaction)
		}
	}
}

// ---------------------------------------------------------------- pacman ---

func FuzzParsePacmanPending(f *testing.F) {
	seed(f, "pacman-checkupdates.txt", "pacman-qu.txt",
		"pacman-checkupdates-no-fakeroot.txt", "pacman-qu-no-sync-db.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkPackages(t, ParsePacmanPending(out))
	})
}

// detailRe is the shape ParsePacmanLog builds a transaction summary in:
// "2 upgraded, 1 installed".
var detailRe = regexp.MustCompile(
	`^\d+ [a-z]+(, \d+ [a-z]+)*$`)

func FuzzParsePacmanLog(f *testing.F) {
	seed(f, "pacman.log", "pacman-omarchy.log")
	f.Fuzz(func(t *testing.T, text string) {
		transactions := ParsePacmanLog(text, 10)
		checkTransactions(t, transactions, 10)
		for _, transaction := range transactions {
			// A pacman run that changed no package is dropped, so anything
			// left has a countable summary rather than an empty column.
			if !detailRe.MatchString(transaction.Detail) {
				t.Fatalf("detail is not a count summary: %q",
					transaction.Detail)
			}
		}
		// The unlimited call is the one the --check path makes.
		checkTransactions(t, ParsePacmanLog(text, 0), 0)
	})
}

func FuzzParseOmarchyRestart(f *testing.F) {
	seed(f, "omarchy-restart-dryrun.txt", "omarchy-restart-dryrun-clean.txt",
		"omarchy-restart-dryrun-services.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkRestart(t, ParseOmarchyRestart(out))
	})
}

// ------------------------------------------------------------------- apt ---

func FuzzParseAPTUpgradable(f *testing.F) {
	seed(f, "apt-list-upgradable.txt")
	f.Fuzz(func(t *testing.T, out string) {
		packages := ParseAPTUpgradable(out)
		checkPackages(t, packages)
		for _, p := range packages {
			// The screen shows the pocket as the evidence for the security
			// flag, so a flag without one would be an unsourced claim.
			if p.Security && p.SecurityRef == "" {
				t.Fatalf("%q flagged as security with no pocket named",
					p.Name)
			}
		}
	})
}

func FuzzParseAPTSimulation(f *testing.F) {
	seed(f, "apt-get-s-upgrade.txt")
	f.Fuzz(func(t *testing.T, out string) {
		plan := ParseAPTSimulation(out)
		checkPackages(t, plan.Packages)
		for _, count := range []int{
			plan.Upgraded, plan.Installed, plan.Removed, plan.Held,
		} {
			if count < 0 {
				t.Fatalf("negative count in %#v", plan)
			}
		}
		// Both are printed verbatim on the plan screen, so a multi-line
		// value would break the layout apart.
		if strings.ContainsAny(plan.Download+plan.Disk, "\n\r") {
			t.Fatalf("size line carries a newline: %q / %q",
				plan.Download, plan.Disk)
		}
	})
}

func FuzzParseNeedrestart(f *testing.F) {
	seed(f, "needrestart-b.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkRestart(t, ParseNeedrestart(out))
	})
}

func FuzzParseAPTHistory(f *testing.F) {
	seed(f, "apt-history.log")
	f.Fuzz(func(t *testing.T, text string) {
		checkTransactions(t, ParseAPTHistory(text, 10), 10)
		checkTransactions(t, ParseAPTHistory(text, 0), 0)
	})
}

func FuzzParseRebootRequiredPkgs(f *testing.F) {
	f.Add("linux-image-6.8.0-51-generic\nlibc6\n")
	f.Add("linux-image\nlinux-image\n")
	f.Add("   \n\n")
	f.Fuzz(func(t *testing.T, text string) {
		names := ParseRebootRequiredPkgs(text)
		seen := map[string]bool{}
		for _, name := range names {
			if name == "" {
				t.Fatalf("blank package name in %q", names)
			}
			if seen[name] {
				t.Fatalf("package named twice: %q", name)
			}
			seen[name] = true
		}
	})
}

// ------------------------------------------------------------------- dnf ---

func FuzzParseDNFCheckUpdate(f *testing.F) {
	seed(f, "dnf5-check-update.txt")
	f.Fuzz(func(t *testing.T, out string) {
		packages := ParseDNFCheckUpdate(out)
		checkPackages(t, packages)
		for _, p := range packages {
			// name.arch is the key the size and advisory maps are read by,
			// so a package that lost its architecture would silently miss
			// both.
			if p.Arch == "" {
				t.Fatalf("%q parsed without an architecture", p.Name)
			}
		}
	})
}

func FuzzParseDNFSizes(f *testing.F) {
	seed(f, "dnf5-repoquery-upgrades.txt")
	f.Fuzz(func(t *testing.T, out string) {
		for key, size := range ParseDNFSizes(out) {
			if key == "" {
				t.Fatalf("blank key for size %d", size)
			}
			if strings.ContainsAny(key, "| \t\n") {
				t.Fatalf("key is not a name.arch: %q", key)
			}
			if size < 0 {
				t.Fatalf("negative size for %q: %d", key, size)
			}
			// The size column renders through humanSize, which the plan and
			// the list both print.
			if humanSize(size) == "" {
				t.Fatalf("size %d rendered as nothing", size)
			}
		}
	})
}

func FuzzParseDNFSecurity(f *testing.F) {
	seed(f, "dnf5-updateinfo-security.txt")
	f.Fuzz(func(t *testing.T, out string) {
		for key, advisory := range ParseDNFSecurity(out) {
			if key == "" {
				t.Fatalf("advisory %#v keyed by nothing", advisory)
			}
			if advisory.Package != key {
				t.Fatalf("advisory keyed by %q but names %q",
					key, advisory.Package)
			}
			// The id is what the screen shows as the source of a security
			// flag; an empty one would be a claim with no reference.
			if advisory.ID == "" {
				t.Fatalf("advisory for %q with no id", key)
			}
		}
	})
}

func FuzzParseDNFHistory(f *testing.F) {
	seed(f, "dnf-history-list.txt")
	f.Fuzz(func(t *testing.T, out string) {
		transactions := ParseDNFHistory(out, 10)
		checkTransactions(t, transactions, 10)
		for _, transaction := range transactions {
			// The id column is dnf's transaction number, and it is what a
			// reader would pass back to `dnf history info`.
			if _, err := strconv.Atoi(transaction.ID); err != nil {
				t.Fatalf("transaction id is not a number: %q", transaction.ID)
			}
		}
		checkTransactions(t, ParseDNFHistory(out, 0), 0)
	})
}

func FuzzParseNeedsRestartingServices(f *testing.F) {
	seed(f, "needs-restarting-s.txt", "needs-restarting-r-none.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkServices(t, ParseNeedsRestartingServices(out))
	})
}

// ------------------------------------------------------------- snapshots ---

func FuzzParseSnapperConfigs(f *testing.F) {
	seed(f, "snapper-list-configs.txt")
	f.Fuzz(func(t *testing.T, out string) {
		for _, config := range ParseSnapperConfigs(out) {
			// The name goes into `snapper -c <config> create`, so a blank or
			// a split one would build a command nobody asked for.
			if config == "" {
				t.Fatalf("blank snapper configuration name")
			}
			// It is the first column of a pipe-separated table, trimmed:
			// a separator inside it would mean the row was read wrong.
			if strings.Contains(config, "|") {
				t.Fatalf("configuration name carries a separator: %q", config)
			}
			if config != strings.TrimSpace(config) {
				t.Fatalf("configuration name is not trimmed: %q", config)
			}
		}
	})
}

// ----------------------------------------------------------------- holds ---
//
// A name that comes out of these two ends up in `apt-mark hold <name>` or
// `dnf versionlock add <name>`, so what matters is that it is a package name
// and nothing else: no whitespace, no version tail, nothing a shell or a
// manager would read as a second argument.

func FuzzParseAPTHolds(f *testing.F) {
	seed(f, "apt-mark-showhold.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkHoldNames(t, ParseAPTHolds(out))
	})
}

func FuzzParseDNFVersionlock(f *testing.F) {
	seed(f, "dnf-versionlock-list.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkHoldNames(t, ParseDNFVersionlock(out))
	})
}

// checkHoldNames is the shared invariant of both hold readers.
func checkHoldNames(t *testing.T, holds map[string]bool) {
	t.Helper()
	for name := range holds {
		if name == "" {
			t.Fatalf("a blank package name was read as held")
		}
		if name != strings.TrimSpace(name) {
			t.Fatalf("held name is not trimmed: %q", name)
		}
		// The name reaches an argv, so it has to satisfy the same rule every
		// other package name in this package does.
		if err := checkPackageName(name); err != nil {
			t.Fatalf("held name is not a package name: %v", err)
		}
	}
}
