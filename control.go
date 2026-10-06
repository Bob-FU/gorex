package main

import (
	"errors"
	"slices"

	"github.com/egoist/mygo"

	"gorex/internal/rex"
)

// subscribe makes the app's connection to the server the window's, which
// answers what the command line asks of it: to split a pane, to name a
// pane or a tab.
func (a *App) subscribe(win *mygo.Window) error {
	c := a.client
	return c.Subscribe(func(ev rex.Event) {
		run := func() {
			data, err := a.handleEvent(ev)
			go c.Answer(ev.Seq, data, err)
		}
		if win != nil {
			win.Update(run)
		} else {
			a.post(run)
		}
	})
}

// paneOf returns the pane of the session sid.
func (a *App) paneOf(sid string) *Pane {
	for _, t := range a.tabs {
		for _, p := range t.panes() {
			if p.SID == sid && !p.closed {
				return p
			}
		}
	}
	return nil
}

// handleEvent does what the command line asks of the window.
func (a *App) handleEvent(ev rex.Event) (any, error) {
	p := a.paneOf(ev.SID)
	if p == nil {
		return nil, errors.New("no pane shows session " + ev.SID)
	}
	switch ev.Op {
	case "split":
		o := rex.SplitOptions{Direction: "right"}
		if ev.Split != nil {
			o = *ev.Split
		}
		if !slices.Contains([]string{"right", "down", "left", "up"}, o.Direction) {
			return nil, errors.New("the direction is right, down, left or up, not " + o.Direction)
		}
		np := a.splitPane(p, o.Direction, o.Focus)
		if np.SID == "" {
			return nil, errors.New("could not start a session: " + a.err)
		}
		return np.info, nil
	case "rename":
		if ev.Rename == nil {
			return nil, errors.New("no name")
		}
		if ev.Rename.Tab {
			p.Tab.Name = ev.Rename.Title
		} else {
			p.Name = ev.Rename.Title
		}
		a.changed()
		return nil, nil
	}
	return nil, errors.New("unknown event " + ev.Op)
}
