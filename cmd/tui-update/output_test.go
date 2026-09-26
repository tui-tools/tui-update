package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestSplitOutput covers the terminal behaviours the apply pane has to
// reproduce: a carriage return replaces the line, escape sequences go, and
// the lines a person would read survive as they were.
func TestSplitOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain lines", "one\ntwo\n", []string{"one", "two"}},
		{"blank line kept", "one\n\ntwo", []string{"one", "", "two"}},
		{"redrawn line keeps its last state",
			"(Reading database ... \r(Reading database ... 5%\r" +
				"(Reading database ... 100%\r(Reading database ... 4381 files " +
				"and directories currently installed.)\nnext",
			[]string{"(Reading database ... 4381 files and directories " +
				"currently installed.)", "next"}},
		{"CRLF line end", "Setting up libc6 ...\r\nDone\r\n",
			[]string{"Setting up libc6 ...", "Done"}},
		{"trailing CR keeps the line", "Progress: 40%\r",
			[]string{"Progress: 40%"}},
		{"CR followed by blanks keeps the line", "Progress: 40%\r   \nnext",
			[]string{"Progress: 40%", "next"}},
		{"colour stripped", "\x1b[1;31mE:\x1b[0m could not get lock",
			[]string{"E: could not get lock"}},
		{"cursor move ends a line", "first\x1b[1Asecond",
			[]string{"first", "second"}},
		{"cursor moves between blanks leave nothing", "a\n\x1b[1A\x1b[2Jb",
			[]string{"a", "b"}},
		{"tab is a space", "a\tb", []string{"a b"}},
		{"frame drawn in the line-drawing set", "\x1b(0lqqk\x1b(Btext",
			[]string{"text"}},
		{"other controls dropped", "bell\a here", []string{"bell here"}},
		{"trailing blank lines dropped", "done\n\n\n", []string{"done"}},
	}
	for _, test := range tests {
		if got := splitOutput(test.in); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: splitOutput(%q)\n got %q\nwant %q",
				test.name, test.in, got, test.want)
		}
	}
}

// TestSplitOutputAptUpgrade runs the pane over a real `apt-get -y upgrade`
// captured without a terminal, as the runner runs it: dpkg redraws its
// "(Reading database ..." line with carriage returns once per package, and
// ends every line it prints in one.
func TestSplitOutputAptUpgrade(t *testing.T) {
	raw, err := os.ReadFile("testdata/apt-get-upgrade-cr.txt")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "\r(Reading database ... 5%") {
		t.Fatal("the fixture no longer carries a redrawn progress line")
	}

	lines := splitOutput(out)
	if want := strings.Count(strings.TrimRight(out, "\r\n"), "\n") + 1; len(lines) != want {
		t.Errorf("%d pane lines, want one per output line (%d)", len(lines), want)
	}
	reading := 0
	for i, line := range lines {
		if strings.ContainsAny(line, "\r\x1b") {
			t.Errorf("line %d still carries terminal control: %q", i, line)
		}
		if strings.HasPrefix(line, "(Reading database") {
			reading++
			if !strings.HasSuffix(line, "files and directories currently installed.)") {
				t.Errorf("line %d is a progress state, not the final one: %q",
					i, line)
			}
			if strings.Count(line, "(Reading database") != 1 {
				t.Errorf("line %d glues progress states together: %q", i, line)
			}
		}
	}
	if reading == 0 {
		t.Error("no (Reading database line survived")
	}
	for _, want := range []string{
		"32 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.",
		"Setting up libc6:amd64 (2.39-0ubuntu8.9) ...",
		"Processing triggers for libc-bin (2.39-0ubuntu8.9) ...",
	} {
		found := false
		for _, line := range lines {
			if line == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the pane lost the line %q", want)
		}
	}
}
