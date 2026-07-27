//go:build addons

// True end-to-end test for the selkies addon: it drives a real headless
// Chromium against the running Selkies web client and asserts the WebRTC video
// stream actually negotiates and keeps decoding frames — the whole
// X → GStreamer → coturn → ICE → browser-decode path, not just "the server
// processes are alive".
//
// Architecture. Chromium runs *inside* the selkies container (pulled in via the
// packages addon), because the test host has no browser. The container command
// brings up selkies-start, then bridges Chromium's DevTools port to the
// published host port via socat (headless Chromium binds it to container
// loopback only, which podman's port-forward can't reach). The Go test connects
// chromedp to that endpoint as a RemoteAllocator, opens a tab, and navigates to
// the in-container http://localhost:8080/. So the browser sits where selkies
// does and exercises the real media pipeline over the coturn relay; it does NOT
// cover the published-8080 host hop (a deliberate scope cut).
//
// Success signal: frames advancing, not pixels. Headless Chromium with software
// rendering (--disable-gpu) does not composite WebRTC <video> into a 2D canvas
// or a CDP screenshot — both read back black even while frames decode — so a
// pixel-fidelity check is unreliable here (it would need a headed/GPU run). The
// reliable browser-side proof is a steadily *rising* framesDecoded, which also
// directly catches the flakiness modes that matter: stalls and freezes. A tiny
// on-screen xterm keeps the damage-based capture producing frames so an idle
// desktop isn't mistaken for a stall.
//
// Flakiness instrument, not a pass/retry gate. Real selkies usage is flaky, and
// the point is to *characterize* that. It runs the connect→stream cycle N times
// (CSB_SELKIES_E2E_ATTEMPTS, default 3; crank it up locally), and every attempt
// is a recorded data point with a stage classifier — no-signaling | no-ice |
// ice-failed | connected-no-frames | frozen | ok — plus getStats
// (framesDecoded/dropped, packetsLost, the nominated ICE candidate pair) and
// milestone timings. On failure it dumps the browser console and the
// server-side logs (bind-mounted out of the container). A flaky run then reports
// *where* it stalled, e.g. "2/10 stuck at no-ice, candidate pair never
// nominated", which is the actionable signal.
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
# selkies captures via damage-based ximagesrc, so a static screen emits little
# XDamage and the encoder goes idle — which would look identical to a stall to
# the frames-advance check below. A tiny xterm printing on a timer generates
# continuous damage, so a healthy pipeline shows a steadily rising framesDecoded.
export DISPLAY=:1
xterm -e bash -c 'while :; do printf .; sleep 0.1; done' &
chromium \
  --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
  --remote-debugging-port=9223 --remote-allow-origins=* \
  --user-data-dir="$HOME/.selkies/chrome" \
  about:blank &
for _ in $(seq 1 150); do
  (exec 3<>/dev/tcp/127.0.0.1/9223) 2>/dev/null && { exec 3>&-; break; }
  sleep 0.2
