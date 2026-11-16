package main_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
)

// mockBehavior describes how a mock docker subcommand should respond.
type mockBehavior struct {
	Exit   int
	Stdout string
	Stderr string
}

// mockDockerConfig maps subcommand keys to behaviors.
//
// Supported keys:
//
//	"image inspect"                       docker image inspect ...
//	"build"                               docker build ...
//	"images"                              docker images ...
//	"rmi"                                 docker rmi ...
//	"volume inspect" / "volume create"    docker volume inspect/create ...
//	"volume ls"      / "volume rm"        docker volume ls/rm ...
//	"network inspect"                     docker network inspect ...
//	"run"                                 docker run ...
//	"ps"                                  docker ps ...
//	"container inspect"                   docker container inspect <ids> (label JSON)
//	"container inspect -f"                docker container inspect -f <fmt> <id> (running state)
//	"exec ready"                          docker exec ... tmux has-session ...
//	"exec attach"                         docker exec -it ... tmux attach-session ...
//	"rm"                                  docker rm ...
//
// Unspecified subcommands default to exit 0 with no output.
type mockDockerConfig map[string]mockBehavior

// caseBody renders the body of one shell case arm: optional stdout/stderr
// output followed by an exit. Example output: "printf '%s' 'oops' >&2; exit 1"
func caseBody(cfg mockDockerConfig, key string) string {
	b := cfg[key]
	var parts []string
	if b.Stdout != "" {
		parts = append(parts, "printf '%s' "+shQuoteSh(b.Stdout))
	}
	if b.Stderr != "" {
		parts = append(parts, "printf '%s' "+shQuoteSh(b.Stderr)+" >&2")
	}
	parts = append(parts, fmt.Sprintf("exit %d", b.Exit))
	return strings.Join(parts, "; ")
}

// mockDockerTmpl is the shell script template for the mock docker binary.
// {{caseBody}} renders the exit (and optional output) for each subcommand.
// The build arm drains stdin so Go's copy-goroutine can finish cleanly
// (csb pipes the build context tar via stdin).
var mockDockerTmpl = template.Must(template.New("mock-docker").Funcs(template.FuncMap{
	"q":        shQuoteSh,
	"caseBody": caseBody,
}).Parse(`#!/bin/sh
printf '%s\n' "$@" >> {{q .LogFile}}
printf -- '---\n' >> {{q .LogFile}}
case "$1" in
  image)
    case "$2" in
      inspect) {{caseBody .Cfg "image inspect"}} ;;
      *) exit 0 ;;
    esac ;;
  volume)
    case "$2" in
      inspect) {{caseBody .Cfg "volume inspect"}} ;;
      create)  {{caseBody .Cfg "volume create"}} ;;
      ls)      {{caseBody .Cfg "volume ls"}} ;;
      rm)      {{caseBody .Cfg "volume rm"}} ;;
      *) exit 0 ;;
    esac ;;
  network)
    case "$2" in
      inspect) {{caseBody .Cfg "network inspect"}} ;;
      *) exit 0 ;;
    esac ;;
  build)
    cat > /dev/null
    {{caseBody .Cfg "build"}} ;;
  images) {{caseBody .Cfg "images"}} ;;
  rmi)    {{caseBody .Cfg "rmi"}} ;;
  run)    {{caseBody .Cfg "run"}} ;;
  ps)     {{caseBody .Cfg "ps"}} ;;
  rm)     {{caseBody .Cfg "rm"}} ;;
  container)
    case "$2" in
      inspect)
        if [ "$3" = "-f" ]; then {{caseBody .Cfg "container inspect -f"}}; else {{caseBody .Cfg "container inspect"}}; fi ;;
      *) exit 0 ;;
    esac ;;
  exec)
    case " $* " in
      *" attach-session "*) {{caseBody .Cfg "exec attach"}} ;;
      *) {{caseBody .Cfg "exec ready"}} ;;
    esac ;;
  *) exit 0 ;;
esac
`))

// injectDocker writes a shell-script mock docker binary into a temporary
// directory, prepends it to PATH, and returns the log file path and the env
// slice to pass to runCSB.
//
// Every docker invocation appends one arg-per-line to the log file followed
// by a "---" separator, so readDockerCalls can reconstruct the call list.
func injectDocker(t *testing.T, cfg mockDockerConfig) (logFile string, env []string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "docker.log")

	var buf bytes.Buffer
	if err := mockDockerTmpl.Execute(&buf, struct {
		LogFile string
		Cfg     mockDockerConfig
	}{logFile, cfg}); err != nil {
		t.Fatalf("injectDocker: render template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), buf.Bytes(), 0755); err != nil {
		t.Fatalf("injectDocker: write mock script: %v", err)
	}
	env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	return logFile, env
}

