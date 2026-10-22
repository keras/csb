//go:build addons

// True end-to-end test for the selkies addon: it drives a real headless
// Chromium against the running Selkies web client and asserts the video stream
// actually connects and keeps decoding frames — the whole X → pixelflux →
// WebSocket → browser-decode path, not just "the server processes are alive".
//
// Architecture. Chromium runs *inside* the selkies container (pulled in via the
// packages addon), because the test host has no browser. The container command
// brings up selkies-start, then bridges Chromium's DevTools port to the
// published host port via socat (headless Chromium binds it to container
// loopback only, which podman's port-forward can't reach). The Go test connects
// chromedp to that endpoint as a RemoteAllocator, opens a tab, and navigates to
// the in-container http://localhost:8080/. So the browser sits where selkies
// does and exercises the real media pipeline; it does NOT cover the
// published-8080 host hop (a deliberate scope cut).
//
// Success signal: frames advancing. A hook around the page's VideoDecoder counts
// the frames the client decodes, so a steadily *rising* count proves the
// stream is flowing and decodable, and directly catches the flakiness modes
// that matter: stalls and freezes. A tiny on-screen xterm keeps the
// damage-based capture producing frames so an idle desktop isn't mistaken for
// a stall.
//
// Flakiness instrument, not a pass/retry gate. Real selkies usage is flaky, and
// the point is to *characterize* that. It runs the connect→stream cycle N times
// (CSB_SELKIES_E2E_ATTEMPTS, default 3; crank it up locally), and every attempt
// is a recorded data point with a stage classifier — no-client | no-socket |
// no-frames | frozen | ok — plus milestone timings. On failure it dumps the
// browser console and the server-side logs (bind-mounted out of the container).
// A flaky run then reports *where* it stalled.
package addons_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const cdpPort = 9222 // fixed on both sides so the published ws URL resolves as-is

// selkiesBrowserScript runs inside the container: bring up the full selkies
// stack, launch headless Chromium, then bridge its DevTools port to all
// interfaces so csb's published port can reach it.
//
// The bridge is why this looks convoluted: headless Chromium binds its
// remote-debugging port to the container's *loopback* only (it ignores
// --remote-debugging-address for security), but podman forwards a published
// port to the container's veth IP, not loopback — so the host can't reach a
// loopback-bound DevTools. socat listens on 0.0.0.0:9222 (the published port)
// and forwards to Chromium on 127.0.0.1:9223. --no-sandbox: no userns sandbox
// in the container; --remote-allow-origins=*: else Chromium refuses the ws
// upgrade. socat is exec'd last so the container's lifetime tracks the bridge.
const selkiesBrowserScript = `set -e
selkies-start
# Keep something drawing on the desktop so the stream keeps producing frames.
# selkies captures on damage, so a static screen emits little and the encoder
# goes idle — which would look identical to a stall to the frames-advance
# check below. A tiny xterm printing on a timer generates continuous damage,
# so a healthy pipeline shows a steadily rising decoded-frame count.
export DISPLAY=:1
xterm -e bash -c 'while :; do printf .; sleep 0.1; done' &
# Publish the real display state for the host to read through the ~/.selkies
# bind mount: the DPI that X clients see and the current resolution. This is
# ground truth for the HiDPI test, independent of what selkies logs.
( while :; do
    echo "dpi=$(xrdb -query 2>/dev/null | awk '/^Xft.dpi:/{print $2}') res=$(xrandr 2>/dev/null | sed -n 's/.*current \([0-9]*\) x \([0-9]*\),.*/\1x\2/p')" \
      > "$HOME/.selkies/display.txt.tmp" && mv "$HOME/.selkies/display.txt.tmp" "$HOME/.selkies/display.txt"
    sleep 0.5
  done ) &
# csb preloads libnss_wrapper into every process to give the mapped host user
# a passwd entry. Chromium hangs at startup under it and never opens its
# DevTools port, so run it without the preload.
env -u LD_PRELOAD chromium \
  --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
  --remote-debugging-port=9223 --remote-allow-origins=* \
  --user-data-dir="$HOME/.selkies/chrome" \
  about:blank &
for _ in $(seq 1 150); do
  (exec 3<>/dev/tcp/127.0.0.1/9223) 2>/dev/null && { exec 3>&-; break; }
  sleep 0.2
done
exec socat TCP-LISTEN:9222,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:9223`