done
exec socat TCP-LISTEN:9222,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:9223`

// hookScript is installed before navigation so it captures every
// RTCPeerConnection the Selkies client constructs; the poll reads them back.
const hookScript = `(function(){
  window.__csb = { pcs: [], events: [] };
  var Orig = window.RTCPeerConnection;
  if (!Orig) return;
  var idx = 0;
  function wrap(pc){
    var id = idx++, t0 = performance.now();
    function mark(k, v){ window.__csb.events.push({pc: id, t: Math.round(performance.now()-t0), k: k, v: String(v==null?'':v)}); }
    mark('pc', 'created');
    pc.addEventListener('icegatheringstatechange', function(){ mark('gather', pc.iceGatheringState); });
    pc.addEventListener('iceconnectionstatechange', function(){ mark('ice', pc.iceConnectionState); });
    pc.addEventListener('icecandidate', function(e){ if (e.candidate) mark('cand', e.candidate.type+'/'+(e.candidate.protocol||'')); });
    ['createOffer','createAnswer','setLocalDescription','setRemoteDescription'].forEach(function(m){
      var orig = pc[m];
      pc[m] = function(){ var r = orig.apply(pc, arguments); try { Promise.resolve(r).then(function(){ mark(m, 'ok'); }, function(){}); } catch (e) {} return r; };
    });
    return pc;
  }
  window.RTCPeerConnection = function(){
    var pc = new (Function.prototype.bind.apply(Orig, [null].concat([].slice.call(arguments))));
    window.__csb.pcs.push(pc);
    return wrap(pc);
  };
  window.RTCPeerConnection.prototype = Orig.prototype;
})();`

// pollScript returns the current pipeline state as JSON, up to the point of
// "streaming" (ICE connected + video frames decoding). The pixel-fidelity check
// is deliberately NOT done here: a 2D-canvas getImageData() of the <video> reads
// back black under headless Chromium with --disable-gpu, even while frames
// decode. The Go side instead grabs a CDP screenshot (which captures the real
// composited frame) and analyzes it.
const pollScript = `(async () => {
  var out = {stage:'no-signaling', ice:'', frames:0, dropped:0, pktloss:0, vw:0, vh:0, cand:'', pcs:0};
  var pcs = (window.__csb && window.__csb.pcs) || [];
  out.pcs = pcs.length;
  if (pcs.length === 0) return out;
  var pc = pcs[0];
  out.ice = pc.iceConnectionState || '';
  try {
    var stats = await pc.getStats();
    stats.forEach(function(r){
      if (r.type === 'inbound-rtp' && r.kind === 'video') {
        out.frames = r.framesDecoded || 0;
        out.dropped = r.framesDropped || 0;
        out.pktloss = r.packetsLost || 0;
      }
      if (r.type === 'candidate-pair' && r.nominated) { out.cand = r.state || ''; }
    });
  } catch (e) {}
  if (out.ice === 'failed') { out.stage = 'ice-failed'; return out; }
  if (out.ice !== 'connected' && out.ice !== 'completed') { out.stage = 'no-ice'; return out; }
  var v = document.querySelector('video');
  if (v) { out.vw = v.videoWidth||0; out.vh = v.videoHeight||0; }
  if (out.frames === 0) { out.stage = 'connected-no-frames'; return out; }
  out.stage = 'streaming';
  out.events = (window.__csb && window.__csb.events) || [];
  return out;
})()`

type probe struct {
	Stage   string     `json:"stage"`
	Ice     string     `json:"ice"`
	Frames  int        `json:"frames"`
	Dropped int        `json:"dropped"`
	PktLoss int        `json:"pktloss"`
	VW      int        `json:"vw"`
	VH      int        `json:"vh"`
	Cand    string     `json:"cand"`
	PCs     int        `json:"pcs"`
	Events  []iceEvent `json:"events"`
}

// iceEvent is one timestamped WebRTC milestone, ms relative to *its own* peer
// connection's creation (Pc): negotiation calls (createOffer/Answer,
// set{Local,Remote}Description), gathering/ice state changes, gathered
// candidates. Per-PC timing avoids the misleading absolute performance.now().
type iceEvent struct {
	Pc int    `json:"pc"`
	T  int    `json:"t"`
	K  string `json:"k"`
	V  string `json:"v"`
}

func (p probe) timeline() string {
	if len(p.Events) == 0 {
		return "(no events)"
	}
	parts := make([]string, len(p.Events))
	for i, e := range p.Events {
		parts[i] = fmt.Sprintf("#%d %s=%s@%dms", e.Pc, e.K, e.V, e.T)
	}
	return strings.Join(parts, " ")
}

type attemptResult struct {
	idx         int
	final       probe
	tSignaling  time.Duration // first PC observed
	tICE        time.Duration // ICE connected/completed
	tFirstFrame time.Duration // framesDecoded > 0
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
	return fmt.Sprintf("stage=%s ice=%s frames=%d dropped=%d pktloss=%d cand=%s res=%dx%d | sig=%s ice=%s frame=%s ok=%s",
		r.final.Stage, r.final.Ice, r.final.Frames, r.final.Dropped, r.final.PktLoss, r.final.Cand,
		r.final.VW, r.final.VH,
		ms(r.tSignaling), ms(r.tICE), ms(r.tFirstFrame), ms(r.tOK))
}

// selkiesBrowser is a running csb container hosting selkies + headless Chromium.
type selkiesBrowser struct {
	cmd     *exec.Cmd
	logDir  string // host side of the ~/.selkies bind mount
	out     *syncBuf
	cdpPort int
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
	hostPort := cdpPort

	args := []string{
		"--config-dir", t.TempDir(),
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
		// selkies rundir out; selkies-start writes Xorg/coturn/selkies/pulse
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

	sb := &selkiesBrowser{cmd: cmd, logDir: logDir, out: out, cdpPort: hostPort}
	t.Cleanup(sb.stop)
	return sb
}

func (sb *selkiesBrowser) stop() {
	if sb.cmd.Process != nil {
		_ = sb.cmd.Process.Kill()
		_, _ = sb.cmd.Process.Wait()
	}
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
	resp, err := http.Get(base)
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
// also enabled — the heavier, more realistic mode (systemd as PID 1). It is a
// distinct combination because systemd boots the coturn package's coturn.service
// on 3478, which can shadow selkies-start's own TURN relay (a different
// realm/credentials from the generated turnserver.conf), and only a real browser
// negotiating over that relay proves whether media still connects.
func TestSelkiesE2EStreamSystemd(t *testing.T) {
	runSelkiesE2E(t, "systemd")
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
		t.Logf("attempt %d ice-timeline: %s", i, r.final.timeline())
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

	// Guard the selkies signalling-retry fix (see the addon install.sh patch):
	// pairing must be prompt. Unpatched, the server's "no peer" retry loop stalls
	// ~10s before it emits the WebRTC offer (setRemoteDescription), so the browser
	// only reaches ICE-connected after that. The ICE/TURN handshake itself is
	// ~150ms, so a healthy connect lands well under this bound; a regression
	// (blocking 2s retry back) blows straight past it.
	maxConnect := time.Duration(envInt("CSB_SELKIES_E2E_MAX_CONNECT_SECS", 4)) * time.Second
	for _, r := range results {
		if r.tICE < 0 || r.tICE > maxConnect {
			t.Errorf("attempt %d: ICE connected in %s, want <= %s (selkies signalling-retry stall?)",
				r.idx, r.tICE.Round(time.Millisecond), maxConnect)
		}
	}
}

// runAttempt opens a fresh tab, navigates to the Selkies client, and polls the
// pipeline until it renders a non-black frame or the deadline passes. Each call
// is an independent connect cycle (fresh tab → fresh WebRTC negotiation).
func runAttempt(t *testing.T, allocCtx context.Context, idx int, deadline time.Duration) attemptResult {
	t.Helper()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	res := attemptResult{idx: idx, tSignaling: notReached, tICE: notReached, tFirstFrame: notReached, tOK: notReached}

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
		chromedp.Navigate("http://localhost:8080/"),
	); err != nil {
		t.Logf("attempt %d navigate error: %v", idx, err)
		return res
	}

	start := time.Now()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	pollDeadline := time.Now().Add(deadline)
	// Success is proven by frames *advancing*, not by pixels: headless Chromium
	// with software rendering does not composite WebRTC <video> into canvas or
	// screenshots (both read back black even though frames decode), so a pixel
	// check is unreliable here. A live, advancing framesDecoded count also
	// directly catches the flakiness modes that matter — stalls and freezes.
	minAdvance := envInt("CSB_SELKIES_E2E_MIN_FRAME_ADVANCE", 30)
	baseFrames := -1
	var baseTime time.Time
	for {
		var p probe
		evalCtx, evalCancel := context.WithTimeout(ctx, 5*time.Second)
		err := chromedp.Run(evalCtx, chromedp.Evaluate(pollScript, &p,
			func(ep *runtime.EvaluateParams) *runtime.EvaluateParams { return ep.WithAwaitPromise(true) }))
		evalCancel()
		if err == nil {
			res.final = p
			el := time.Since(start)
			if p.PCs > 0 && res.tSignaling < 0 {
				res.tSignaling = el
			}
			if (p.Ice == "connected" || p.Ice == "completed") && res.tICE < 0 {
				res.tICE = el
			}
			if p.Frames > 0 && res.tFirstFrame < 0 {
				res.tFirstFrame = el
			}
			if p.Stage == "streaming" {
				if baseFrames < 0 {
					baseFrames, baseTime = p.Frames, time.Now()
				}
				if p.Cand == "succeeded" && p.Frames-baseFrames >= minAdvance && time.Since(baseTime) >= time.Second {
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
