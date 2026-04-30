package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"csb-host/internal/proto"
	"nhooyr.io/websocket"
)

// fakeServer runs a minimal WebSocket server that acts like the broker for testing.
type fakeServer struct {
	handler func(conn *websocket.Conn, r *http.Request)
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.handler(conn, r)
}

func startFake(t *testing.T, handler func(*websocket.Conn, *http.Request)) (url, token string) {
	t.Helper()
	ts := httptest.NewServer(&fakeServer{handler: handler})
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http"), "test-token"
}

func readFrame(t *testing.T, conn *websocket.Conn) proto.Frame {
	t.Helper()
	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("fake server read: %v", err)
	}
	f, err := proto.Decode(data)
	if err != nil {
		t.Fatalf("fake server decode: %v", err)
	}
	return f
}

func sendFrame(t *testing.T, conn *websocket.Conn, f proto.Frame) {
	t.Helper()
	data, _ := proto.Encode(f)
	conn.Write(context.Background(), websocket.MessageText, data)
}

func noStdin() *bytes.Reader { return bytes.NewReader(nil) }

func TestRunExitCode(t *testing.T) {
	url, token := startFake(t, func(conn *websocket.Conn, r *http.Request) {
		f := readFrame(t, conn)
		if f.Type != "start" || f.Cmd != "myapp" {
			t.Errorf("expected start myapp, got %+v", f)
		}
		sendFrame(t, conn, proto.NewExit(42))
		conn.Close(websocket.StatusNormalClosure, "")
	})

	code, err := Run(url, token, "myapp", []string{}, nil, noStdin(), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 42 {
		t.Errorf("exit code: got %d, want 42", code)
	}
}

func TestRunStdoutStderr(t *testing.T) {
	url, token := startFake(t, func(conn *websocket.Conn, r *http.Request) {
		readFrame(t, conn) // consume start
		sendFrame(t, conn, proto.NewStdout([]byte("out")))
		sendFrame(t, conn, proto.NewStderr([]byte("err")))
		sendFrame(t, conn, proto.NewExit(0))
		conn.Close(websocket.StatusNormalClosure, "")
	})

	var stdout, stderr bytes.Buffer
	code, runErr := Run(url, token, "cmd", nil, nil, noStdin(), &stdout, &stderr)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if code != 0 {
		t.Errorf("code: got %d, want 0", code)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout: got %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr: got %q, want %q", stderr.String(), "err")
	}
}

func TestRunErrorFrame(t *testing.T) {
	url, token := startFake(t, func(conn *websocket.Conn, r *http.Request) {
		readFrame(t, conn)
		sendFrame(t, conn, proto.NewError("not_whitelisted", "nope"))
		conn.Close(websocket.StatusNormalClosure, "")
	})

	code, err := Run(url, token, "rm", []string{"-rf", "/"}, nil, noStdin(), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 126 {
		t.Errorf("exit code: got %d, want 126", code)
	}
}

func TestRunAuthHeader(t *testing.T) {
	var gotAuth string
	url, token := startFake(t, func(conn *websocket.Conn, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		sendFrame(t, conn, proto.NewExit(0))
		conn.Close(websocket.StatusNormalClosure, "")
	})

	Run(url, token, "cmd", nil, nil, noStdin(), &bytes.Buffer{}, &bytes.Buffer{})

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer test-token")
	}
}
