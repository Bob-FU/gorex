// Package rex is GoRex's session server and its client.
//
// The server owns the pseudo-terminals: shells keep running when the app
// quits, and the next launch attaches to them again, with what they printed
// replayed. The app talks to it over a Unix socket: a control connection of
// JSON lines (requests and their responses), and one connection per
// attached session that carries the terminal's bytes both ways.
package rex

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"
)

// ProtocolVersion changes whenever the wire protocol does.
const ProtocolVersion = 2

// Request is a line of the control connection, from the client.
type Request struct {
	ID     int64           `json:"id"`
	Op     string          `json:"op"`
	SID    string          `json:"sid,omitempty"`
	Cols   int             `json:"cols,omitempty"`
	Rows   int             `json:"rows,omitempty"`
	Create *CreateOptions  `json:"create,omitempty"`
	Layout json.RawMessage `json:"layout,omitempty"`
	// Text is what "send" types into a session.
	Text string `json:"text,omitempty"`
	// Scrollback and Lines choose what "read" returns: the screen, or the
	// last Lines lines of the scrollback and screen.
	Scrollback bool `json:"scrollback,omitempty"`
	Lines      int  `json:"lines,omitempty"`
	// Split and Rename ask the app's window, which "answer" answers.
	Split  *SplitOptions  `json:"split,omitempty"`
	Rename *RenameOptions `json:"rename,omitempty"`
	Answer *Answer        `json:"answer,omitempty"`
}

// SplitOptions split the pane of the session SID.
type SplitOptions struct {
	// Direction is where the new pane goes: right, down, left or up.
	Direction string `json:"direction"`
	// Focus gives the new pane the focus.
	Focus bool `json:"focus,omitempty"`
}

// RenameOptions name the pane of the session SID, or its tab when Tab is
// set; an empty Title gives back the name the pane or tab had by itself.
type RenameOptions struct {
	Title string `json:"title"`
	Tab   bool   `json:"tab,omitempty"`
}

// Event is a line the server sends the app's window on its control
// connection, once it subscribed: what a command line asks of the
// window, which it answers with an "answer" request of the same Seq.
type Event struct {
	Seq    int64          `json:"seq"`
	Op     string         `json:"op"` // "split" or "rename"
	SID    string         `json:"sid"`
	Split  *SplitOptions  `json:"split,omitempty"`
	Rename *RenameOptions `json:"rename,omitempty"`
}

// Answer answers an Event.
type Answer struct {
	Seq   int64           `json:"seq"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Response answers the Request with the same ID; a line with an Event
// instead is the server's, to the app's window.
type Response struct {
	ID    int64           `json:"id"`
	Event *Event          `json:"event,omitempty"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// CreateOptions start a session.
type CreateOptions struct {
	// Command runs instead of the user's login shell.
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Workspace is the ID of the tab the session opens in, which its
	// shell finds in GOREX_WORKSPACE_ID.
	Workspace string `json:"workspace,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
}

// SessionInfo describes a session and what runs in it now.
type SessionInfo struct {
	ID    string `json:"id"`
	PID   int    `json:"pid"`
	Shell string `json:"shell"`
	// Workspace is the ID of the tab the session opened in.
	Workspace string    `json:"workspace,omitempty"`
	Created   time.Time `json:"created"`
	// Program is the name of the program in the foreground, and Args its
	// arguments; Idle tells that it is the shell itself, at its prompt.
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
	Idle    bool     `json:"idle"`
	// Dir is the working directory of the program in the foreground.
	Dir string `json:"dir"`
	// Title is the last title a program set (OSC 0, OSC 2).
	Title      string    `json:"title,omitempty"`
	LastOutput time.Time `json:"lastOutput"`
	LastInput  time.Time `json:"lastInput"`
	Output     uint64    `json:"output"`
	Bells      int       `json:"bells"`
	Exited     bool      `json:"exited"`
	ExitCode   int       `json:"exitCode"`
	Attached   int       `json:"attached"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
}

// Hello is the server's answer to "hello".
type Hello struct {
	Version int       `json:"version"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Host    HostInfo  `json:"host"`
	// Exe is the server's executable, and ExeTime when it was modified
	// last as the server started: a development build compares them with
	// its own, to replace a server of an older build.
	Exe     string    `json:"exe,omitempty"`
	ExeTime time.Time `json:"exeTime,omitzero"`
}

// HostInfo describes the machine the server runs on.
type HostInfo struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Chip   string `json:"chip"`
	Memory uint64 `json:"memory"`
	OS     string `json:"os"`
	User   string `json:"user"`
	Home   string `json:"home"`
}

// Attach is the first line of a connection attaching to a session.
type Attach struct {
	Op   string `json:"op"` // "attach"
	SID  string `json:"sid"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// Dir returns the directory of the server's socket, log and state.
func Dir() string {
	if d := os.Getenv("GOREX_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "GoRex")
}

// BinDir returns the directory of the gorex command, a link to the app
// that the server makes, which its sessions have in PATH.
func BinDir() string { return filepath.Join(Dir(), "bin") }

// SocketPath returns the path of the server's socket: in Dir, unless that
// is too long a path for a socket, then in the temporary directory.
func SocketPath() string {
	p := filepath.Join(Dir(), "server.sock")
	if len(p) < 100 {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(p))
	return filepath.Join(os.TempDir(), fmt.Sprintf("gorex-%d-%x.sock", os.Getuid(), h.Sum32()))
}

// NewID returns a random UUID, the ID of a new session or tab, as
// terminals' command lines name their surfaces and workspaces.
func NewID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