func shQuoteSh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readDockerCalls parses the mock log and returns each invocation as a
// []string of arguments. Returns nil if docker was never called.
func readDockerCalls(t *testing.T, logFile string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readDockerCalls: %v", err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "---":
			if len(cur) > 0 {
				calls = append(calls, cur)
				cur = nil
			}
		case line != "":
			cur = append(cur, line)
		}
	}
	return calls
}

// findCall returns the first call whose leading args match prefix, or nil.
func findCall(calls [][]string, prefix ...string) []string {
	for _, call := range calls {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if call[i] != p {
				match = false
				break
			}
		}
		if match {
			return call
		}
	}
	return nil
}

// hasFlag returns true if args contains flag immediately followed by value.
func hasFlag(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// -- Tests --------------------------------------------------------------------

// TestRunFreshImage verifies the full docker call sequence when the image is
// not yet cached: image inspect -> build -> volume inspect -> volume create -> run.
func TestRunFreshImage(t *testing.T) {
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":   {Exit: 1}, // not found -> trigger build
		"build":           {Exit: 0},
		"volume inspect":  {Exit: 1}, // not found -> trigger create
		"volume create":   {Exit: 0},
		"network inspect": {Exit: 1}, // no gateway IP
		"run":             {Exit: 0},
	})

	dir := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker")
	assert.True(t, ok, "csb run exited non-zero; output:\n%s", out)

	calls := readDockerCalls(t, logFile)
	assert.NotNil(t, findCall(calls, "image", "inspect"), "expected docker image inspect call")
	assert.NotNil(t, findCall(calls, "build"), "expected docker build call (image not found)")
	assert.NotNil(t, findCall(calls, "volume", "inspect"), "expected docker volume inspect call")
	assert.NotNil(t, findCall(calls, "volume", "create"), "expected docker volume create call")
	assert.NotNil(t, findCall(calls, "run"), "expected docker run call")
}

// TestRunImageCached verifies that no build is triggered when the image
// already exists locally.
func TestRunImageCached(t *testing.T) {
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":   {Exit: 0}, // found -> skip build
		"volume inspect":  {Exit: 0}, // found -> skip create
		"network inspect": {Exit: 1},
		"run":             {Exit: 0},
	})

	dir := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker")
	assert.True(t, ok, "csb run exited non-zero; output:\n%s", out)

	calls := readDockerCalls(t, logFile)
	assert.Nil(t, findCall(calls, "build"), "docker build should not be called when image is cached")
	assert.Nil(t, findCall(calls, "volume", "create"), "docker volume create should not be called when volume exists")
	assert.NotNil(t, findCall(calls, "run"), "expected docker run call")
}

// TestRunPlatformPropagation verifies that --arch flows through to both
// docker build --platform and docker run --platform.
func TestRunPlatformPropagation(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			logFile, env := injectDocker(t, mockDockerConfig{
				"image inspect":   {Exit: 1},
				"build":           {Exit: 0},
				"volume inspect":  {Exit: 1},
				"volume create":   {Exit: 0},
				"network inspect": {Exit: 1},
				"run":             {Exit: 0},
			})

			dir := t.TempDir()
			out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker", "--arch="+arch)
			assert.True(t, ok, "csb run exited non-zero; output:\n%s", out)

			calls := readDockerCalls(t, logFile)
			platform := "linux/" + arch

			build := findCall(calls, "build")
			if assert.NotNil(t, build, "expected docker build call") {
				assert.True(t, hasFlag(build, "--platform", platform), "docker build: want --platform %s; got %v", platform, build)
			}

			run := findCall(calls, "run")
			if assert.NotNil(t, run, "expected docker run call") {
				assert.True(t, hasFlag(run, "--platform", platform), "docker run: want --platform %s; got %v", platform, run)
			}
		})
	}
}

