package updates

import (
	"reflect"
	"testing"
)

// TestSnapshotNumber reads `snapper create --print-number`, which prints the
// number alone, possibly after a warning.
func TestSnapshotNumber(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"42", "42", true},
		{"42\n", "42", true},
		{"  7  ", "7", true},
		{"sudo: unable to resolve host lab\n118", "118", true},
		{"", "", false},
		{"Creating snapshot failed.", "", false},
		{"42 43", "", false},
		{"-1", "", false},
	}
	for _, test := range tests {
		got, ok := SnapshotNumber(test.in)
		if got != test.want || ok != test.ok {
			t.Errorf("SnapshotNumber(%q) = %q, %v; want %q, %v",
				test.in, got, ok, test.want, test.ok)
		}
	}
}

// TestBindPreNumber fills the placeholder in and leaves the original alone.
func TestBindPreNumber(t *testing.T) {
	commands := []Command{
		{Argv: []string{"snapper", "create", "-t", "pre", "--print-number"}},
		{Argv: []string{"apt-get", "-y", "upgrade"}},
		{Argv: []string{"snapper", "create", "-t", "post",
			"--pre-number", PreNumber, "--print-number"}},
	}
	bound := BindPreNumber(commands, "42")
	want := []string{"snapper", "create", "-t", "post",
		"--pre-number", "42", "--print-number"}
	if !reflect.DeepEqual(bound[2].Argv, want) {
		t.Errorf("post = %v, want %v", bound[2].Argv, want)
	}
	if !reflect.DeepEqual(bound[0].Argv, commands[0].Argv) ||
		!reflect.DeepEqual(bound[1].Argv, commands[1].Argv) {
		t.Errorf("commands without the placeholder changed: %v", bound)
	}
	if commands[2].Argv[5] != PreNumber {
		t.Errorf("the original commands were changed in place")
	}
}

// TestSnapshotPreStep: the pre snapshot is the first step, and only on a
// plan that takes the pair.
func TestSnapshotPreStep(t *testing.T) {
	plan := Plan{TakeSnapshot: true, Commands: make([]Command, 4)}
	if !plan.IsSnapshotPre(0) || plan.IsSnapshotPre(1) {
		t.Errorf("IsSnapshotPre is wrong")
	}
	plan.TakeSnapshot = false
	if plan.IsSnapshotPre(0) {
		t.Errorf("a plan without the pair has a pre snapshot step")
	}
}
