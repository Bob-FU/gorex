package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gorex/internal/rex"
)

// The command line drives the window from its shells, after cmux's: a
// script splits the pane it runs in, runs a program in the new pane,
// reads its screen and closes it.
//
//	gorex new-split right --focus false    OK surface:<id> workspace:<id>
//	gorex rename-tab --surface <id> Codex  name the pane
//	gorex send --surface <id> 'make\n'     type, \n as Enter
//	gorex read-screen --surface <id> --scrollback --lines 60
//	gorex close-surface --surface <id>     end the session, closing its pane
//
// A surface is a pane's session, and a workspace a tab: a shell finds
// its own in GOREX_SURFACE_ID and GOREX_WORKSPACE_ID, and the server in
// GOREX_SOCKET.
var cliCommands = map[string]func(c *cli) error{
	"ping":             (*cli).ping,
	"identify":         (*cli).identify,
	"list-surfaces":    (*cli).list,
	"list-panes":       (*cli).list,
	"new-split":        (*cli).newSplit,
	"rename-tab":       (*cli).renameTab,
	"rename-surface":   (*cli).renameTab,
	"rename-workspace": (*cli).renameWorkspace,
	"send":             (*cli).send,
	"send-key":         (*cli).sendKey,
	"read-screen":      (*cli).readScreen,
	"close-surface":    (*cli).closeSurface,
	"help":             (*cli).help,
}

// isCLICommand tells the arguments of a command of the command line, as
// [--json] [--id-format <f>] new-split right, from the app's.
func isCLICommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--json" || strings.HasPrefix(a, "--id-format="):
		case a == "--id-format":
			i++
		default:
			_, ok := cliCommands[a]
			return ok || a == "--help"
		}
	}
	return false
}

const cliUsage = `usage: gorex [--json] <command> [options]

  ping                                 is the session server running
  identify                             this shell's surface and workspace
  list-surfaces                        every session, and its program
  new-split right|down|left|up [--surface <id>] [--focus true|false]
            [--command <text>]         split a pane, print the new surface
  rename-tab [--surface <id>] <title>  name a pane ("" gives its own back)
  rename-workspace [--surface <id>] <title>
                                       name the tab of a pane
  send [--surface <id>] [--] <text>    type text; \n and \r are Enter, \t Tab
  send-key [--surface <id>] <key>      enter, tab, escape, backspace, up,
                                       down, left, right, ctrl-c, ctrl-d…
  read-screen [--surface <id>] [--scrollback] [--lines <n>]
                                       print the text of the screen
  close-surface --surface <id>         end a session, closing its pane

--surface defaults to GOREX_SURFACE_ID, but for close-surface, which
never closes a surface it is not named.
`

// cli is a command of the command line being run.
type cli struct {
	args   []string
	json   bool
	out    io.Writer
	client *rex.Client
}

// runCLI runs a command and returns the exit code.
func runCLI(args []string, stdout, stderr io.Writer) int {
	c := &cli{out: stdout}
	var name string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case name == "" && a == "--json":
			c.json = true
		case name == "" && a == "--id-format":
			i++ // cmux's; GoRex's IDs are UUIDs
		case name == "" && strings.HasPrefix(a, "--id-format="):
		case name == "" && (a == "--help" || a == "-h"):
			name = "help"
		case name == "":
			name = a
		default:
			c.args = append(c.args, a)
		}
	}
	run, ok := cliCommands[name]
	if !ok {
		fmt.Fprintf(stderr, "gorex: unknown command %q\n\n%s", name, cliUsage)
		return 2
	}
	// --json and --id-format may follow the command too, as with cmux.
	if name != "send" {
		c.args = c.flags(c.args)
	}
	if err := run(c); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if c.client != nil {
		c.client.Close()
	}
	return 0
}

// flags takes the global flags out of args.
func (c *cli) flags(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			return append(rest, args[i:]...)
		case a == "--json":
			c.json = true
		case a == "--id-format":
			i++
		case strings.HasPrefix(a, "--id-format="):
		default:
			rest = append(rest, a)
		}
	}
	return rest
}

// connect connects to the session server, which must run already.
func (c *cli) connect() (*rex.Client, error) {
	if c.client != nil {
		return c.client, nil
	}
	path := os.Getenv("GOREX_SOCKET")
	if path == "" {
		path = rex.SocketPath()
	}
	client, err := rex.Dial(path)
	if err != nil {
		return nil, fmt.Errorf("GoRex's session server is not running (%s)", path)
	}
	c.client = client
	return client, nil
}

