package rex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Client is a control connection to the server.
type Client struct {
	conn net.Conn

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan Response
	err     error
	closed  chan struct{}
	// onEvent is called with what the server asks of the app's window,
	// once it subscribed, from the reading goroutine.
	onEvent func(Event)
}

// Dial connects to the server at the socket path, and fails when none
// runs there, as the command line does.
func Dial(path string) (*Client, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

func newClient(conn net.Conn) *Client {
	c := &Client{conn: conn, pending: map[int64]chan Response{}, closed: make(chan struct{})}
	go c.read()
	return c
}

// Connect connects to the server, starting one in the background when
// none runs.
func Connect() (*Client, error) {
	conn, err := net.Dial("unix", SocketPath())
	if err != nil {
		if err := Spawn(); err != nil {
			return nil, fmt.Errorf("rex: starting the server: %w", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			time.Sleep(30 * time.Millisecond)
			conn, err = net.Dial("unix", SocketPath())
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("rex: connecting to the server: %w", err)
			}
		}
	}
	return newClient(conn), nil
}

// Executable returns the path of the running executable and when it was
// modified last.
func Executable() (string, time.Time) {
	exe, err := os.Executable()
	if err != nil {
		return "", time.Time{}
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	st, err := os.Stat(exe)
	if err != nil {
		return exe, time.Time{}
	}
	return exe, st.ModTime()
}

// Restart shuts the server down, ending its sessions, waits for it to
// exit, and connects to a new one.
func (c *Client) Restart(pid int) (*Client, error) {
	c.Shutdown()
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for pid > 0 && processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return Connect()
}

// Closed is closed once the connection to the server is lost.
func (c *Client) Closed() <-chan struct{} { return c.closed }

func (c *Client) read() {
	r := bufio.NewReaderSize(c.conn, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				ch <- Response{ID: id, Error: "connection to the server lost"}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			close(c.closed)
			return
		}
		var res Response
		if json.Unmarshal(line, &res) != nil {
			continue
		}
		if res.Event != nil {
			c.mu.Lock()
			fn := c.onEvent
			c.mu.Unlock()
			if fn != nil {
				fn(*res.Event)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[res.ID]
		delete(c.pending, res.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}
}

// call sends a request and decodes the response's data into out.
func (c *Client) call(req Request, out any) error {
	ch := make(chan Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return errors.New("rex: connection to the server lost")
	}
	c.nextID++
	req.ID = c.nextID
	c.pending[req.ID] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(req)
	c.wmu.Lock()
	_, err := c.conn.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return err
	}
	var res Response
	select {
	case res = <-ch:
	case <-time.After(15 * time.Second):
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return fmt.Errorf("rex: %s timed out", req.Op)
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	if out != nil && len(res.Data) > 0 {
		return json.Unmarshal(res.Data, out)
	}
	return nil
}

func (c *Client) Hello() (h Hello, err error) {
	err = c.call(Request{Op: "hello"}, &h)
	return
}

func (c *Client) List() (s []SessionInfo, err error) {
	err = c.call(Request{Op: "list"}, &s)
	return
}

func (c *Client) Create(o CreateOptions) (s SessionInfo, err error) {
	err = c.call(Request{Op: "create", Create: &o}, &s)
	return
}

func (c *Client) Kill(sid string) error { return c.call(Request{Op: "kill", SID: sid}, nil) }

func (c *Client) Resize(sid string, cols, rows int) error {
	return c.call(Request{Op: "resize", SID: sid, Cols: cols, Rows: rows}, nil)
}

func (c *Client) Layout() (json.RawMessage, error) {
	var l json.RawMessage
	err := c.call(Request{Op: "getLayout"}, &l)
	return l, err
}

func (c *Client) SetLayout(l json.RawMessage) error {
	return c.call(Request{Op: "setLayout", Layout: l}, nil)
}

// Subscribe makes this connection the app's window's: fn is called, from
// the reading goroutine, with what command lines ask of the window, which
// it answers with Answer.
func (c *Client) Subscribe(fn func(Event)) error {
	c.mu.Lock()
	c.onEvent = fn
	c.mu.Unlock()
	return c.call(Request{Op: "subscribe"}, nil)
}

// Answer answers the event seq with data, or err.
func (c *Client) Answer(seq int64, data any, err error) error {
	a := &Answer{Seq: seq}
	if err != nil {
		a.Error = err.Error()
	} else if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		a.Data = b
	}
	return c.call(Request{Op: "answer", Answer: a}, nil)
}

// Send types text into the session sid.
func (c *Client) Send(sid, text string) error {
	return c.call(Request{Op: "send", SID: sid, Text: text}, nil)
}

// ReadScreen returns the text of the session's screen, or with scrollback
// its last lines lines (all of them when lines is 0).
func (c *Client) ReadScreen(sid string, scrollback bool, lines int) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := c.call(Request{Op: "read", SID: sid, Scrollback: scrollback, Lines: lines}, &out)
	return out.Text, err
}

// Split asks the window to split the pane of the session sid, and
// returns the new pane's session.
func (c *Client) Split(sid string, o SplitOptions) (s SessionInfo, err error) {
	err = c.call(Request{Op: "split", SID: sid, Split: &o}, &s)
	return
}

// Rename asks the window to name the pane of the session sid, or its tab.
func (c *Client) Rename(sid string, o RenameOptions) error {
	return c.call(Request{Op: "rename", SID: sid, Rename: &o}, nil)
}

func (c *Client) Shutdown() error { return c.call(Request{Op: "shutdown"}, nil) }

func (c *Client) Close() error { return c.conn.Close() }

// Stream is a session attached to, for a terminal's Conn: it attaches
// once it knows the size of the screen, from the first Resize, or after
// a moment at the size it had last.
type Stream struct {
	c        *Client
	sid      string
	attachMu sync.Mutex // one attach at a time
	mu       sync.Mutex
	cond     *sync.Cond
	conn     net.Conn
	err      error
	closed   bool
	// cols and rows are the terminal's size; sentCols and sentRows the
	// size the server was told last.
	cols, rows         int
	sentCols, sentRows int

	// OnData, when set, is called with the size of what the session
	// prints, from the reading goroutine.
	OnData func(n int)
}

// Stream returns a stream of the session sid, which attaches lazily.
func (c *Client) Stream(sid string, cols, rows int) *Stream {
	s := &Stream{c: c, sid: sid, cols: cols, rows: rows}
	s.cond = sync.NewCond(&s.mu)
	time.AfterFunc(400*time.Millisecond, func() {
		s.attach()
		s.sync()
	})
	return s
}

// attach attaches to the session at the terminal's size, once.
func (s *Stream) attach() {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	s.mu.Lock()
	if s.conn != nil || s.err != nil || s.closed {
		s.mu.Unlock()
		return
	}
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	conn, err := net.Dial("unix", SocketPath())
	if err == nil {
		b, _ := json.Marshal(Attach{Op: "attach", SID: s.sid, Cols: cols, Rows: rows})
		if _, err = conn.Write(append(b, '\n')); err == nil {
			err = readOK(conn)
		}
		if err != nil {
			conn.Close()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if err == nil {
			conn.Close()
		}
		return
	}
	if err != nil {
		s.err = err
	} else {
		s.conn, s.sentCols, s.sentRows = conn, cols, rows
	}
	s.cond.Broadcast()
}

// sync tells the server the terminal's size, once attached, when it was
// told another.
func (s *Stream) sync() error {
	s.mu.Lock()
	if s.conn == nil || s.cols == s.sentCols && s.rows == s.sentRows {
		s.mu.Unlock()
		return nil
	}
	cols, rows := s.cols, s.rows
	s.sentCols, s.sentRows = cols, rows
	s.mu.Unlock()
	return s.c.Resize(s.sid, cols, rows)
}

// readOK reads the server's answer to an attach, a byte at a time so as
// to read nothing past it.
func readOK(conn net.Conn) error {
	var line []byte
	b := make([]byte, 1)
	for {
		if _, err := conn.Read(b); err != nil {
			return err
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
		if len(line) > 4096 {
			return errors.New("rex: bad answer")
		}
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Error)
	}
	return nil
}

func (s *Stream) wait() (net.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.conn == nil && s.err == nil && !s.closed {
		s.cond.Wait()
	}
	if s.closed {
		return nil, io.EOF
	}
	return s.conn, s.err
}

func (s *Stream) Read(p []byte) (int, error) {
	conn, err := s.wait()
	if err != nil {
		return 0, err
	}
	n, err := conn.Read(p)
	if n > 0 && s.OnData != nil {
		s.OnData(n)
	}
	return n, err
}

func (s *Stream) Write(p []byte) (int, error) {
	conn, err := s.wait()
	if err != nil {
		return 0, err
	}
	return conn.Write(p)
}

// Resize tells the session the size of the screen, attaching first.
func (s *Stream) Resize(cols, rows int) error {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	s.attach()
	return s.sync()
}

// Close detaches; the session goes on.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.closed = true
	conn := s.conn
	s.cond.Broadcast()
	s.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// Size returns the size the session was last given.
func (s *Stream) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}
