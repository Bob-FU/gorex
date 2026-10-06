//go:build darwin || linux

package rex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Server owns the sessions.
type Server struct {
	mu       sync.Mutex
	sessions map[string]*session
	order    []string
	layout   json.RawMessage
	started  time.Time
	host     HostInfo
	hostDone chan struct{}
	ln       net.Listener
	exe      string
	exeTime  time.Time
	controls int
	idleFrom time.Time
	// ui is the control connection of the app's window that subscribed
	// last, which answers what command lines ask of the window; asked
	// are the events it is still to answer.
	ui       *control
	asked    map[int64]chan Answer
	eventSeq int64
	quit     chan struct{}
	quitOnce sync.Once
}

// idleTimeout is how long a server with no sessions and no app waits
// before it exits.
const idleTimeout = 10 * time.Minute

// Serve runs the server until it is shut down: it returns an error at
// once when another server already runs.
func Serve() error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("rex: a server already runs")
	}
	defer lock.Close()
	sock := SocketPath()
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600)
	s := &Server{
		sessions: map[string]*session{}, started: time.Now(), ln: ln,
		hostDone: make(chan struct{}), idleFrom: time.Now(), quit: make(chan struct{}),
		asked: map[int64]chan Answer{},
	}
	if b, err := os.ReadFile(filepath.Join(dir, "layout.json")); err == nil && json.Valid(b) {
		s.layout = b
	}
	s.exe, s.exeTime = Executable()
	linkCommand(s.exe)
	go func() {
		s.host = hostInfo()
		close(s.hostDone)
	}()
	go s.watchIdle()
	go func() {
		<-s.quit
		ln.Close()
	}()
	log.Printf("rex: serving on %s (pid %d)", sock, os.Getpid())
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				s.mu.Lock()
				all := make([]*session, 0, len(s.sessions))
				for _, ss := range s.sessions {
					all = append(all, ss)
				}
				s.mu.Unlock()
				for _, ss := range all {
					ss.kill()
				}
				os.Remove(sock)
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) watchIdle() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle := len(s.sessions) == 0 && s.controls == 0 && time.Since(s.idleFrom) > idleTimeout
		s.mu.Unlock()
		if idle {
			log.Print("rex: idle, exiting")
			s.shutdown()
			return
		}
	}
}

func (s *Server) shutdown() { s.quitOnce.Do(func() { close(s.quit) }) }

func (s *Server) handle(conn net.Conn) {
	r := bufio.NewReaderSize(conn, 64<<10)
	first, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return
	}
	var a Attach
	if json.Unmarshal(first, &a) == nil && a.Op == "attach" {
		s.mu.Lock()
		ss := s.sessions[a.SID]
		s.mu.Unlock()
		if ss == nil {
			conn.Write([]byte(`{"error":"no such session"}` + "\n"))
			conn.Close()
			return
		}
		conn.Write([]byte(`{"ok":true}` + "\n"))
		// What the reader buffered past the first line is input.
		if n := r.Buffered(); n > 0 {
			b, _ := r.Peek(n)
			ss.input(b)
		}
		ss.attach(conn, a.Cols, a.Rows)
		return
	}
	s.control(conn, r, first)
}

// control is a control connection.
type control struct {
	conn net.Conn
	wmu  sync.Mutex
}

func (c *control) send(res Response) {
	b, _ := json.Marshal(res)
	c.wmu.Lock()
	c.conn.Write(append(b, '\n'))
	c.wmu.Unlock()
}

// control serves a control connection: a request per line.
func (s *Server) control(conn net.Conn, r *bufio.Reader, first []byte) {
	cc := &control{conn: conn}
	s.mu.Lock()
	s.controls++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.controls--
		s.idleFrom = time.Now()
		if s.ui == cc {
			s.ui = nil
		}
		s.mu.Unlock()
		conn.Close()
	}()
	line := first
	for {
		var req Request
		if err := json.Unmarshal(line, &req); err == nil {
			switch req.Op {
			case "subscribe":
				s.mu.Lock()
				s.ui = cc
				s.mu.Unlock()
				cc.send(Response{ID: req.ID})
			case "kill", "split", "rename":
				// Kills wait for the session to end, and what the
				// window is asked for its answer: answer them aside.
				go func() { cc.send(s.request(req)) }()
			default:
				cc.send(s.request(req))
			}
		}
		var err error
		line, err = r.ReadBytes('\n')
		if err != nil {
			return
		}
	}
}

func (s *Server) request(req Request) Response {
	data, err := s.do(req)
	res := Response{ID: req.ID}
	if err != nil {
		res.Error = err.Error()
	} else if data != nil {
		res.Data, _ = json.Marshal(data)
	}
	return res
}

func (s *Server) session(id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.sessions[id]; ss != nil {
		return ss, nil
	}
	return nil, fmt.Errorf("no session %q", id)
}