// option takes the value of a flag out of the arguments: ok tells it was
// there.
func (c *cli) option(names ...string) (value string, ok bool, err error) {
	for i := 0; i < len(c.args); i++ {
		a := c.args[i]
		if a == "--" {
			break
		}
		for _, n := range names {
			if v, found := strings.CutPrefix(a, n+"="); found {
				c.args = append(c.args[:i:i], c.args[i+1:]...)
				return v, true, nil
			}
			if a == n {
				if i+1 >= len(c.args) {
					return "", true, fmt.Errorf("%s needs a value", n)
				}
				v := c.args[i+1]
				c.args = append(c.args[:i:i], c.args[i+2:]...)
				return v, true, nil
			}
		}
	}
	return "", false, nil
}

// flag takes a flag of no value out of the arguments.
func (c *cli) flag(name string) bool {
	for i, a := range c.args {
		if a == "--" {
			break
		}
		if a == name {
			c.args = append(c.args[:i:i], c.args[i+1:]...)
			return true
		}
	}
	return false
}

// noFlags fails on a flag the command does not know.
func (c *cli) noFlags(cmd string) error {
	for _, a := range c.args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			return fmt.Errorf("%s: unknown flag %s", cmd, a)
		}
	}
	return nil
}

// surface returns the session --surface names, or the shell's own, as a
// unique prefix of its ID too. --workspace and --window, cmux's, are
// taken and left aside: a surface names its tab.
func (c *cli) surface(cmd string, required bool) (string, error) {
	c.option("--workspace")
	c.option("--window")
	v, given, err := c.option("--surface", "--panel")
	if err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if given && v == "" {
		return "", fmt.Errorf("%s: --surface is blank", cmd)
	}
	if !given {
		if required {
			return "", fmt.Errorf("%s needs --surface <id>", cmd)
		}
		v = os.Getenv("GOREX_SURFACE_ID")
		if v == "" {
			return "", fmt.Errorf("%s: not in a GoRex terminal, and no --surface <id>", cmd)
		}
	}
	v = strings.TrimPrefix(v, "surface:")
	client, err := c.connect()
	if err != nil {
		return "", err
	}
	infos, err := client.List()
	if err != nil {
		return "", err
	}
	match := ""
	for _, in := range infos {
		if in.ID == v {
			return v, nil
		}
		if strings.HasPrefix(in.ID, v) {
			if match != "" {
				return "", fmt.Errorf("surface %q is ambiguous", v)
			}
			match = in.ID
		}
	}
	if match == "" {
		return "", fmt.Errorf("no surface %q", v)
	}
	return match, nil
}

// text returns the remaining arguments, past a --, joined by spaces.
func (c *cli) text() string {
	args := c.args
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	return strings.Join(args, " ")
}

func (c *cli) print(v any, text string) error {
	if c.json {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%s\n", b)
		return nil
	}
	if text != "" {
		fmt.Fprintln(c.out, text)
	}
	return nil
}

func (c *cli) help() error {
	fmt.Fprint(c.out, cliUsage)
	return nil
}

func (c *cli) ping() error {
	client, err := c.connect()
	if err != nil {
		return err
	}
	h, err := client.Hello()
	if err != nil {
		return err
	}
	if h.Version != rex.ProtocolVersion {
		return fmt.Errorf("the session server is of another version of GoRex")
	}
	return c.print(map[string]any{"ok": true, "pid": h.PID}, "PONG")
}

func (c *cli) identify() error {
	if err := c.noFlags("identify"); err != nil {
		return err
	}
	sid, ws := os.Getenv("GOREX_SURFACE_ID"), os.Getenv("GOREX_WORKSPACE_ID")
	sock := os.Getenv("GOREX_SOCKET")
	if sock == "" {
		sock = rex.SocketPath()
	}
	text := fmt.Sprintf("surface:%s workspace:%s socket:%s", sid, ws, sock)
	return c.print(map[string]string{"surface_id": sid, "workspace_id": ws, "socket_path": sock}, text)
}

func (c *cli) list() error {
	if err := c.noFlags("list-surfaces"); err != nil {
		return err
	}
	client, err := c.connect()
	if err != nil {
		return err
	}
	infos, err := client.List()
	if err != nil {
		return err
	}
	if c.json {
		return c.print(infos, "")
	}
	self := os.Getenv("GOREX_SURFACE_ID")
	for _, in := range infos {
		mark := "  "
		if in.ID == self {
			mark = "* "
		}
		state := in.Program
		if in.Exited {
			state = fmt.Sprintf("exited %d", in.ExitCode)
		}
		fmt.Fprintf(c.out, "%ssurface:%s workspace:%s  %s  %s\n", mark, in.ID, in.Workspace, state, in.Dir)
	}
	return nil
}

