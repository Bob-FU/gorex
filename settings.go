package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"gorex/internal/rex"
)

// settings are the user's choices, kept in settings.json beside the
// server's state.
type settings struct {
	// Appearance is "light", "dark", or "" to follow the system.
	Appearance string  `json:"appearance,omitempty"`
	FontSize   float32 `json:"fontSize,omitempty"`
}

const defaultFontSize = 11.6

var prefs settings

func settingsPath() string { return filepath.Join(rex.Dir(), "settings.json") }

func loadSettings() {
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &prefs)
	}
	// No size, or none GoRex takes, is the configuration's.
	if prefs.FontSize < 6 || prefs.FontSize > 40 {
		prefs.FontSize = 0
	}
	loadConfig()
	applyAppearance()
}

func saveSettings() {
	b, _ := json.MarshalIndent(prefs, "", "  ")
	os.MkdirAll(rex.Dir(), 0o700)
	os.WriteFile(settingsPath(), b, 0o600)
}

func applyAppearance() {
	switch prefs.Appearance {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) setAppearance(v string) {
	prefs.Appearance = v
	applyAppearance()
	saveSettings()
}

// setFontSize changes the size of the terminals' text, in every pane: 0
// gives back the configuration's.
func (a *App) setFontSize(size float32) {
	if size != 0 {
		size = min(max(size, 8), 32)
	}
	prefs.FontSize = size
	termFont.Size = fontSize()
	for _, t := range a.tabs {
		for _, p := range t.panes() {
			if p.term != nil {
				p.term.SetFont(termFont)
			}
		}
	}
	saveSettings()
}
