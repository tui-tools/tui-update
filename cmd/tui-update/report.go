package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-update/internal/pkgmgr"
)

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-update knows: which package manager was detected, what the compat probe
// read off it, and which of the helper binaries around it are installed.
//
// It never reads the pending updates. --check is the flag that does that, and
// it can take a minute on a large machine; a report has to answer instantly
// and on the machine where the read is what failed. For the same reason a host
// with no supported manager at all still gets a report, with the detection
// error as one of its lines: "there is nothing here to drive" is a bug report,
// not a refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same detection and the same probe --check and the header use. There
	// is one of each in this tool and this is it.
	manager := detectManager(opts.demo)
	backendCompat := probeCompat(context.Background(), manager)

	var selected, selectError string
	if backend, err := pickBackend(cfg, opts, backendCompat); err != nil {
		selectError = err.Error()
	} else {
		selected = backend.Name()
	}

	info := report.Info{
		Tool:           toolName,
		Version:        version,
		Backend:        selected,
		BackendVersion: backendCompat.Version,
		BackendDetail:  backendCompat.Detail,
		Demo:           opts.demo,
		Sudo:           cfg.String(config.KeySudo, ""),
		Theme:          palette.Name,
	}
	if opts.demo {
		// The fake imitates one of the three managers, and which one decides
		// which command builders and which parser the session exercised. It is
		// taken from the backend package's own constant rather than from the
		// fake's Name, which answers with the imitated manager and would leave
		// a demo report indistinguishable from a live one.
		info.Backend = "demo"
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: pkgmgr.DemoManager,
		})
	}
	// The helper line describes the real machine even under --demo, where the
	// probe deliberately looked at nothing: a demo report is still filed from
	// a host, and what that host has installed is what the next question would
	// be about.
	info.Extra = append(info.Extra, report.Field{
		Key: "helpers", Value: describeHelpers(pkgmgr.Inspect(detectManager(false))),
	})
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// describeHelpers renders the detector's view of every binary this manager is
// driven through as one line. Most of what goes wrong here is a helper that is
// not installed rather than a parse that is wrong — a stale-looking pending
// list is checkupdates or fakeroot missing, an empty restart list is
// needrestart or needs-restarting missing, a plan with no snapshot is snapper
// missing — and a report that says only "pacman 7.0.0" cannot tell them apart.
func describeHelpers(states []pkgmgr.State) string {
	parts := make([]string, 0, len(states))
	for _, s := range states {
		state := "absent"
		if s.Installed {
			state = "present"
		}
		parts = append(parts, s.Name+" "+state)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
