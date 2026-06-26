package csb

import (
	"fmt"
	"os"
	"strings"
)

// This file implements the "# csb:run-arg" addon directive: extra arguments an
// addon needs on the container run command (caps, devices, namespaces, …),
// optionally guarded by a condition on the build/run facts (runtime, arch).

// parseAddonRunArgs scans enabled addon scripts for "# csb:run-arg" directives
// and returns deduplicated tokens to append to the container run command. facts
// describes the current build/run context (e.g. runtime, arch) that conditional
// directives are matched against; see runArgDirective.
func parseAddonRunArgs(scripts []string, facts map[string]string) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading addon %s: %w", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			// Directives live in the leading comment block. Stop at the first line
			// of actual script: it bounds the scan to the header and keeps a
			// "# csb:run-arg" that appears later (e.g. inside a heredoc or an echoed
			// string) from being mistaken for a directive.
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break
			}
			val, ok := runArgDirective(line, facts)
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			if val == "" || seen[val] {
				continue
			}
			seen[val] = true
			result = append(result, strings.Fields(val)...)
		}
	}
	return result, nil
}

// runArgDirective returns the argument text of a "# csb:run-arg" line and whether
// it applies given facts. A directive may carry a bracketed condition right after
// the keyword:
//
//	# csb:run-arg --tmpfs /run                       (unconditional)
//	# csb:run-arg[runtime=podman] --pid=private      (podman only)
//	# csb:run-arg[runtime=docker|podman] --foo       (either runtime)
//	# csb:run-arg[runtime=podman,arch=arm64] --bar    (both must hold)
//	# csb:run-arg[runtime!=docker] --baz             (negation)
//
// The condition is a comma-separated list of terms, all of which must hold (AND).
// A term is "key=value" or "key!=value"; the value may list alternatives with
// "|" (matches if the fact equals any). Keys are the facts passed by the caller
// (runtime, arch). The "[runtime=podman]" form exists because some args are
// runtime-specific — e.g. podman accepts "--pid=private" but docker rejects it
// ("--pid: invalid PID mode"), its default namespace already being private.
func runArgDirective(line string, facts map[string]string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "# csb:run-arg")
	if !ok {
		return "", false
	}
	// Bracketed condition must immediately follow the keyword: "[...] <args>".
	if strings.HasPrefix(rest, "[") {
		cond, after, found := strings.Cut(rest[1:], "]")
		if !found {
			return "", false // unterminated condition
		}
		args, ok := strings.CutPrefix(after, " ")
		if !ok {
			return "", false // need a separator before the args
		}
		if !evalRunArgCond(cond, facts) {
			return "", false
		}
		return args, true
	}
	// Unconditional form requires a separating space (so "# csb:run-args" and the
	// like don't match).
	if args, ok := strings.CutPrefix(rest, " "); ok {
		return args, true
	}
	return "", false
}

// evalRunArgCond reports whether a run-arg condition holds for facts. cond is a
// comma-separated list of "key=value" / "key!=value" terms (all ANDed); a value
// may list "|"-separated alternatives. A malformed term or a key absent from
// facts is treated as an empty fact value.
func evalRunArgCond(cond string, facts map[string]string) bool {
	matchesAny := func(fact, spec string) bool {
		for _, want := range strings.Split(spec, "|") {
			if fact == strings.TrimSpace(want) {
				return true
			}
		}
		return false
	}
	for _, term := range strings.Split(cond, ",") {
		term = strings.TrimSpace(term)
		if key, spec, ok := strings.Cut(term, "!="); ok {
			if matchesAny(facts[strings.TrimSpace(key)], spec) {
				return false
			}
			continue
		}
		if key, spec, ok := strings.Cut(term, "="); ok {
			if !matchesAny(facts[strings.TrimSpace(key)], spec) {
				return false
			}
			continue
		}
		return false // not a recognized term
	}
	return true
}