func (s *Server) do(req Request) (any, error) {
	switch req.Op {
	case "hello":
		select {
		case <-s.hostDone:
		case <-time.After(3 * time.Second):
		}
		return Hello{Version: ProtocolVersion, PID: os.Getpid(), Started: s.started, Host: s.host, Exe: s.exe, ExeTime: s.exeTime}, nil
	case "list":
		s.mu.Lock()
		all := make([]*session, 0, len(s.order))
		for _, id := range s.order {
			all = append(all, s.sessions[id])
		}
		s.mu.Unlock()
		infos := make([]SessionInfo, len(all))
		for i, ss := range all {
			infos[i] = ss.info()
		}
		return infos, nil
	case "create":
		o := CreateOptions{}
		if req.Create != nil {
			o = *req.Create
		}
		id := NewID()
		ss, err := newSession(id, o)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.sessions[id] = ss
		s.order = append(s.order, id)
		s.mu.Unlock()
		return ss.info(), nil
	case "kill":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, nil // already gone
		}
		ss.kill()
		s.mu.Lock()
		delete(s.sessions, req.SID)
		s.order = slices.DeleteFunc(s.order, func(id string) bool { return id == req.SID })
		if len(s.sessions) == 0 {
			s.idleFrom = time.Now()
		}
		s.mu.Unlock()
		return nil, nil
	case "resize":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		ss.resize(req.Cols, req.Rows)
		return nil, nil
	case "getLayout":
		s.mu.Lock()
		l := s.layout
		s.mu.Unlock()
		if l == nil {
			return nil, nil
		}
		return l, nil
	case "setLayout":
		if !json.Valid(req.Layout) {
			return nil, errors.New("layout is not JSON")
		}
		l := bytes.Clone(req.Layout)
		s.mu.Lock()
		s.layout = l
		s.mu.Unlock()
		path := filepath.Join(Dir(), "layout.json")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, l, 0o600); err == nil {
			os.Rename(tmp, path)
		}
		return nil, nil
	case "send":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		if !ss.input([]byte(req.Text)) {
			return nil, fmt.Errorf("session %q has exited", req.SID)
		}
		return nil, nil
	case "read":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"text": ss.text(req.Scrollback, req.Lines)}, nil
	case "split", "rename":
		if _, err := s.session(req.SID); err != nil {
			return nil, err
		}
		return s.ask(Event{Op: req.Op, SID: req.SID, Split: req.Split, Rename: req.Rename})
	case "answer":
		if req.Answer == nil {
			return nil, errors.New("no answer")
		}
		s.mu.Lock()
		ch := s.asked[req.Answer.Seq]
		delete(s.asked, req.Answer.Seq)
		s.mu.Unlock()
		if ch != nil {
			ch <- *req.Answer
		}
		return nil, nil
	case "shutdown":
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.shutdown()
		}()
		return nil, nil
	}
	return nil, fmt.Errorf("unknown op %q", req.Op)
}

// ask asks the app's window, and waits for its answer.
func (s *Server) ask(ev Event) (any, error) {
	ch := make(chan Answer, 1)
	s.mu.Lock()
	ui := s.ui
	if ui == nil {
		s.mu.Unlock()
		return nil, errors.New("no GoRex window is open")
	}
	s.eventSeq++
	ev.Seq = s.eventSeq
	s.asked[ev.Seq] = ch
	s.mu.Unlock()
	ui.send(Response{Event: &ev})
	select {
	case a := <-ch:
		if a.Error != "" {
			return nil, errors.New(a.Error)
		}
		return a.Data, nil
	case <-time.After(10 * time.Second):
		s.mu.Lock()
		delete(s.asked, ev.Seq)
		s.mu.Unlock()
		return nil, errors.New("the GoRex window did not answer")
	}
}

// linkCommand links the gorex command, in BinDir, to the server's
// executable.
func linkCommand(exe string) {
	if exe == "" {
		return
	}
	dir := BinDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	link := filepath.Join(dir, "gorex")
	if cur, err := os.Readlink(link); err == nil && cur == exe {
		return
	}
	tmp := link + ".tmp"
	os.Remove(tmp)
	if os.Symlink(exe, tmp) == nil {
		os.Rename(tmp, link)
	}
}

// hostInfo describes this machine; it may take a second.
func hostInfo() HostInfo {
	h := HostInfo{}
	if u, err := user.Current(); err == nil {
		h.User = u.Username
		h.Home = u.HomeDir
	}
	out := func(name string, args ...string) string {
		b, err := exec.Command(name, args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if runtime.GOOS == "darwin" {
		h.Name = out("scutil", "--get", "ComputerName")
		h.OS = "macOS " + out("sw_vers", "-productVersion")
		h.Chip = sysctlString("machdep.cpu.brand_string")
		h.Memory = sysctlUint64("hw.memsize")
		var hw struct {
			Data []struct {
				MachineName string `json:"machine_name"`
				ChipType    string `json:"chip_type"`
			} `json:"SPHardwareDataType"`
		}
		if json.Unmarshal([]byte(out("system_profiler", "SPHardwareDataType", "-json", "-detailLevel", "mini")), &hw) == nil && len(hw.Data) > 0 {
			h.Model = hw.Data[0].MachineName
			if hw.Data[0].ChipType != "" {
				h.Chip = hw.Data[0].ChipType
			}
		}
		if h.Model == "" {
			h.Model = sysctlString("hw.model")
		}
	} else {
		h.Name, _ = os.Hostname()
		h.OS = runtime.GOOS
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
					h.OS, _ = strconv.Unquote(v)
				}
			}
		}
		h.Model = "Linux"
	}
	if h.Name == "" {
		h.Name, _ = os.Hostname()
		h.Name = strings.TrimSuffix(h.Name, ".local")
	}
	return h
}

// Spawn starts a server in the background, as a process of its own that
// outlives the app: the executable run with -server.
func Spawn() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(Dir(), "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(exe, "-server")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
