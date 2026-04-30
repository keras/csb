package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"csb-host/internal/allowlist"
	"csb-host/internal/broker"
)

type allowFlags []string

func (a *allowFlags) String() string { return fmt.Sprint([]string(*a)) }
func (a *allowFlags) Set(v string) error {
	*a = append(*a, v)
	return nil
}

func main() {
	bind := flag.String("bind", "127.0.0.1:0", "listen address (port 0 = auto-assign)")
	var allows allowFlags
	flag.Var(&allows, "allow", "allowed command pattern, repeatable: \"cmd arg1 **\"")
	flag.Parse()

	rules, err := allowlist.ParseAll([]string(allows))
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-broker: %v\n", err)
		os.Exit(1)
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-broker: generate token: %v\n", err)
		os.Exit(1)
	}
	token := hex.EncodeToString(tokenBytes)

	ln, err := net.Listen("tcp", *bind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-broker: listen %s: %v\n", *bind, err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// Print ready signal for the parent process (csb) to read.
	info, _ := json.Marshal(map[string]any{"port": port, "token": token})
	fmt.Println(string(info))

	srv := broker.NewServer(token, rules)
	if err := http.Serve(ln, srv); err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-broker: serve: %v\n", err)
		os.Exit(1)
	}
}
