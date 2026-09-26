package pkgmgr

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-update/internal/updates"
)

// handOffSentence is what the confirm dialog says about a plan with a
// hand-off in it, so the reader knows the screen is about to go away and
// that it comes back.
const handOffSentence = "The terminal is handed over to " + OmarchyUpdate +
	" for its step: answer it there, and tui-update comes back when it exits."

// handOffPrefix is the escalation prefix of a hand-off: the configured one
// without `-n`. `-n` is there so that a runner step, which has no terminal,
// fails at once instead of waiting on a password prompt nobody can see. A
// hand-off has the terminal, so sudo may ask for the password on it, which is
// exactly what running the updater by hand would do.
func handOffPrefix(privilege []string) []string {
	out := make([]string, 0, len(privilege))
	for i, arg := range privilege {
		if i > 0 && arg == "-n" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// handOffArgv is the invocation of a hand-off: the escalation prefix without
// `-n`, the command's variables through env(1) when it escalates, then the
// resolved binary and the command's own arguments. It mirrors what the kit
// runner builds for a runner step, so Preview and HandOff agree.
func handOffArgv(run *runner.Runner, cmd updates.Command) []string {
	rest := cmd.Argv
	if len(rest) > 0 && rest[0] == run.Name {
		rest = rest[1:]
	}
	var argv []string
	if run.Privileged() {
		argv = append(argv, handOffPrefix(run.Privilege)...)
		if len(cmd.Env) > 0 {
			argv = append(append(argv, "env"), cmd.Env...)
		}
	}
	argv = append(argv, run.Bin)
	return append(argv, rest...)
}

// handOffPreview renders a hand-off the way the confirm dialog shows it: the
// escalation prefix without `-n`, then the command as the user would type it.
func handOffPreview(run *runner.Runner, cmd updates.Command) string {
	if !run.Privileged() {
		return cmd.String()
	}
	prefix := runner.Join(handOffPrefix(run.Privilege))
	if len(cmd.Env) > 0 {
		return prefix + " env " + cmd.String()
	}
	return prefix + " " + cmd.String()
}

// HandOff prepares a hand-off step: the command with the terminal handed over
// to it. It is started by tea.Exec, never here.
func (r *Real) HandOff(cmd updates.Command) (updates.Process, error) {
	if !IsHandOff(cmd) {
		return nil, fmt.Errorf("pkgmgr: %s is a runner step, not a hand-off",
			firstArg(cmd))
	}
	run := r.runnerFor(cmd)
	if run == nil {
		return nil, fmt.Errorf("pkgmgr: %q is not available on this machine",
			firstArg(cmd))
	}
	argv := handOffArgv(run, cmd)
	// G204: argv[0] is the resolved escalation prefix or the resolved binary,
	// and every argument comes from a Build* function, never from a shell.
	c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv built here, no shell
	c.Env = os.Environ()
	if !run.Privileged() {
		// Escalated, the variables travel through env(1) in the argv.
		c.Env = append(c.Env, cmd.Env...)
	}
	return &process{cmd: c}, nil
}

// process adapts an exec.Cmd to updates.Process.
type process struct{ cmd *exec.Cmd }

// Run starts the command and waits for it.
func (p *process) Run() error { return p.cmd.Run() }

// SetStdin gives the child the terminal's input.
func (p *process) SetStdin(r io.Reader) {
	if p.cmd.Stdin == nil {
		p.cmd.Stdin = r
	}
}

// SetStdout gives the child the terminal's output.
func (p *process) SetStdout(w io.Writer) {
	if p.cmd.Stdout == nil {
		p.cmd.Stdout = w
	}
}

// SetStderr gives the child the terminal's error stream.
func (p *process) SetStderr(w io.Writer) {
	if p.cmd.Stderr == nil {
		p.cmd.Stderr = w
	}
}
