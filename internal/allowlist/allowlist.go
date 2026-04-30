package allowlist

import (
	"fmt"
	"strings"
)

// Rule describes one permitted command pattern.
type Rule struct {
	Cmd  string
	Args []string
}

// Parse parses a single allow rule from a string of the form "cmd arg1 arg2 **".
// Tokens are split on whitespace; the first token is the command, the rest are arg patterns.
func Parse(s string) (Rule, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Rule{}, fmt.Errorf("allowlist: empty rule")
	}
	return Rule{Cmd: fields[0], Args: fields[1:]}, nil
}

// ParseAll parses a slice of rule strings and returns the resulting rules.
func ParseAll(rules []string) ([]Rule, error) {
	out := make([]Rule, 0, len(rules))
	for _, s := range rules {
		r, err := Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Match reports whether cmd + args is permitted by any rule.
// Rules are tested in order; first match wins.
func Match(rules []Rule, cmd string, args []string) bool {
	for _, r := range rules {
		if r.Cmd == cmd && matchArgs(r.Args, args) {
			return true
		}
	}
	return false
}

// matchArgs checks whether actual satisfies pattern.
//
// Pattern tokens:
//   - literal string — exact equality
//   - "*"            — matches exactly one arg of any value
//   - "**"           — matches zero or more trailing args; valid only as the last token
func matchArgs(pattern, actual []string) bool {
	if len(pattern) > 0 && pattern[len(pattern)-1] == "**" {
		prefix := pattern[:len(pattern)-1]
		if len(actual) < len(prefix) {
			return false
		}
		for i, p := range prefix {
			if p != "*" && p != actual[i] {
				return false
			}
		}
		return true
	}
	if len(pattern) != len(actual) {
		return false
	}
	for i, p := range pattern {
		if p != "*" && p != actual[i] {
			return false
		}
	}
	return true
}
