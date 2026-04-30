package broker

import (
	"context"
	"net/http"

	"csb-host/internal/allowlist"
	"csb-host/internal/proto"
	"nhooyr.io/websocket"
)

// Server is an HTTP handler that upgrades to WebSocket and runs whitelisted host commands.
type Server struct {
	token string
	rules []allowlist.Rule
}

func NewServer(token string, rules []allowlist.Rule) *Server {
	return &Server{token: token, rules: rules}
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

	runCommand(ctx, cancel, conn, f.Cmd, f.Args)
}

func sendFrame(ctx context.Context, conn *websocket.Conn, f proto.Frame) {
	data, err := proto.Encode(f)
	if err != nil {
		return
	}
	conn.Write(ctx, websocket.MessageText, data)
}
