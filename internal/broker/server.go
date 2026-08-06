package broker

import (
	"context"
	"net/http"

	"github.com/keras/csb/internal/allowlist"
	"github.com/keras/csb/internal/proto"

	"nhooyr.io/websocket"
)

// Server is an HTTP handler that upgrades to WebSocket and runs whitelisted host commands.
type Server struct {
	token string
	rules []allowlist.Rule
	// workspace is the host directory mounted as the sandbox workspace. It is the
	// only subtree a client may pick a working directory from; empty disables the
	// feature (commands then run in the broker's own cwd).
	workspace string
}

func NewServer(token string, rules []allowlist.Rule, workspace string) *Server {
	return &Server{token: token, rules: rules, workspace: workspace}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // localhost only; no Origin check needed
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(32 << 20) // 32 MiB — accommodate large stdin frames

	s.handle(conn, r)
}

func (s *Server) handle(conn *websocket.Conn, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "read error")
		return
	}

	f, err := proto.Decode(data)
	if err != nil || f.Type != "start" {
		sendFrame(ctx, conn, proto.NewError("invalid_request", "expected start frame"))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}

	if !allowlist.Match(s.rules, f.Cmd, f.Args) {
		sendFrame(ctx, conn, proto.NewError("not_whitelisted", "command not permitted: "+f.Cmd))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}

	dir, err := resolveCwd(s.workspace, f.Cwd)
	if err != nil {
		sendFrame(ctx, conn, proto.NewError("invalid_cwd", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}

	runCommand(ctx, cancel, conn, startFrame{
		cmd:  f.Cmd,
		args: f.Args,
		dir:  dir,
		tty:  f.Tty,
		cols: f.Cols,
		rows: f.Rows,
	})
}

func sendFrame(ctx context.Context, conn *websocket.Conn, f proto.Frame) {
	data, err := proto.Encode(f)
	if err != nil {
		return
	}
	conn.Write(ctx, websocket.MessageText, data)
}
