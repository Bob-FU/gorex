package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	ghostty := filepath.Join(dir, "ghostty")
	gorex := filepath.Join(dir, "gorex")
	os.WriteFile(ghostty, []byte(`# Ghostty's
font-family = "Fira Code"
font-size = 14
theme = light:Catppuccin Latte,dark:Catppuccin Mocha
palette = 1=#ff0000
keybind = cmd+t=new_tab
`), 0o600)
	os.WriteFile(gorex, []byte(`font-family = ""
font-family = MesloLGS NF
font-family = Symbols Only
adjust-cell-height = 10%
background = #102030
`), 0o600)
	c := parseConfig(readConfig([]string{ghostty, filepath.Join(dir, "missing"), gorex}))
	if len(c.Errors) > 0 {
		t.Errorf("errors %q", c.Errors)
	}
	if strings.Join(c.Families, "|") != "MesloLGS NF|Symbols Only" {
		t.Errorf("families %q", c.Families)
	}
	if c.Size != 14 || c.LineHeight < 1.09 || c.LineHeight > 1.11 {
		t.Errorf("size %v, line height %v", c.Size, c.LineHeight)
	}
	if c.LightTheme == nil || c.DarkTheme == nil || c.LightTheme == c.DarkTheme {
		t.Fatal("no themes")
	}
	for _, th := range []*ui.Color{&c.LightTheme.Background, &c.DarkTheme.Background} {
		if *th != ui.Hex("#102030") {
			t.Errorf("background %v", *th)
		}
	}
	if c.DarkTheme.Palette[1] != ui.Hex("#ff0000") {
		t.Errorf("red %v", c.DarkTheme.Palette[1])
	}
	// Mocha's blue, which nothing overrides.
	if c.DarkTheme.Palette[4] == c.LightTheme.Palette[4] {
		t.Error("the light and dark themes are alike")
	}

	if l, d := splitTheme("Dracula"); l != "Dracula" || d != "Dracula" {
		t.Errorf("one theme %q %q", l, d)
	}
	if c := parseConfig([]configLine{{key: "theme", value: "No Such Theme"}}); len(c.Errors) != 1 {
		t.Errorf("errors %q", c.Errors)
	}
	if c := parseConfig(nil); c.LightTheme != nil {
		t.Error("a theme without configuration")
	}
}

func TestSetConfig(t *testing.T) {
	t.Setenv("GOREX_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	savedConfig, savedFont := config, termFont
	t.Cleanup(func() { config, termFont = savedConfig, savedFont })
	a := &App{}
	if err := a.setConfig("theme", "Dracula"); err != nil {
		t.Fatal(err)
	}
	a.setConfig("font-family", `""`, "MesloLGS NF")
	a.setConfig("theme", "Nord")
	b, _ := os.ReadFile(gorexConfigPath())
	if got := string(b); got != "font-family = \"\"\nfont-family = MesloLGS NF\ntheme = Nord\n" {
		t.Errorf("config %q", got)
	}
	if !strings.HasPrefix(termFont.Family, "MesloLGS NF, JetBrains Mono, Symbols Nerd Font Mono") {
		t.Errorf("family %q", termFont.Family)
	}
	if config.DarkTheme == nil || !themedDark(false) {
		t.Error("Nord is not a dark theme in a light window")
	}
	if configChanged() {
		t.Error("changed after reading")
	}
}