// hookScript is installed before navigation so it wraps the page's
// VideoDecoder and counts the frames the Selkies client decodes. Frames of the
// client's own 320x240 codec-support probe are ignored; the desktop is larger.
const hookScript = `(function(){
  window.__csb = { frames: 0, vw: 0, vh: 0 };
  var Orig = window.VideoDecoder;
  if (!Orig) return;
  function Wrapped(init){
    var out = init.output;
    var wrapped = Object.assign({}, init, { output: function(f){
      if (f.codedWidth > 320) { window.__csb.frames++; window.__csb.vw = f.displayWidth; window.__csb.vh = f.displayHeight; }
      return out.apply(this, arguments);
    }});
    return new Orig(wrapped);
  }
  Wrapped.prototype = Orig.prototype;
  Wrapped.isConfigSupported = Orig.isConfigSupported.bind(Orig);
  window.VideoDecoder = Wrapped;
})();`

// pollScript returns the current pipeline state as JSON. window.selkiesTransport
// is the client's data socket (or its proxy to a socket worker); readyState 1
// means open.
const pollScript = `(() => {
  var out = {stage:'no-client', ws:-1, frames:0, vw:0, vh:0, decoder: typeof VideoDecoder !== 'undefined'};
  var t = window.selkiesTransport;
  if (!t) return out;
  out.ws = (t.readyState === undefined) ? 1 : t.readyState;
  if (out.ws !== 1) { out.stage = 'no-socket'; return out; }
  var c = window.__csb || {};
  out.frames = c.frames || 0; out.vw = c.vw || 0; out.vh = c.vh || 0;
  out.stage = out.frames === 0 ? 'no-frames' : 'streaming';
  return out;
})()`

type probe struct {
	Stage   string `json:"stage"`
	WS      int    `json:"ws"`
	Frames  int    `json:"frames"`
	VW      int    `json:"vw"`
	VH      int    `json:"vh"`
	Decoder bool   `json:"decoder"`
}

type attemptResult struct {
	idx         int
	final       probe
	tSocket     time.Duration // data socket open
	tFirstFrame time.Duration // first desktop frame decoded
	tOK         time.Duration // stage == ok (frames advancing)
	console     []string
}

const notReached = time.Duration(-1)

func (r attemptResult) ok() bool { return r.final.Stage == "ok" }

func (r attemptResult) summary() string {
	ms := func(d time.Duration) string {
		if d < 0 {
			return "-"
		}
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	}
	return fmt.Sprintf("stage=%s ws=%d frames=%d webcodecs=%t res=%dx%d | socket=%s frame=%s ok=%s",
		r.final.Stage, r.final.WS, r.final.Frames, r.final.Decoder,
		r.final.VW, r.final.VH,
		ms(r.tSocket), ms(r.tFirstFrame), ms(r.tOK))
}

// selkiesBrowser is a running csb container hosting selkies + headless Chromium.
type selkiesBrowser struct {
	cmd       *exec.Cmd
	logDir    string // host side of the ~/.selkies bind mount
	configDir string // csb --config-dir; also the container's csb.config-dir label
	out       *syncBuf
	cdpPort   int
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func startSelkiesBrowser(t *testing.T, extraAddons ...string) *selkiesBrowser {
	t.Helper()
	logDir := t.TempDir()
	configDir := t.TempDir()
	hostPort := cdpPort

	args := []string{
		"--config-dir", configDir,
		"--addon", "selkies",
		"--addon", "packages chromium socat",
	}
	for _, a := range extraAddons {
		args = append(args, "--addon", a)
	}
	args = append(args,
		"--no-workspace",
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, cdpPort),
		// Surface the server-side logs on the host by bind-mounting the
		// selkies rundir out; selkies-start writes Xorg/xfce4-session/selkies/pulse
		// logs here. Must be :rw — csb's --mount defaults to read-only.
		"--mount", logDir+":~/.selkies:rw",
		"--", "bash", "-c", selkiesBrowserScript)
	cmd := exec.Command(csbBin, args...)

	out := &syncBuf{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start selkies+chromium container: %v", err)
	}

	sb := &selkiesBrowser{cmd: cmd, logDir: logDir, configDir: configDir, out: out, cdpPort: hostPort}
	t.Cleanup(sb.stop)
	return sb
}

