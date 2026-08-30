package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuiupdate "github.com/tui-tools/tui-update"
)

// probeCompat reads the version of the package manager this tool is about to
// drive.
//
// The facts it is judged against — the minimum version, the versions the lab
// has actually run against, the caveats that apply to a range, and which read
// paths exist on which release — come from the repository's own tool.json,
// embedded in the binary, so there is no second copy of them in the code.
//
// Unlike a tool with one backend, tui-update declares three, and only the one
// this machine runs is probed: asking `pacman --version` on a Fedora box
// would report nothing and asking all three would take three processes to
// answer a question with one answer.
//
// It never fails: a manifest that cannot be parsed, a manager that could not
// be detected and a missing binary all produce the zero Result, whose
// capability set answers yes to everything — which is the right default,
// because a backend that cannot do what was asked refuses in its own words,
// and that is a better message than a view hidden over an unreadable version
// string.
func probeCompat(ctx context.Context, manager string) compat.Result {
	if manager == "" {
		return compat.Result{}
	}
	m, err := manifest.Load(tuiupdate.ManifestJSON)
	if err != nil {
		return compat.Result{}
	}
	backend, ok := m.Backend(manager)
	if !ok {
		return compat.Result{}
	}
	return compat.Probe(ctx, backend)
}
