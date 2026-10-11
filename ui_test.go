package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestMessageBody(t *testing.T) {
	cases := []struct {
		name, in string
		want     partBody
	}{
		{"html", `{"html_body":[1],"parts":[{"body":{"Multipart":[1]}},{"body":{"Html":"<p>hi</p>"}}]}`, partBody{HTML: "<p>hi</p>"}},
		{"text", `{"html_body":[0],"parts":[{"body":{"Text":"hi"}}]}`, partBody{Text: "hi"}},
		{"no body", `{"html_body":[],"parts":[]}`, partBody{}},
		{"bad index", `{"html_body":[3],"parts":[{"body":{"Text":"hi"}}]}`, partBody{}},
	}
	for _, c := range cases {
		var m message
		if err := json.Unmarshal([]byte(c.in), &m); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := m.body(); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestCleanDropsEscapes(t *testing.T) {
	if got := clean("a\x1b[2Jb\r\n\tc"); got != "a[2Jb\n c" {
		t.Errorf("clean: got %q", got)
	}
	if got := oneLine(" a\n b\x07 "); got != "a b" {
		t.Errorf("oneLine: got %q", got)
	}
}

// The view must fill the window exactly in every state, or the header scrolls
// off the alternate screen.
func TestViewFitsWindow(t *testing.T) {
	const w, h = 80, 24
	e := envelope{ID: "1", Subject: strings.Repeat("long subject ", 20), From: []address{{Email: "a@example.com"}}, Date: "2026-10-11T10:02:35Z"}

	steps := []struct {
		name string
		msg  tea.Msg
	}{
		{"empty", tea.WindowSizeMsg{Width: w, Height: h}},
		{"inbox", envelopesMsg{envelopes: []envelope{e, e, e}}},
		{"error banner", envelopesMsg{err: errors.New("himalaya: no connection")}},
		{"reader", messageMsg{envelope: e, text: strings.Repeat("line\n", 100)}},
		{"full help", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}},
	}

	var m tea.Model = newModel()
	for _, s := range steps {
		m, _ = m.Update(s.msg)
		view := m.View()
		if got := lipgloss.Height(view); got != h {
			t.Errorf("%s: view is %d rows, want %d", s.name, got, h)
		}
		if got := lipgloss.Width(view); got > w {
			t.Errorf("%s: view is %d columns, want at most %d", s.name, got, w)
		}
	}
}
