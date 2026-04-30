package broker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"csb-host/internal/allowlist"
	"csb-host/internal/proto"
	"nhooyr.io/websocket"
)

func dial(t *testing.T, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/run"
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) proto.Frame {
	t.Helper()
	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := proto.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return f
}

func writeFrame(t *testing.T, conn *websocket.Conn, f proto.Frame) {
	t.Helper()
	data, err := proto.Encode(f)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func newTestServer(token string, rules []allowlist.Rule) *httptest.Server {
	return httptest.NewServer(NewServer(token, rules))
}

func TestEchoCommand(t *testing.T) {
	token := "test-token"
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	ts := newTestServer(token, rules)
	defer ts.Close()

	conn := dial(t, ts, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeFrame(t, conn, proto.NewStart("echo", []string{"hello", "world"}))
	// close stdin immediately
	writeFrame(t, conn, proto.NewStdin(nil))

	var stdout strings.Builder
	for {
		f := readFrame(t, conn)
		switch f.Type {
		case "stdout":
			stdout.Write(f.Data)
		case "stderr":
			// ignore
		case "exit":
			if f.ExitCode == nil || *f.ExitCode != 0 {
				t.Errorf("exit code: got %v, want 0", f.ExitCode)
			}
			if got := strings.TrimRight(stdout.String(), "\n"); got != "hello world" {
				t.Errorf("stdout: got %q, want %q", got, "hello world")
			}
			return
		case "error":
			t.Fatalf("unexpected error frame: %s %s", f.ErrCode, f.Message)
		}
	}
}

func TestAuthRejection(t *testing.T) {
	ts := newTestServer("correct-token", nil)
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/run"
	_, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer wrong-token"}},
	})
	if err == nil {
		t.Fatal("expected dial to fail with wrong token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got: %v", resp)
	}
}

func TestAllowlistDenial(t *testing.T) {
	token := "tok"
	rules := []allowlist.Rule{{Cmd: "echo", Args: []string{"**"}}}
	ts := newTestServer(token, rules)
	defer ts.Close()

	conn := dial(t, ts, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeFrame(t, conn, proto.NewStart("rm", []string{"-rf", "/"}))

	f := readFrame(t, conn)
	if f.Type != "error" {
		t.Fatalf("expected error frame, got %q", f.Type)
	}
	if f.ErrCode != "not_whitelisted" {
		t.Errorf("err_code: got %q, want %q", f.ErrCode, "not_whitelisted")
	}
}

func TestStdinPiping(t *testing.T) {
	token := "tok"
	rules := []allowlist.Rule{{Cmd: "cat", Args: []string{}}}
	ts := newTestServer(token, rules)
	defer ts.Close()

	conn := dial(t, ts, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeFrame(t, conn, proto.NewStart("cat", []string{}))
	writeFrame(t, conn, proto.NewStdin([]byte("ping")))
	writeFrame(t, conn, proto.NewStdin(nil)) // EOF

	var stdout strings.Builder
	for {
		f := readFrame(t, conn)
		switch f.Type {
		case "stdout":
			stdout.Write(f.Data)
		case "exit":
			if got := stdout.String(); got != "ping" {
				t.Errorf("stdout: got %q, want %q", got, "ping")
			}
			return
		case "error":
			t.Fatalf("error: %s %s", f.ErrCode, f.Message)
		}
	}
}

func TestProcessKilledOnClientDisconnect(t *testing.T) {
	// Verifies that a running host process is terminated when the WS client
	// disconnects abruptly (no graceful close handshake).
	token := "tok"
	rules := []allowlist.Rule{{Cmd: "sleep", Args: []string{"*"}}}

	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	srv := NewServer(token, rules)
	mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r)
		close(handlerDone)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/run",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}

	writeFrame(t, conn, proto.NewStart("sleep", []string{"60"}))

	// Give the process a moment to start, then drop the connection without a close frame.
	time.Sleep(50 * time.Millisecond)
	conn.CloseNow()

	select {
	case <-handlerDone:
		// Good: context cancel propagated and the handler returned.
	case <-time.After(2 * time.Second):
		t.Fatal("host process not killed after client disconnect (handler still running)")
	}
}

func TestLargeStdinFullyWritten(t *testing.T) {
	// Verifies that stdin data larger than a single pipe buffer is fully forwarded
	// to the host process without partial-write truncation.
	token := "tok"
	rules := []allowlist.Rule{{Cmd: "cat", Args: []string{}}}
	ts := newTestServer(token, rules)
	defer ts.Close()

	conn := dial(t, ts, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 64 KiB — well above typical pipe buffer and ws read buffer sizes.
	const size = 64 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	writeFrame(t, conn, proto.NewStart("cat", []string{}))
	writeFrame(t, conn, proto.NewStdin(payload))
	writeFrame(t, conn, proto.NewStdin(nil)) // EOF

	var got []byte
	for {
		f := readFrame(t, conn)
		switch f.Type {
		case "stdout":
			got = append(got, f.Data...)
		case "exit":
			if len(got) != size {
				t.Errorf("stdout length: got %d, want %d", len(got), size)
			}
			return
		case "error":
			t.Fatalf("error: %s %s", f.ErrCode, f.Message)
		}
	}
}

func TestExitCode(t *testing.T) {
	token := "tok"
	rules := []allowlist.Rule{{Cmd: "sh", Args: []string{"-c", "*"}}}
	ts := newTestServer(token, rules)
	defer ts.Close()

	conn := dial(t, ts, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeFrame(t, conn, proto.NewStart("sh", []string{"-c", "exit 42"}))
	writeFrame(t, conn, proto.NewStdin(nil))

	for {
		f := readFrame(t, conn)
		if f.Type == "exit" {
			if f.ExitCode == nil || *f.ExitCode != 42 {
				t.Errorf("exit code: got %v, want 42", f.ExitCode)
			}
			return
		}
	}
}
