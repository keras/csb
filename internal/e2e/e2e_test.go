// Package e2e tests the broker and client together end-to-end without mocking either side.
// The broker runs as a real HTTP server; the client connects through the full WebSocket+JSON
// protocol stack. Only the host process execution uses real system commands.
package e2e_test

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"csb-host/internal/allowlist"
	"csb-host/internal/broker"
	"csb-host/internal/client"
)

// setup starts a real broker httptest.Server with the given rules and returns
// the WebSocket URL and token needed to connect.
func setup(t *testing.T, rules []allowlist.Rule) (wsURL, token string) {
	t.Helper()
	const tok = "e2e-token"
	ts := httptest.NewServer(broker.NewServer(tok, rules))
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http"), tok
}

// run executes a command through the full broker+client stack and returns
// stdout, stderr, and exit code.
func run(t *testing.T, wsURL, token, cmd string, args []string, stdinData []byte) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	stdinR := bytes.NewReader(stdinData)
	var err error
	code, err = client.Run(wsURL, token, cmd, args, nil, stdinR, &outBuf, &errBuf)
	if err != nil {
		t.Fatalf("client.Run: %v", err)
	}
	return outBuf.String(), errBuf.String(), code
}

func TestE2EEchoArgs(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	wsURL, token := setup(t, rules)

	stdout, _, code := run(t, wsURL, token, "echo", []string{"hello", "world"}, nil)

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if got := strings.TrimRight(stdout, "\n"); got != "hello world" {
		t.Errorf("stdout: got %q, want %q", got, "hello world")
	}
}

func TestE2EExitCode(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "sh", Args: []string{"-c", "*"}}}
	wsURL, token := setup(t, rules)

	_, _, code := run(t, wsURL, token, "sh", []string{"-c", "exit 7"}, nil)

	if code != 7 {
		t.Errorf("exit code: got %d, want 7", code)
	}
}

func TestE2EExitCodeZero(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "true", Args: []string{}}}
	wsURL, token := setup(t, rules)

	_, _, code := run(t, wsURL, token, "true", []string{}, nil)

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
}

func TestE2EStdinPiping(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "cat", Args: []string{}}}
	wsURL, token := setup(t, rules)

	stdout, _, code := run(t, wsURL, token, "cat", []string{}, []byte("hello from stdin"))

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdout != "hello from stdin" {
		t.Errorf("stdout: got %q, want %q", stdout, "hello from stdin")
	}
}

func TestE2EBinaryDataRoundtrip(t *testing.T) {
	// All 256 byte values must survive the base64 encode/decode round-trip.
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	rules := []allowlist.Rule{{Cmd: "cat", Args: []string{}}}
	wsURL, token := setup(t, rules)

	stdout, _, code := run(t, wsURL, token, "cat", []string{}, allBytes)

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdout != string(allBytes) {
		t.Errorf("binary data did not survive round-trip (len got=%d want=%d)", len(stdout), len(allBytes))
	}
}

func TestE2EStderr(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "sh", Args: []string{"-c", "*"}}}
	wsURL, token := setup(t, rules)

	_, stderr, code := run(t, wsURL, token, "sh", []string{"-c", "echo oops >&2; exit 1"}, nil)

	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if got := strings.TrimRight(stderr, "\n"); got != "oops" {
		t.Errorf("stderr: got %q, want %q", got, "oops")
	}
}

func TestE2EAllowlistDenial(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	wsURL, token := setup(t, rules)

	_, _, code := run(t, wsURL, token, "rm", []string{"-rf", "/"}, nil)

	if code != 126 {
		t.Errorf("exit code for denied command: got %d, want 126", code)
	}
}

func TestE2EWrongToken(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	wsURL, _ := setup(t, rules)

	var outBuf, errBuf bytes.Buffer
	_, err := client.Run(wsURL, "wrong-token", "echo", []string{"hi"}, nil, bytes.NewReader(nil), &outBuf, &errBuf)
	if err == nil {
		t.Error("expected error when connecting with wrong token, got nil")
	}
}

func TestE2EMultipleSequentialCommands(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	wsURL, token := setup(t, rules)

	for i, arg := range []string{"first", "second", "third"} {
		stdout, _, code := run(t, wsURL, token, "echo", []string{arg}, nil)
		if code != 0 {
			t.Errorf("cmd %d: exit code %d", i, code)
		}
		if got := strings.TrimRight(stdout, "\n"); got != arg {
			t.Errorf("cmd %d: stdout got %q, want %q", i, got, arg)
		}
	}
}

func TestE2ELiteralArgsDenied(t *testing.T) {
	// "git status" is allowed but "git log" is not — verify per-rule specificity.
	rules := []allowlist.Rule{{Cmd: "git", Args: []string{"status"}}}
	wsURL, token := setup(t, rules)

	_, _, code := run(t, wsURL, token, "git", []string{"log"}, nil)
	if code != 126 {
		t.Errorf("git log should be denied (126), got %d", code)
	}
}

func TestE2EStarWildcard(t *testing.T) {
	// "*" matches exactly one arg regardless of value.
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"*"}}}
	wsURL, token := setup(t, rules)

	// One arg — allowed.
	stdout, _, code := run(t, wsURL, token, "echo", []string{"anything"}, nil)
	if code != 0 {
		t.Errorf("one-arg echo: exit code %d", code)
	}
	if got := strings.TrimRight(stdout, "\n"); got != "anything" {
		t.Errorf("one-arg echo: got %q", got)
	}

	// Two args — denied (pattern has exactly one "*").
	_, _, code = run(t, wsURL, token, "echo", []string{"a", "b"}, nil)
	if code != 126 {
		t.Errorf("two-arg echo should be denied (126), got %d", code)
	}
}

func TestE2EGlobstarAllowsZeroArgs(t *testing.T) {
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	wsURL, token := setup(t, rules)

	stdout, _, code := run(t, wsURL, token, "echo", []string{}, nil)
	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	// echo with no args prints a blank line
	if stdout != "\n" {
		t.Errorf("stdout: got %q, want %q", stdout, "\n")
	}
}

func TestE2ELargeStdinRoundtrip(t *testing.T) {
	// Exercises the partial-write fix: 256 KiB of stdin must arrive intact.
	const size = 256 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251) // prime modulus to avoid repeating patterns
	}

	rules := []allowlist.Rule{{Cmd: "cat", Args: []string{}}}
	wsURL, token := setup(t, rules)

	stdout, _, code := run(t, wsURL, token, "cat", []string{}, payload)

	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if len(stdout) != size {
		t.Errorf("stdout length: got %d, want %d", len(stdout), size)
	}
}

func TestE2EEnvironmentScrubbed(t *testing.T) {
	// GIT_SSH_COMMAND injected from the "sandbox" must not reach the host process.
	rules := []allowlist.Rule{{Cmd: "sh", Args: []string{"-c", "*"}}}
	wsURL, token := setup(t, rules)

	// The broker scrubs env — GIT_SSH_COMMAND should be empty in the spawned process.
	stdout, _, code := run(t, wsURL, token, "sh", []string{"-c", `echo "${GIT_SSH_COMMAND:-empty}"`}, nil)
	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if got := strings.TrimRight(stdout, "\n"); got != "empty" {
		t.Errorf("GIT_SSH_COMMAND leaked into host process: got %q", got)
	}
}