// TestRunVolumeMount verifies that the home volume is mounted into the
// container at the expected path.
func TestRunVolumeMount(t *testing.T) {
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":   {Exit: 1},
		"build":           {Exit: 0},
		"volume inspect":  {Exit: 1},
		"volume create":   {Exit: 0},
		"network inspect": {Exit: 1},
		"run":             {Exit: 0},
	})

	dir := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker")
	assert.True(t, ok, "csb run exited non-zero; output:\n%s", out)

	run := findCall(readDockerCalls(t, logFile), "run")
	if assert.NotNil(t, run, "expected docker run call") {
		assert.True(t, hasFlag(run, "-v", "csb-home:/home/sandbox"), "docker run missing home volume mount; got %v", run)
	}
}

// TestRunBuildFailure verifies that a failing docker build causes csb to exit
// non-zero with an error message.
func TestRunBuildFailure(t *testing.T) {
	_, env := injectDocker(t, mockDockerConfig{
		"image inspect": {Exit: 1},
		"build":         {Exit: 1, Stderr: "simulated build failure\n"},
	})

	dir := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker")
	assert.False(t, ok, "csb run should exit non-zero when docker build fails")
	assert.Contains(t, out, "build")
}

// TestRunImageNameConsistency verifies that the image name passed to
// docker build and docker run are identical (content-addressed).
func TestRunImageNameConsistency(t *testing.T) {
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":   {Exit: 1},
		"build":           {Exit: 0},
		"volume inspect":  {Exit: 1},
		"volume create":   {Exit: 0},
		"network inspect": {Exit: 1},
		"run":             {Exit: 0},
	})

	dir := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "--runtime=docker")
	assert.True(t, ok, "csb run exited non-zero; output:\n%s", out)

	calls := readDockerCalls(t, logFile)
	build := findCall(calls, "build")
	run := findCall(calls, "run")
	if !assert.NotNil(t, build, "expected docker build call") || !assert.NotNil(t, run, "expected docker run call") {
		return
	}

	var buildImage string
	for i := 0; i+1 < len(build); i++ {
		if build[i] == "-t" {
			buildImage = build[i+1]
			break
		}
	}
	assert.NotEmpty(t, buildImage, "could not find -t image name in docker build call: %v", build)
	assert.True(t, strings.HasPrefix(buildImage, "csb:"), "image name should start with 'csb:'; got %q", buildImage)

	var runImage string
	for _, arg := range run {
		if strings.HasPrefix(arg, "csb:") {
			runImage = arg
			break
		}
	}
	assert.NotEmpty(t, runImage, "could not find csb: image name in docker run call: %v", run)
	assert.Equal(t, buildImage, runImage, "image name in build and run should match")
}

// -- Detachable sessions ------------------------------------------------------

// TestDetachableStartsThenAttaches verifies the detachable call sequence: the
// container is started in the background (-d, named, labelled), csb waits for
// the tmux session with `exec ... has-session`, then attaches with
// `exec -it ... attach-session`. Nothing stops the container afterwards.
func TestDetachableStartsThenAttaches(t *testing.T) {
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":        {Exit: 0},
		"volume inspect":       {Exit: 0},
		"network inspect":      {Exit: 1},
		"ps":                   {Exit: 0}, // no running sessions
		"run":                  {Exit: 0, Stdout: "abc123def456\n"},
		"exec ready":           {Exit: 0},
		"exec attach":          {Exit: 0},
		"container inspect -f": {Exit: 0, Stdout: "true\n"},
	})

	ws := t.TempDir()
	out, ok := runCSB(t, env, configDirFlag(t.TempDir()), "--workspace="+ws, "--runtime=docker", "--detachable", "--tty")
	assert.True(t, ok, "csb --detachable exited non-zero; output:\n%s", out)
	assert.Contains(t, out, "still running; reattach with: csb attach csb-")

	calls := readDockerCalls(t, logFile)
	run := findCall(calls, "run")
	if assert.NotNil(t, run, "expected docker run call") {
		assert.Contains(t, run, "-d", "detachable run must be backgrounded")
		assert.Contains(t, run, "--name")
		assert.Contains(t, run, "csb.detachable=true")
		assert.Contains(t, run, "csb.workspace="+ws)
	}

	ready := findCall(calls, "exec", "-u")
	assert.NotNil(t, ready, "expected docker exec has-session readiness probe")

	attach := findCall(calls, "exec", "-it")
	if assert.NotNil(t, attach, "expected docker exec -it attach call") {
		assert.Equal(t, []string{"tmux", "attach-session", "-t", "main"}, attach[len(attach)-4:])
		assert.Contains(t, attach, "abc123def456")
	}
	assert.Nil(t, findCall(calls, "rm"), "detaching must not remove the container")
}

