package main

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"gorex/internal/rex"
)

// The terminals' font and colors come from the configuration of Ghostty,
// in its format, as cmux takes them: Ghostty's own files, then cmux's,
// then GoRex's, each line overriding the ones before.
//
//	font-family = MesloLGS NF
//	font-size = 13
//	adjust-cell-height = 10%
//	theme = light:Catppuccin Latte,dark:Catppuccin Mocha
//	background = #1e1e2e
//	palette = 4=#89b4fa
//
// What the families lack, the Nerd Fonts' symbols and the system's fonts
// draw, as the glyphs of powerlevel10k's prompts.

// fallbackFamilies follow the families of the configuration.
const fallbackFamilies = "JetBrains Mono, Symbols Nerd Font Mono, SF Mono, Menlo, monospace"

// termConfig is what the configuration sets of the terminals.
type termConfig struct {
	Families   []string
	Size       float32
	LineHeight float32
	Features   []string
	Thicken    bool
	// LightTheme and DarkTheme are the themes of light and dark windows;
	// both nil leaves GoRex's own.
	LightTheme, DarkTheme *terminal.Theme
	// Errors are the lines the terminals could not take.
	Errors []string
}

// configFiles returns the files of the configuration, read in order.
func configFiles() []string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	support := filepath.Join(home, "Library", "Application Support")
	var files []string
	for _, dir := range []string{
		filepath.Join(xdg, "ghostty"),
		filepath.Join(support, "com.mitchellh.ghostty"),
		filepath.Join(support, "com.cmuxterm.app"),
	} {
		files = append(files, filepath.Join(dir, "config"), filepath.Join(dir, "config.ghostty"))
	}
	return append(files, gorexConfigPath())
}

// gorexConfigPath returns GoRex's own file of the configuration.
func gorexConfigPath() string { return filepath.Join(rex.Dir(), "config") }

// configLine is a line of the configuration: a key and its value.
type configLine struct{ key, value, file string }

// readConfig reads the lines of the configuration's files.
func readConfig(files []string) []configLine {
	var lines []configLine
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] == '#' {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			lines = append(lines, configLine{strings.TrimSpace(k), v, f})
		}
	}
	return lines
}

// parseConfig takes what the lines set of the terminals.
func parseConfig(lines []configLine) termConfig {
	var c termConfig
	var themeLight, themeDark string
	var colors []configLine
	for _, l := range lines {
		switch l.key {
		case "font-family":
			// An empty value clears the families before it, as in Ghostty.
			if l.value == "" {
				c.Families = nil
			} else {
				c.Families = append(c.Families, l.value)
			}
		case "font-size":
			if v, err := strconv.ParseFloat(l.value, 32); err == nil && v >= 6 && v <= 72 {
				c.Size = float32(v)
			} else {
				c.Errors = append(c.Errors, "font-size = "+l.value)
			}
		case "font-feature":
			if l.value == "" {
				c.Features = nil
			} else {
				c.Features = append(c.Features, l.value)
			}
		case "font-thicken":
			c.Thicken = l.value == "true"
		case "adjust-cell-height":
			c.LineHeight = cellHeight(l.value)
		case "theme":
			themeLight, themeDark = splitTheme(l.value)
		case "background", "foreground", "cursor-color", "cursor-text",
			"selection-background", "selection-foreground", "palette":
			colors = append(colors, l)
		}
	}
	if themeLight != "" || themeDark != "" || len(colors) > 0 {
		c.LightTheme = loadTheme(themeLight, &c)
		if themeDark == themeLight {
			if c.LightTheme != nil {
				c.DarkTheme = cloneTheme(c.LightTheme)
			}
		} else {
			c.DarkTheme = loadTheme(themeDark, &c)
		}
		for _, th := range []**terminal.Theme{&c.LightTheme, &c.DarkTheme} {
			if *th == nil {
				// Colors without a theme are over the terminal's own.
				if th == &c.LightTheme {
					*th = cloneTheme(lightTerm)
				} else {
					*th = cloneTheme(darkTerm)
				}
			}
			overlay(*th, colors, &c)
		}
	}
	return c
}

// splitTheme reads theme = Name, or light:Name,dark:Name.
func splitTheme(v string) (light, dark string) {
	if !strings.Contains(v, "light:") && !strings.Contains(v, "dark:") {
		return v, v
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if name, ok := strings.CutPrefix(part, "light:"); ok {
			light = strings.TrimSpace(name)
		} else if name, ok := strings.CutPrefix(part, "dark:"); ok {
			dark = strings.TrimSpace(name)
		}
	}
	return light, dark
}

// loadTheme returns a theme of Ghostty's by its name, or the path of its
// file, and nil for none.
func loadTheme(name string, c *termConfig) *terminal.Theme {
	if name == "" {
		return nil
	}
	th, err := terminal.GhosttyTheme(name)
	if err != nil {
		c.Errors = append(c.Errors, "theme = "+name)
		return nil
	}
	return th
}

func cloneTheme(th *terminal.Theme) *terminal.Theme {
	c := *th
	return &c
}

