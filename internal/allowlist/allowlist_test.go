package allowlist

import (
	"testing"
)

func TestMatchArgs(t *testing.T) {
	tests := []struct {
		name    string
		pattern []string
		actual  []string
		want    bool
	}{
		// Literal
		{"exact match", []string{"status"}, []string{"status"}, true},
		{"literal mismatch", []string{"status"}, []string{"log"}, false},
		{"two literals match", []string{"log", "--oneline"}, []string{"log", "--oneline"}, true},
		{"two literals mismatch second", []string{"log", "--oneline"}, []string{"log", "--stat"}, false},

		// Empty pattern
		{"empty pattern zero args", []string{}, []string{}, true},
		{"empty pattern with args", []string{}, []string{"extra"}, false},
		{"nil pattern zero args", nil, []string{}, true},

		// Single wildcard *
		{"star matches one", []string{"*"}, []string{"anything"}, true},
		{"star rejects zero", []string{"*"}, []string{}, false},
		{"star rejects two", []string{"*"}, []string{"a", "b"}, false},
		{"star in middle", []string{"log", "*", "--oneline"}, []string{"log", "HEAD", "--oneline"}, true},
		{"star in middle mismatch", []string{"log", "*", "--oneline"}, []string{"log", "HEAD", "--stat"}, false},

		// Variadic tail **
		{"globstar zero args", []string{"**"}, []string{}, true},
		{"globstar one arg", []string{"**"}, []string{"hello"}, true},
		{"globstar many args", []string{"**"}, []string{"a", "b", "c"}, true},
		{"prefix + globstar zero tail", []string{"log", "**"}, []string{"log"}, true},
		{"prefix + globstar one tail", []string{"log", "**"}, []string{"log", "--oneline"}, true},
		{"prefix + globstar many tail", []string{"log", "**"}, []string{"log", "-n", "10"}, true},
		{"prefix + globstar wrong prefix", []string{"log", "**"}, []string{"status"}, false},
		{"prefix + globstar missing prefix", []string{"log", "**"}, []string{}, false},
		{"star + globstar", []string{"*", "**"}, []string{"anything"}, true},
		{"star + globstar with tail", []string{"*", "**"}, []string{"anything", "extra"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchArgs(tt.pattern, tt.actual)
			if got != tt.want {
				t.Errorf("matchArgs(%v, %v) = %v, want %v", tt.pattern, tt.actual, got, tt.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	rules := []Rule{
		{Cmd: "git", Args: []string{"status"}},
		{Cmd: "git", Args: []string{"log", "**"}},
		{Cmd: "open", Args: []string{"*"}},
		{Cmd: "say", Args: []string{"**"}},
		{Cmd: "cat", Args: []string{}},
	}

	tests := []struct {
		cmd  string
		args []string
		want bool
	}{
		{"git", []string{"status"}, true},
		{"git", []string{"log"}, true},
		{"git", []string{"log", "--oneline"}, true},
		{"git", []string{"diff"}, false},
		{"open", []string{"https://example.com"}, true},
		{"open", []string{"a", "b"}, false},
		{"say", []string{}, true},
		{"say", []string{"hello", "world"}, true},
		{"cat", []string{}, true},
		{"cat", []string{"file"}, false},
		{"rm", []string{"-rf", "/"}, false},
		{"bash", []string{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd+" "+joinArgs(tt.args), func(t *testing.T) {
			got := Match(rules, tt.cmd, tt.args)
			if got != tt.want {
				t.Errorf("Match(%q, %v) = %v, want %v", tt.cmd, tt.args, got, tt.want)
			}
		})
	}
}

func joinArgs(args []string) string {
	r := ""
	for _, a := range args {
		r += " " + a
	}
	return r
}

func TestParse(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantArgs []string
		wantErr  bool
	}{
		{"echo **", "echo", []string{"**"}, false},
		{"git status", "git", []string{"status"}, false},
		{"git log **", "git", []string{"log", "**"}, false},
		{"open *", "open", []string{"*"}, false},
		{"cat", "cat", []string{}, false},
		{"say hello world", "say", []string{"hello", "world"}, false},
		{"  echo  **  ", "echo", []string{"**"}, false}, // extra whitespace
		{"", "", nil, true},
		{"   ", "", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			r, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			if r.Cmd != tt.wantCmd {
				t.Errorf("Cmd: got %q, want %q", r.Cmd, tt.wantCmd)
			}
			if len(r.Args) != len(tt.wantArgs) {
				t.Errorf("Args: got %v, want %v", r.Args, tt.wantArgs)
				return
			}
			for i := range r.Args {
				if r.Args[i] != tt.wantArgs[i] {
					t.Errorf("Args[%d]: got %q, want %q", i, r.Args[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestParseAll(t *testing.T) {
	rules, err := ParseAll([]string{"echo **", "git status", "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}
	if !Match(rules, "echo", []string{}) {
		t.Error("echo with no args should match")
	}
	if !Match(rules, "echo", []string{"hello", "world"}) {
		t.Error("echo with args should match")
	}
	if !Match(rules, "cat", []string{}) {
		t.Error("cat with no args should match")
	}
	if Match(rules, "cat", []string{"file"}) {
		t.Error("cat with file should not match")
	}

	_, err = ParseAll([]string{"echo **", ""})
	if err == nil {
		t.Error("ParseAll with empty rule: expected error")
	}
}
