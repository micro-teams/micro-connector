// Command testserver is the reference control plane: the smallest program that speaks the whole
// contract a connector expects, and the other end of the end-to-end test.
//
// It serves three things. Enrolment, which approves every machine at once because the shape of the
// exchange is the contract and approval policy is not. The link, a WebSocket per machine carrying
// the message set of docs/protocol.md. And a test interface that lets e2e.sh drive screens and read
// back everything the machine has said. All state is derived from messages the machine sent.
//
//	POST /machine/enroll/start   -> {"code", "approveUrl", "interval"}
//	POST /machine/enroll/poll    -> {"status": "approved", "machineId", "token"}
//	GET  /link                   WebSocket; the machine authenticates with the
//	                             X-Microteams-Session header carrying its token
//	POST /test/open              {"machine", "sid", "applet", "command", "env"} -> session.create
//	POST /test/call              {"machine", "sid", "name", "args"} -> {"id"}; sends rpc.call
//	GET  /test/state             {"screens": {sid: {"ready", "driver", "error", "vars"}},
//	                              "rpc": {id: {"value", "error"}}}
//
// Flags: -addr (default 127.0.0.1:8099), -applets (directory of built drivers, default applets/dist).
// Example: go run ./server -applets ../applets/dist
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

type screen struct {
	Ready  bool           `json:"ready"`
	Driver string         `json:"driver,omitempty"`
	Error  string         `json:"error,omitempty"`
	Vars   map[string]any `json:"vars,omitempty"`
}

type rpcResult struct {
	Value any    `json:"value"`
	Error string `json:"error,omitempty"`
}

type server struct {
	applets string

	mu       sync.Mutex
	tokens   map[string]string // token -> machine id
	links    map[string]*link  // machine id -> its live link
	screens  map[string]*screen
	rpc      map[string]rpcResult
	upgrader websocket.Upgrader
}

// link is one machine's WebSocket. Writes are serialised because the test interface and the reply
// to an applet's rpc.call can write at the same time.
type link struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (l *link) send(m protocol.Msg) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conn.WriteJSON(m)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address")
	applets := flag.String("applets", "applets/dist", "directory of built drivers")
	flag.Parse()
	s := &server{
		applets: *applets,
		tokens:  map[string]string{},
		links:   map[string]*link{},
		screens: map[string]*screen{},
		rpc:     map[string]rpcResult{},
	}
	http.HandleFunc("POST /machine/enroll/start", s.enrollStart)
	http.HandleFunc("POST /machine/enroll/poll", s.enrollPoll)
	http.HandleFunc("GET /link", s.link)
	http.HandleFunc("POST /test/open", s.open)
	http.HandleFunc("POST /test/call", s.call)
	http.HandleFunc("GET /test/state", s.state)
	log.Printf("reference control plane on %s, drivers from %s", *addr, *applets)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func (s *server) enrollStart(w http.ResponseWriter, r *http.Request) {
	code := newID(3)
	writeJSON(w, map[string]any{"code": code, "approveUrl": "http://" + r.Host + "/approve/" + code, "interval": 1})
}

func (s *server) enrollPoll(w http.ResponseWriter, _ *http.Request) {
	id, token := "m-"+newID(4), newID(16)
	s.mu.Lock()
	s.tokens[token] = id
	s.mu.Unlock()
	writeJSON(w, map[string]any{"status": "approved", "machineId": id, "token": token})
}

func (s *server) link(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	id, ok := s.tokens[r.Header.Get("X-Microteams-Session")]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown machine token", http.StatusUnauthorized)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade %s: %v", id, err)
		return
	}
	l := &link{conn: conn}
	s.mu.Lock()
	s.links[id] = l
	s.mu.Unlock()
	log.Printf("machine %s connected", id)
	for {
		var m protocol.Msg
		if err := conn.ReadJSON(&m); err != nil {
			log.Printf("machine %s disconnected: %v", id, err)
			return
		}
		s.receive(l, m)
	}
}

// receive is the whole control-plane side of the protocol: every message a machine can send, and
// what a control plane records or answers for it.
func (s *server) receive(l *link, m protocol.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.screens[m.Sid]
	if sc == nil && m.Sid != "" {
		sc = &screen{Vars: map[string]any{}}
		s.screens[m.Sid] = sc
	}
	switch m.T {
	case "hello":
		if err := l.send(protocol.Msg{T: "welcome", V: protocol.Version}); err != nil {
			log.Printf("welcome: %v", err)
		}
	case "session.ready":
		sc.Ready = true
	case "session.error":
		sc.Error = m.Error
	case "var.push":
		sc.Vars[m.Name] = m.Value
	case "rpc.call":
		// screenReady is the call a driver makes on startup to say what it is. Every call must be
		// answered, or the applet waits for ever.
		if m.Name == "screenReady" && len(m.Args) > 0 {
			if info, ok := m.Args[0].(map[string]any); ok {
				sc.Driver, _ = info["driver"].(string)
			}
		}
		if err := l.send(protocol.Msg{T: "rpc.result", Sid: m.Sid, ID: m.ID, Value: map[string]bool{"ok": true}}); err != nil {
			log.Printf("rpc.result: %v", err)
		}
	case "rpc.result":
		s.rpc[m.ID] = rpcResult{Value: m.Value, Error: m.Error}
	}
}

// machineLink returns the live link of a machine, or an error the test interface reports.
func (s *server) machineLink(id string) (*link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[id]
	if !ok {
		return nil, fmt.Errorf("machine %q is not connected", id)
	}
	return l, nil
}

func (s *server) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Machine, Sid, Applet string
		Command              []string
		Env                  map[string]string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src, err := os.ReadFile(filepath.Join(s.applets, req.Applet+".js"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l, err := s.machineLink(req.Machine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	msg := protocol.Msg{T: "session.create", Sid: req.Sid, Command: req.Command, Env: req.Env,
		Source: string(src), Cols: 120, Rows: 32}
	if err := l.send(msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) call(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Machine, Sid, Name string
		Args               []any
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l, err := s.machineLink(req.Machine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	id := newID(4)
	if err := l.send(protocol.Msg{T: "rpc.call", Sid: req.Sid, ID: id, Name: req.Name, Args: req.Args}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *server) state(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{"screens": s.screens, "rpc": s.rpc})
}
