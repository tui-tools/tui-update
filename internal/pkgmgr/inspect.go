package pkgmgr

import (
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// DemoManager is the manager the sample machine runs, exported so a caller can
// name what --demo imitates. The fake answers Name() with this manager's name,
// which is right on screen and wrong in a bug report, where "demo" has to be
// the backend and the imitated manager a separate fact.
const DemoManager = demoManager

// State is what the detector saw of one binary this backend would drive. It
// carries no version: the manager's own version comes from the compat probe,
// and the helpers are interesting only for being there or not.
type State struct {
	// Name is the binary as it is invoked ("checkupdates").
	Name string
	// Installed reports that it was resolved on PATH or in one of the search
	// paths a non-root PATH commonly omits.
	Installed bool
}

// Inspect reports which of the binaries this machine's manager depends on are
// installed, without starting any of them: the answer comes from resolving
// paths, the same way the runners are built.
//
// It exists because most of what goes wrong in this tool is a missing helper
// rather than a broken parse — a pending list that looks stale is
// checkupdates or fakeroot absent, an empty restart list is needrestart or
// needs-restarting absent, a plan that takes no snapshot is snapper absent.
// A report that names the manager alone leaves the reader guessing between
// all of them.
//
// An empty manager means detection found nothing, and then the useful answer
// is which of the three managers are on the machine at all.
func Inspect(manager string) []State {
	var bins []string
	switch manager {
	case updates.ManagerPacman:
		bins = []string{"pacman", "checkupdates", "fakeroot", OmarchyUpdate,
			OmarchyRestart, "snapper", "systemctl"}
	case updates.ManagerAPT:
		bins = []string{"apt", "apt-get", "needrestart", "snapper", "systemctl"}
	case updates.ManagerDNF:
		bins = []string{"dnf", "rpm", "needs-restarting", "snapper", "systemctl"}
	default:
		bins = []string{"pacman", "apt", "dnf"}
	}

	states := make([]State, 0, len(bins))
	for _, bin := range bins {
		states = append(states, State{Name: bin, Installed: available(bin)})
	}
	return states
}

// available resolves one binary the way the runners do, through PATH first and
// then the search paths a non-root PATH commonly omits. It starts nothing.
func available(bin string) bool {
	return runner.Available(bin, searchPaths[bin]...)
}
