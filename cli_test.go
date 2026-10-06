package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"gorex/internal/rex"
)

// cmd runs a command of the command line while the view runs frames, as
// the window answers what it asks between them.
func cmd(t *testing.T, a *App, env map[string]string, args ...string) (string, int) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errOut bytes.Buffer
	done := make(chan int)
	go func() { done <- runCLI(args, &out, &errOut) }()
	for {
		select {
		case code := <-done:
			if code != 0 {
				t.Logf("gorex %s: %d %s", strings.Join(args, " "), code, errOut.String())
			}
			return strings.TrimSpace(out.String()), code
		case <-time.After(10 * time.Millisecond):
			a.runPosted()
		}
	}
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TestCLI drives the window as cmux-tui-run.sh does cmux's: split the
// pane, name the new one, run a program in it, read its screen, close it.
func TestCLI(t *testing.T) {
	a, tt := newTestApp(t)
	if err := a.subscribe(nil); err != nil {
		t.Fatal(err)
	}
	tab := a.tab()
	self := tab.Focus
	waitFor(t, tt, "the shell", func() bool { return self.term != nil && strings.Contains(self.term.Text(), "$") })
	env := map[string]string{"GOREX_SURFACE_ID": self.SID, "GOREX_WORKSPACE_ID": tab.UID, "GOREX_SOCKET": rex.SocketPath()}

	if out, code := cmd(t, a, env, "ping"); code != 0 || out != "PONG" {
		t.Fatalf("ping %q %d", out, code)
	}

	out, code := cmd(t, a, env, "--id-format", "both", "new-split", "right", "--focus", "false")
	if code != 0 || !strings.HasPrefix(out, "OK surface:") {
		t.Fatalf("new-split %q %d", out, code)
	}
	sid := uuidRE.FindString(out)
	if sid == "" || sid == self.SID || !strings.Contains(out, "workspace:"+tab.UID) {
		t.Fatalf("new-split printed %q", out)
	}
	if n := len(tab.panes()); n != 2 || tab.Focus != self {
		t.Fatalf("%d panes, the focus moved: %v", n, tab.Focus != self)
	}
	split := a.paneOf(sid)
	if split == nil || tab.Root.A.Pane != self || tab.Root.B.Pane != split {
		t.Fatal("the new pane is not right of the first")
	}

	if _, code := cmd(t, a, env, "rename-tab", "--surface", sid, "Codex Review"); code != 0 {
		t.Fatal("rename-tab failed")
	}
	if n, _ := split.label(); n != "Codex Review" {
		t.Errorf("pane named %q", n)
	}

	// The new shell knows its surface and tab.
	if _, code := cmd(t, a, env, "send", "--surface", sid, "--", `echo "id=$GOREX_SURFACE_ID ws=$GOREX_WORKSPACE_ID"\n`); code != 0 {
		t.Fatal("send failed")
	}
	want := "id=" + sid + " ws=" + tab.UID
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ = cmd(t, a, env, "read-screen", "--surface", sid, "--scrollback", "--lines", "20")
		if strings.Contains(out, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("screen %q, not %q", out, want)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// close-surface closes only the surface it names.
	if _, code := cmd(t, a, env, "close-surface"); code == 0 {
		t.Fatal("close-surface closed a surface it was not named")
	}
	if _, code := cmd(t, a, env, "close-surface", "--surface", ""); code == 0 {
		t.Fatal("close-surface took a blank --surface")
	}
	if _, code := cmd(t, a, env, "close-surface", "--surface", sid); code != 0 {
		t.Fatal("close-surface failed")
	}
	waitFor(t, tt, "the pane to close", func() bool { return len(tab.panes()) == 1 && tab.Focus == self })
	if _, code := cmd(t, a, env, "send", "--surface", sid, "hi"); code == 0 {
		t.Error("send to a closed surface succeeded")
	}

	// Splits go left and up too, and take the focus when asked.
	out, _ = cmd(t, a, env, "new-split", "up", "--focus", "true")
	up := a.paneOf(uuidRE.FindString(out))
	if up == nil || tab.Root.A.Pane != up || !tab.Root.Vertical || tab.Focus != up {
		t.Fatalf("split up %q", out)
	}
}

func TestIsCLICommand(t *testing.T) {
	for _, args := range [][]string{{"ping"}, {"--id-format", "both", "new-split", "right"}, {"--json", "read-screen"}, {"--help"}} {
		if !isCLICommand(args) {
			t.Errorf("%q is a command", args)
		}
	}
	for _, args := range [][]string{nil, {"-server"}, {"-NSWindowResizeTime", "1"}, {"--id-format", "both"}} {
		if isCLICommand(args) {
			t.Errorf("%q is the app's", args)
		}
	}
}
