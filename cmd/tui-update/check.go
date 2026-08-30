package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-update/internal/updates"
)

// checkTimeout bounds the read. Loading the model shells out to the package
// manager, rpm and systemctl, and a machine whose manager is wedged behind a
// stale lock must not hang a non-interactive check forever.
const checkTimeout = 60 * time.Second

// checkTimer is one unattended-update unit, flattened for a shell script.
type checkTimer struct {
	Unit    string `json:"unit"`
	Present bool   `json:"present"`
	Enabled bool   `json:"enabled"`
	Active  bool   `json:"active"`
	State   string `json:"state"`
}

// checkReport is what --check prints: the summary a test can assert on
// without walking the whole model, plus the model itself.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation, and it never refreshes the manager's metadata — which is what
// makes it safe to run anywhere, as any user, including in CI against a
// production-shaped machine.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	// Manager is the package manager that was detected and driven.
	Manager string `json:"manager"`
	// Distro is the machine's /etc/os-release ID.
	Distro string `json:"distro,omitempty"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`
	// Pending and Security are the counts. Pending is an integer even when it
	// is zero, so a script can compare it without checking for a null.
	Pending  int `json:"pending"`
	Security int `json:"security"`
	// RebootRequired is the manager's own verdict, not a guess from the
	// package names; Restart is the merged classification.
	RebootRequired bool     `json:"rebootRequired"`
	Restart        string   `json:"restart"`
	Services       []string `json:"services"`
	// Snapshot reports whether a pre-upgrade snapshot can be taken here, and
	// SnapshotConfig which snapper configuration would take it.
	Snapshot       bool   `json:"snapshot"`
	SnapshotConfig string `json:"snapshotConfig,omitempty"`
	// Timers is the state of every unattended-update unit found.
	Timers []checkTimer `json:"timers"`
	// Compat is what the manager version probe found. It is reported rather
	// than asserted: an untested version is a fact about the machine, not a
	// failure of the read path.
	Compat compat.Result `json:"compat"`
	// Model is the parsed state in full.
	Model updates.Model `json:"model"`
}

// runCheck exercises the backend's real read path and prints the result as
// JSON. It returns an error when the manager cannot be read, which main turns
// into a non-zero exit — so a caller can treat the exit code alone as the
// verdict.
//
// A machine with nothing to upgrade is not a failure: an empty pending list
// is the read path working, and it is what the smoke test asserts on a freshly
// updated guest.
func runCheck(backend updates.Backend, backendCompat compat.Result,
	out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	model, err := backend.Load(ctx)
	if err != nil {
		return fmt.Errorf("%s read failed: %w", backend.Name(), err)
	}

	report := checkReport{
		Tool:           toolName,
		Version:        version,
		Manager:        model.Manager,
		Distro:         model.Distro,
		Describe:       backend.Describe(),
		Pending:        len(model.Pending),
		Security:       model.SecurityCount,
		RebootRequired: model.Restart.RebootRequired,
		Restart:        model.Restart.Class,
		Services:       model.Restart.Services,
		Snapshot:       model.Snapshot.Available,
		SnapshotConfig: model.Snapshot.Config,
		Compat:         backendCompat,
		Model:          model,
	}
	// A nil slice marshals as null, which a shell script has to special-case.
	if report.Services == nil {
		report.Services = []string{}
	}
	report.Timers = []checkTimer{}
	for _, timer := range model.Timers {
		report.Timers = append(report.Timers, checkTimer{
			Unit:    timer.Unit,
			Present: timer.Present,
			Enabled: timer.Enabled,
			Active:  timer.Active,
			State:   timer.State,
		})
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
