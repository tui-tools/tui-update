package pkgmgr

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-update/internal/updates"
)

// The tests in this file are the ones a real Omarchy Server 4.0.1 guest paid
// for. That image ships `checkupdates` from pacman-contrib but not
// `fakeroot`, which checkupdates needs, and it has never run a `pacman -Sy`,
// so its sync databases are not on disk either. Every fixture named here was
// captured on that machine.

// TestCheckupdatesNeedsFakeroot: the script being installed is not the
// question. Without fakeroot every call fails, and the pre-check is what
// keeps the tool from making a doomed call through sudo on every refresh.
func TestCheckupdatesNeedsFakeroot(t *testing.T) {
	if why := checkupdatesUnavailable(false); why == "" {
		t.Error("checkupdates that is not installed must be reported unusable")
	} else if !strings.Contains(why, "pacman-contrib") {
		t.Errorf("reason = %q, want it to name the package", why)
	}
}

// TestCheckupdatesFailureIsNotAnEmptyList: the error checkupdates prints
// without fakeroot must not read as "nothing to upgrade". If it did, the
// fallback would never be taken and the machine would silently report zero.
func TestCheckupdatesFailureIsNotAnEmptyList(t *testing.T) {
	out := fixture(t, "pacman-checkupdates-no-fakeroot.txt")
	if packages := ParsePacmanPending(out); len(packages) != 0 {
		t.Errorf("parsed %d packages out of an error message", len(packages))
	}
	if isNothingToDo(out) {
		t.Error("the fakeroot error must be a failure, not an empty answer")
	}
}

// TestStaleListNoteNamesTheReasonAndTheCure: the note is the whole reason the
// fallback is acceptable. It has to say what could not be used and that the
// upgrade itself still refreshes, or a stale list is just a wrong list.
func TestStaleListNoteNamesTheReasonAndTheCure(t *testing.T) {
	note := staleListNote("fakeroot is not installed")
	for _, want := range []string{
		"checkupdates unavailable", "fakeroot is not installed",
		"may be stale", "-Syu",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q is missing %q", note, want)
		}
	}
}

// TestPacmanQuWithoutSyncDatabases: `pacman -Qu` succeeds on a machine that
// has never synchronised, prints only warnings and reports nothing pending.
// That is an answer the screen must qualify rather than present as "up to
// date", so the warning is recognised.
func TestPacmanQuWithoutSyncDatabases(t *testing.T) {
	out := fixture(t, "pacman-qu-no-sync-db.txt")
	if packages := ParsePacmanPending(out); len(packages) != 0 {
		t.Errorf("parsed %d packages out of pacman's warnings", len(packages))
	}
	if !syncDatabaseMissing(out) {
		t.Error("the missing sync databases must be recognised")
	}
	if syncDatabaseMissing(fixture(t, "pacman-qu.txt")) {
		t.Error("a normal -Qu answer must not be reported as a missing database")
	}
	// `pacman -Qu` exits non-zero when nothing is upgradable, so the way to
	// tell an empty answer from a failure is that it listed no packages. The
	// warnings must not be mistaken for the output of a failed command.
	if !isNothingToDo(withoutPacmanWarnings(out)) {
		t.Error("warnings alone must read as an empty list, not a failure")
	}
	if isNothingToDo(withoutPacmanWarnings(
		fixture(t, "pacman-checkupdates-no-fakeroot.txt"))) {
		t.Error("a real error must survive the warning filter")
	}
}

// TestParseOmarchyRestartOnAQuietMachine pins the shape the Omarchy restart
// pass prints when it has nothing to say. It is the common case on a machine
// that was just built, and an empty classification there must still be a
// classification.
func TestParseOmarchyRestartOnAQuietMachine(t *testing.T) {
	restart := ParseOmarchyRestart(
		fixture(t, "omarchy-restart-dryrun-clean.txt"))
	if restart.RebootRequired {
		t.Error("no reboot was required, so none may be reported")
	}
	if len(restart.Services) != 0 {
		t.Errorf("services = %v, want none", restart.Services)
	}
}

// TestParsePacmanLogFromOmarchy reads the log a real
// `omarchy-server-update run --no-reboot` left behind.
//
// It is a harder input than the hand-written fixture in three ways the
// history screen would have shown: the wrapper runs pacman three times for
// one update, so one command is not one transaction; it upgrades with an
// argv of its own that the parser must quote rather than recognise; and ALPM
// writes plain warnings into the same [ALPM] namespace as the actions, on the
// line right before one.
func TestParsePacmanLogFromOmarchy(t *testing.T) {
	transactions := ParsePacmanLog(fixture(t, "pacman-omarchy.log"), 20)
	if len(transactions) != 2 {
		t.Fatalf("parsed %d transactions, want the two that changed packages",
			len(transactions))
	}
	newest := transactions[0]
	if newest.Command != "pacman -Syu --noconfirm --overwrite /usr/share/omarchy/*" {
		t.Errorf("newest command = %q, want the wrapper's own argv", newest.Command)
	}
	if newest.Detail != "2 upgraded" {
		t.Errorf("newest detail = %q; the ALPM warning line must not count",
			newest.Detail)
	}
	if transactions[1].Detail != "1 reinstalled" {
		t.Errorf("keyring refresh detail = %q", transactions[1].Detail)
	}
}

// TestOmarchyArgvMatchesTheRealCommand checks the flags this tool passes
// against the wrapper's own help text, captured from the guest. The argv is
// pinned by string equality in TestArgvTable; this is the other half, that
// the string it is pinned to is a command the wrapper accepts.
func TestOmarchyArgvMatchesTheRealCommand(t *testing.T) {
	help := fixture(t, "omarchy-update-help.txt")
	upgrade, err := BuildUpgrade(updates.ManagerPacman, updates.UpgradeDefault, true)
	if err != nil {
		t.Fatalf("build upgrade: %v", err)
	}
	for _, arg := range upgrade.Argv[1:] {
		if !strings.Contains(help, arg) {
			t.Errorf("%s does not document %q", OmarchyUpdate, arg)
		}
	}
}