// TestDetachableAutoAttach verifies that a bare --detachable run reattaches to
// the running session for the same workspace and config dir, without building
// or starting anything.
func TestDetachableAutoAttach(t *testing.T) {
	ws := t.TempDir()
	cfgDir := t.TempDir()
	labels := fmt.Sprintf(`[{"Id":"abc123def456aaaaaaaaaaaa","Config":{"Labels":{"csb.workspace":%q,"csb.config-dir":%q}}}]`, ws, cfgDir)
	logFile, env := injectDocker(t, mockDockerConfig{
		"ps":                   {Exit: 0, Stdout: "abc123def456\tcsb-proj-aabbcc\tUp 2 minutes\n"},
		"container inspect":    {Exit: 0, Stdout: labels},
		"container inspect -f": {Exit: 0, Stdout: "true\n"},
		"exec attach":          {Exit: 0},
	})

	out, ok := runCSB(t, env, configDirFlag(cfgDir), "--workspace="+ws, "--runtime=docker", "--detachable", "--tty")
	assert.True(t, ok, "csb --detachable exited non-zero; output:\n%s", out)
	assert.Contains(t, out, "attaching to running session csb-proj-aabbcc")

	calls := readDockerCalls(t, logFile)
	assert.Nil(t, findCall(calls, "build"))
	assert.Nil(t, findCall(calls, "run"), "auto-attach must not start a second container")
	assert.NotNil(t, findCall(calls, "exec", "-it"), "expected docker exec attach")
}

// TestDetachableExplicitCommandStartsNew verifies that giving a command skips
// auto-attach: the user wants a fresh container, not the running session.
func TestDetachableExplicitCommandStartsNew(t *testing.T) {
	ws := t.TempDir()
	cfgDir := t.TempDir()
	labels := fmt.Sprintf(`[{"Id":"abc123def456aaaaaaaaaaaa","Config":{"Labels":{"csb.workspace":%q,"csb.config-dir":%q}}}]`, ws, cfgDir)
	logFile, env := injectDocker(t, mockDockerConfig{
		"image inspect":        {Exit: 0},
		"volume inspect":       {Exit: 0},
		"network inspect":      {Exit: 1},
		"ps":                   {Exit: 0, Stdout: "abc123def456\tcsb-proj-aabbcc\tUp 2 minutes\n"},
		"container inspect":    {Exit: 0, Stdout: labels},
		"run":                  {Exit: 0, Stdout: "fff111222333\n"},
		"exec ready":           {Exit: 0},
		"exec attach":          {Exit: 0},
		"container inspect -f": {Exit: 0, Stdout: "true\n"},
	})

	out, ok := runCSB(t, env, configDirFlag(cfgDir), "--workspace="+ws, "--runtime=docker", "--detachable", "--tty", "--", "vim")
	assert.True(t, ok, "csb exited non-zero; output:\n%s", out)
	assert.NotNil(t, findCall(readDockerCalls(t, logFile), "run"), "expected a new container")
}

// TestDetachableSessionDiesBeforeReady verifies that a container which exits
// before its tmux session comes up is reported as an error.
func TestDetachableSessionDiesBeforeReady(t *testing.T) {
	_, env := injectDocker(t, mockDockerConfig{
		"image inspect":        {Exit: 0},
		"volume inspect":       {Exit: 0},
		"network inspect":      {Exit: 1},
		"run":                  {Exit: 0, Stdout: "abc123def456\n"},
		"exec ready":           {Exit: 1},
		"container inspect -f": {Exit: 1},
	})

	out, ok := runCSB(t, env, configDirFlag(t.TempDir()), "--no-workspace", "--runtime=docker", "--detachable", "--tty")
	assert.False(t, ok, "expected failure; output:\n%s", out)
	assert.Contains(t, out, "exited before the session started")
}

// TestDetachableNeedsTTY verifies --detachable is rejected without a terminal.
func TestDetachableNeedsTTY(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--no-workspace", "--runtime=docker", "--detachable", "--no-tty")
	assert.False(t, ok)
	assert.Contains(t, out, "--detachable needs an interactive terminal")
}