func (sb *selkiesBrowser) stop() {
	if sb.cmd.Process != nil {
		_ = sb.cmd.Process.Kill()
		_, _ = sb.cmd.Process.Wait()
	}
	reapContainer(sb.configDir)
}

// waitWSURL polls the published DevTools endpoint until Chromium answers, then
// returns its browser websocket URL (host rewritten to the published port).
func (sb *selkiesBrowser) waitWSURL(t *testing.T, timeout time.Duration) string {
	t.Helper()
	base := fmt.Sprintf("http://127.0.0.1:%d/json/version", sb.cdpPort)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ws := fetchWSURL(base, sb.cdpPort); ws != "" {
			return ws
		}
		if sb.cmd.ProcessState != nil { // container exited early
			t.Fatalf("container exited before Chromium came up\n--- output ---\n%s", sb.out.String())
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Chromium DevTools endpoint never came up on :%d within %s\n--- output ---\n%s\n--- server logs ---\n%s",
		sb.cdpPort, timeout, sb.out.String(), sb.dumpLogs())
	return ""
}

func fetchWSURL(base string, port int) string {
	// A bounded wait: the published port accepts connections before Chromium
	// is up, and an unbounded Get then blocks the whole poll loop.
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WebSocketDebuggerURL == "" {
		return ""
	}
	// Chromium may report the container-internal host; force the published one.
	if u := strings.SplitN(v.WebSocketDebuggerURL, "/devtools/", 2); len(u) == 2 {
		return fmt.Sprintf("ws://127.0.0.1:%d/devtools/%s", port, u[1])
	}
	return v.WebSocketDebuggerURL
}

// dumpLogs returns a compact tail of every server-side log for diagnostics.
func (sb *selkiesBrowser) dumpLogs() string {
	var b strings.Builder
	entries, _ := os.ReadDir(sb.logDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sb.logDir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "=== %s ===\n%s\n", e.Name(), tail(string(data), 40))
	}
	return b.String()
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestSelkiesE2EStream(t *testing.T) {
	runSelkiesE2E(t)
}

// TestSelkiesE2EStreamSystemd runs the same browser e2e with the systemd addon
// also enabled — the heavier, more realistic mode (systemd as PID 1), where the
// desktop session and PulseAudio start beside systemd's own user services.
func TestSelkiesE2EStreamSystemd(t *testing.T) {
	runSelkiesE2E(t, "systemd")
}