func (c *cli) newSplit() error {
	focus := false
	if v, ok, err := c.option("--focus"); err != nil {
		return err
	} else if ok {
		if focus, err = strconv.ParseBool(v); err != nil {
			return fmt.Errorf("new-split: --focus is true or false, not %q", v)
		}
	}
	command, _, err := c.option("--command")
	if err != nil {
		return err
	}
	sid, err := c.surface("new-split", false)
	if err != nil {
		return err
	}
	if err := c.noFlags("new-split"); err != nil {
		return err
	}
	dir := "right"
	if len(c.args) > 0 {
		dir = c.args[0]
	}
	if len(c.args) > 1 {
		return fmt.Errorf("new-split: unexpected %q", c.args[1])
	}
	in, err := c.client.Split(sid, rex.SplitOptions{Direction: dir, Focus: focus})
	if err != nil {
		return err
	}
	if command = strings.TrimSpace(command); command != "" {
		if err := c.client.Send(in.ID, command+"\r"); err != nil {
			return err
		}
	}
	return c.print(map[string]string{"surface_id": in.ID, "workspace_id": in.Workspace},
		"OK surface:"+in.ID+" workspace:"+in.Workspace)
}

func (c *cli) rename(cmd string, tab bool) error {
	title, given, err := c.option("--title")
	if err != nil {
		return err
	}
	c.option("--tab")
	sid, err := c.surface(cmd, false)
	if err != nil {
		return err
	}
	if err := c.noFlags(cmd); err != nil {
		return err
	}
	if !given {
		title = c.text()
		if len(c.args) == 0 {
			return fmt.Errorf("%s needs a title", cmd)
		}
	}
	if err := c.client.Rename(sid, rex.RenameOptions{Title: strings.TrimSpace(title), Tab: tab}); err != nil {
		return err
	}
	return c.print(map[string]bool{"ok": true}, "OK")
}

func (c *cli) renameTab() error       { return c.rename("rename-tab", false) }
func (c *cli) renameWorkspace() error { return c.rename("rename-workspace", true) }

func (c *cli) send() error {
	sid, err := c.surface("send", false)
	if err != nil {
		return err
	}
	text := c.text()
	if text == "" {
		return errors.New("send needs text")
	}
	r := strings.NewReplacer(`\n`, "\r", `\r`, "\r", `\t`, "\t")
	if err := c.client.Send(sid, r.Replace(text)); err != nil {
		return err
	}
	return c.print(map[string]bool{"ok": true}, "OK")
}

// keys are what send-key types for a key's name.
var keys = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "escape": "\x1b", "esc": "\x1b",
	"backspace": "\x7f", "space": " ", "delete": "\x1b[3~",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
}

func (c *cli) sendKey() error {
	sid, err := c.surface("send-key", false)
	if err != nil {
		return err
	}
	if len(c.args) != 1 {
		return errors.New("send-key needs a key")
	}
	name := strings.ToLower(c.args[0])
	seq, ok := keys[name]
	if k, found := strings.CutPrefix(name, "ctrl-"); found && len(k) == 1 && k[0] >= 'a' && k[0] <= 'z' {
		seq, ok = string(rune(k[0]-'a'+1)), true
	}
	if !ok {
		return fmt.Errorf("send-key: unknown key %q", c.args[0])
	}
	if err := c.client.Send(sid, seq); err != nil {
		return err
	}
	return c.print(map[string]bool{"ok": true}, "OK")
}

func (c *cli) readScreen() error {
	scrollback := c.flag("--scrollback")
	lines := 0
	if v, ok, err := c.option("--lines"); err != nil {
		return err
	} else if ok {
		if lines, err = strconv.Atoi(v); err != nil || lines <= 0 {
			return errors.New("--lines must be greater than 0")
		}
		scrollback = true
	}
	sid, err := c.surface("read-screen", false)
	if err != nil {
		return err
	}
	if err := c.noFlags("read-screen"); err != nil {
		return err
	}
	if len(c.args) > 0 {
		return fmt.Errorf("read-screen: unexpected %q", c.args[0])
	}
	text, err := c.client.ReadScreen(sid, scrollback, lines)
	if err != nil {
		return err
	}
	if c.json {
		return c.print(map[string]string{"surface_id": sid, "text": text}, "")
	}
	fmt.Fprintln(c.out, text)
	return nil
}

func (c *cli) closeSurface() error {
	// Never the shell's own surface by default: a script that lost the ID
	// of the pane it opened would close the pane it runs in.
	sid, err := c.surface("close-surface", true)
	if err != nil {
		return err
	}
	if err := c.noFlags("close-surface"); err != nil {
		return err
	}
	if err := c.client.Kill(sid); err != nil {
		return err
	}
	return c.print(map[string]bool{"ok": true}, "OK")
}