// overlay sets the colors the lines set on a theme.
func overlay(th *terminal.Theme, colors []configLine, c *termConfig) {
	for _, l := range colors {
		one, err := terminal.ParseGhosttyTheme([]byte(l.key + " = " + l.value))
		if err != nil {
			c.Errors = append(c.Errors, l.key+" = "+l.value)
			continue
		}
		switch l.key {
		case "background":
			th.Background = one.Background
		case "foreground":
			th.Foreground = one.Foreground
		case "cursor-color":
			th.Cursor = one.Cursor
		case "cursor-text":
			th.CursorText = one.CursorText
		case "selection-background":
			th.Selection = one.Selection
		case "selection-foreground":
			th.SelectionText = one.SelectionText
		case "palette":
			i, _, _ := strings.Cut(l.value, "=")
			if n, err := strconv.ParseUint(strings.TrimSpace(i), 0, 8); err == nil && n < 16 {
				th.Palette[n] = one.Palette[n]
			}
		}
	}
}

// cellHeight reads adjust-cell-height, 10% or 2 (pixels, about a tenth
// of a row each at the usual sizes), as a line height.
func cellHeight(v string) float32 {
	if p, ok := strings.CutSuffix(v, "%"); ok {
		if f, err := strconv.ParseFloat(p, 32); err == nil {
			return float32(max(0.5, 1+f/100))
		}
		return 0
	}
	if f, err := strconv.ParseFloat(v, 32); err == nil {
		return float32(max(0.5, 1+f/14))
	}
	return 0
}

// The configuration in effect, and when its files changed last.
var (
	config      termConfig
	configStamp atomic.Value // string
)

// configChanged reports whether the configuration's files changed since
// it was read last.
func configChanged() bool {
	s, _ := configStamp.Load().(string)
	return stampOf(configFiles()) != s
}

func stampOf(files []string) string {
	var b strings.Builder
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			b.WriteString(f)
			b.WriteString(st.ModTime().Format(time.RFC3339Nano))
			b.WriteString(strconv.FormatInt(st.Size(), 10))
		}
	}
	return b.String()
}

// loadConfig reads the configuration and makes the terminals' font of it.
func loadConfig() {
	files := configFiles()
	configStamp.Store(stampOf(files))
	config = parseConfig(readConfig(files))
	family := fallbackFamilies
	if len(config.Families) > 0 {
		family = strings.Join(config.Families, ", ") + ", " + fallbackFamilies
	}
	termFont.Family = family
	termFont.LineHeight = config.LineHeight
	termFont.Features = config.Features
	termFont.Thicken = config.Thicken
	termFont.Size = fontSize()
}

// fontSize is the size of the terminals' text: the one chosen in the View
// menu, or the configuration's.
func fontSize() float32 {
	switch {
	case prefs.FontSize > 0:
		return prefs.FontSize
	case config.Size > 0:
		return config.Size
	}
	return defaultFontSize
}

// themes returns the terminals' themes of light and dark windows.
func themes() (light, dark *terminal.Theme) {
	if config.LightTheme != nil {
		return config.LightTheme, config.DarkTheme
	}
	return lightTerm, darkTerm
}

// termBackground returns the background of the configuration's theme,
// and false for GoRex's own, whose panes show the window through them.
func termBackground(dark bool) (ui.Color, bool) {
	if config.LightTheme == nil {
		return ui.Color{}, false
	}
	if dark {
		return config.DarkTheme.Background, true
	}
	return config.LightTheme.Background, true
}

// themedDark tells whether the window, dark or not, shows terminals of a
// dark theme, as the configuration's may be in a light window: the
// window's colors follow the terminals'.
func themedDark(dark bool) bool {
	if config.LightTheme == nil {
		return dark
	}
	th := config.LightTheme
	if dark {
		th = config.DarkTheme
	}
	return luminance(th.Background) < 0.5
}

func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// applyConfig rereads the configuration and gives every pane its font
// and colors.
func (a *App) applyConfig() {
	loadConfig()
	light, dark := themes()
	for _, t := range a.tabs {
		for _, p := range t.panes() {
			if p.term != nil {
				p.term.SetFont(termFont)
				p.term.SetTheme(light, dark)
			}
		}
	}
}

// setConfig sets a key of GoRex's file of the configuration, replacing
// the lines that set it, and applies it.
func (a *App) setConfig(key string, values ...string) error {
	path := gorexConfigPath()
	b, _ := os.ReadFile(path)
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		k, _, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if line != "" || len(out) > 0 {
			out = append(out, line)
		}
	}
	for _, v := range values {
		out = append(out, key+" = "+v)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	a.applyConfig()
	return nil
}

// configTemplate starts GoRex's file of the configuration.
const configTemplate = `# GoRex's configuration, in Ghostty's format. GoRex reads Ghostty's
# files first (~/.config/ghostty/config), then cmux's, then this one:
# what this file sets wins. It applies as you save it.
#
# font-family = MesloLGS NF
# font-size = 13
# adjust-cell-height = 10%
# font-feature = -calt
# theme = Catppuccin Mocha
# theme = light:Catppuccin Latte,dark:Catppuccin Mocha
# background = #1e1e2e
# foreground = #cdd6f4
# palette = 4=#89b4fa
#
# The command palette lists the themes: type "theme".
`

// openConfig opens GoRex's file of the configuration, made first.
func (a *App) openConfig() {
	path := gorexConfigPath()
	if _, err := os.Stat(path); err != nil {
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(configTemplate), 0o600)
	}
	openPath(path)
}

// openPath opens a file of text in the user's editor.
func openPath(path string) {
	if runtime.GOOS == "darwin" {
		exec.Command("open", "-t", path).Start()
		return
	}
	mygo.Shell.OpenPath(path)
}