// TestSelkiesE2EHiDPI proves the HiDPI path with a real browser reporting
// devicePixelRatio=2. Two things must happen: Selkies renders the desktop at
// DEVICE pixels (1280x800 CSS becomes 2560x1600) and then raises the desktop
// DPI to 96*2 to compensate. Without the second, the picture is sharp but
// every label is half its physical size.
//
// Both halves are asserted from the real display state the container publishes
// (Xft.dpi as X clients read it, and the current resolution), not from what
// selkies logs, so a DPI change that reports success while changing nothing
// still fails. The log is checked as well, for the failure text that points at
// the cause.
//
// The metrics override is applied BEFORE navigating on purpose. The client
// reports its pixel ratio when it connects, so first connect, not resize, is
// the path a real HiDPI user takes and the one that has to work. Overriding
// after the stream is up registers the session at scale 1 and proves nothing.
func TestSelkiesE2EHiDPI(t *testing.T) {
	sb := startSelkiesBrowser(t)
	wsURL := sb.waitWSURL(t, 3*time.Minute)

	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), wsURL, chromedp.NoModifyURL)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	runCtx, runCancel := context.WithTimeout(ctx, 4*time.Minute)
	defer runCancel()

	if err := chromedp.Run(runCtx, foreground(), chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetDeviceMetricsOverride(1280, 800, 2, false).Do(ctx)
	})); err != nil {
		t.Fatalf("set device metrics override: %v", err)
	}
	if err := chromedp.Run(runCtx, chromedp.Navigate("http://localhost:8080/")); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	var dpr float64
	if err := chromedp.Run(runCtx, chromedp.Evaluate(`window.devicePixelRatio`, &dpr)); err != nil {
		t.Fatalf("read devicePixelRatio: %v", err)
	}
	if dpr != 2 {
		t.Fatalf("devicePixelRatio override did not take: got %v, want 2", dpr)
	}

	// Device pixels, not CSS pixels: 1280x800 at 2x should ask for 2560x1600,
	// and the desktop must reach at least 2000 wide, well past the CSS size.
	var state string
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(filepath.Join(sb.logDir, "display.txt"))
		state = strings.TrimSpace(string(b))
		if displayWidth(state) >= 2000 && strings.HasPrefix(state, "dpi=192 ") {
			break
		}
		time.Sleep(time.Second)
	}
	if w := displayWidth(state); w < 2000 {
		t.Fatalf("desktop did not scale to device pixels (display %q)\n%s", state, sb.dumpLogs())
	}
	if !strings.HasPrefix(state, "dpi=192 ") {
		t.Fatalf("desktop DPI did not follow devicePixelRatio=2 (display %q, want dpi=192)\n%s", state, sb.dumpLogs())
	}

	b, _ := os.ReadFile(filepath.Join(sb.logDir, "selkies.log"))
	logText := string(b)
	for _, bad := range []string{"Failed to set DPI", "No XFCE session", "No DPI setting method succeeded"} {
		if strings.Contains(logText, bad) {
			t.Fatalf("selkies logged %q — the DPI change did not apply cleanly\n%s", bad, tail(logText, 30))
		}
	}
	t.Logf("HiDPI ok: devicePixelRatio=2 -> display %q", state)
}

// foreground makes the tab count as visible and focused. The client stops the
// video stream while its tab is hidden, and a tab opened over the DevTools port
// starts out hidden. Without this the server is told to send nothing and the
// run reads as a stall.
func foreground() chromedp.Tasks {
	return chromedp.Tasks{
		emulation.SetFocusEmulationEnabled(true),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return page.BringToFront().Do(ctx)
		}),
	}
}

// displayWidth parses the width out of the "dpi=N res=WxH" line the container
// publishes, or 0 when it is absent or malformed.
func displayWidth(state string) int {
	_, res, ok := strings.Cut(state, "res=")
	if !ok {
		return 0
	}
	w, _, _ := strings.Cut(res, "x")
	n, _ := strconv.Atoi(w)
	return n
}

func runSelkiesE2E(t *testing.T, extraAddons ...string) {
	attempts := envInt("CSB_SELKIES_E2E_ATTEMPTS", 3)
	perAttempt := time.Duration(envInt("CSB_SELKIES_E2E_ATTEMPT_SECS", 45)) * time.Second

	sb := startSelkiesBrowser(t, extraAddons...)
	wsURL := sb.waitWSURL(t, 3*time.Minute)

	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), wsURL, chromedp.NoModifyURL)
	defer cancelAlloc()

	results := make([]attemptResult, 0, attempts)
	for i := 0; i < attempts; i++ {
		r := runAttempt(t, allocCtx, i, perAttempt)
		results = append(results, r)
		t.Logf("attempt %d: %s", i, r.summary())
		if !r.ok() {
			t.Logf("attempt %d console:\n%s", i, strings.Join(r.console, "\n"))
		}
	}

	report(t, results)
	if logs := sb.dumpLogs(); anyFailed(results) {
		t.Logf("--- server logs ---\n%s", logs)
	}

	okCount := 0
	for _, r := range results {
		if r.ok() {
			okCount++
		}
	}
	if okCount != attempts {
		t.Fatalf("selkies stream reached 'ok' in %d/%d attempts; see per-attempt stages above", okCount, attempts)
	}
}

// runAttempt opens a fresh tab, navigates to the Selkies client, and polls the
// pipeline until frames advance or the deadline passes. Each call is an
// independent connect cycle (fresh tab → fresh WebSocket session).
func runAttempt(t *testing.T, allocCtx context.Context, idx int, deadline time.Duration) attemptResult {
	t.Helper()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	res := attemptResult{idx: idx, tSocket: notReached, tFirstFrame: notReached, tOK: notReached}

	var consoleMu sync.Mutex
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			var parts []string
			for _, a := range e.Args {
				parts = append(parts, strings.Trim(string(a.Value), `"`))
			}
			consoleMu.Lock()
			res.console = append(res.console, e.Type.String()+": "+strings.Join(parts, " "))
			consoleMu.Unlock()
		case *runtime.EventExceptionThrown:
			consoleMu.Lock()
			res.console = append(res.console, "exception: "+e.ExceptionDetails.Text)
			consoleMu.Unlock()
		}
	})

	runCtx, runCancel := context.WithTimeout(ctx, deadline+15*time.Second)
	defer runCancel()

	if err := chromedp.Run(runCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(hookScript).Do(ctx)
			return err
		}),
		foreground(),
		chromedp.Navigate("http://localhost:8080/"),
	); err != nil {
		t.Logf("attempt %d navigate error: %v", idx, err)
		return res
	}

	start := time.Now()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	pollDeadline := time.Now().Add(deadline)
	// Success is proven by frames *advancing*: a live, rising decoded-frame
	// count directly catches the flakiness modes that matter — stalls and
	// freezes.
	minAdvance := envInt("CSB_SELKIES_E2E_MIN_FRAME_ADVANCE", 30)
	baseFrames := -1
	var baseTime time.Time
	for {
		var p probe
		evalCtx, evalCancel := context.WithTimeout(ctx, 5*time.Second)
		err := chromedp.Run(evalCtx, chromedp.Evaluate(pollScript, &p))
		evalCancel()
		if err == nil {
			res.final = p
			el := time.Since(start)
			if p.WS == 1 && res.tSocket < 0 {
				res.tSocket = el
			}
			if p.Frames > 0 && res.tFirstFrame < 0 {
				res.tFirstFrame = el
			}
			if p.Stage == "streaming" {
				if baseFrames < 0 {
					baseFrames, baseTime = p.Frames, time.Now()
				}
				if p.Frames-baseFrames >= minAdvance && time.Since(baseTime) >= time.Second {
					res.final.Stage = "ok"
					res.tOK = el
					return res
				}
			}
		}
		select {
		case <-poll.C:
		case <-runCtx.Done():
			return res
		}
		if time.Now().After(pollDeadline) {
			// Connected with frames but they never advanced enough: a frozen stream.
			if res.final.Stage == "streaming" && baseFrames >= 0 && res.final.Frames-baseFrames < minAdvance {
				res.final.Stage = "frozen"
			}
			return res
		}
	}
}

func report(t *testing.T, results []attemptResult) {
	t.Helper()
	stages := map[string]int{}
	var okTimes []time.Duration
	for _, r := range results {
		stages[r.final.Stage]++
		if r.ok() && r.tFirstFrame >= 0 {
			okTimes = append(okTimes, r.tFirstFrame)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "selkies e2e: %d attempts | stages:", len(results))
	for stage, n := range stages {
		fmt.Fprintf(&b, " %s=%d", stage, n)
	}
	if len(okTimes) > 0 {
		var sum, max time.Duration
		min := okTimes[0]
		for _, d := range okTimes {
			sum += d
			if d < min {
				min = d
			}
			if d > max {
				max = d
			}
		}
		fmt.Fprintf(&b, " | first-frame min=%s avg=%s max=%s",
			min.Round(time.Millisecond), (sum / time.Duration(len(okTimes))).Round(time.Millisecond), max.Round(time.Millisecond))
	}
	t.Log(b.String())
}

func anyFailed(results []attemptResult) bool {
	for _, r := range results {
		if !r.ok() {
			return true
		}
	}
	return false
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
