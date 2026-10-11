package main

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---- styles, reused from journalfix/ui.go ----------------------------------

var (
	accent   = lipgloss.AdaptiveColor{Light: "#6C3FC4", Dark: "#B58BFF"}
	negative = lipgloss.AdaptiveColor{Light: "#B01919", Dark: "#F87171"}
	dim      = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#7A828E"}
)

var (
	tabActiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(accent).
			Bold(true).
			Padding(0, 2)

	tabBarStyle = lipgloss.NewStyle().Padding(0, 0, 1, 0)

	titleStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	dimStyle   = lipgloss.NewStyle().Foreground(dim)
	errorStyle = lipgloss.NewStyle().Foreground(negative).Bold(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(dim).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(dim)
)

// ---- key map ---------------------------------------------------------------

type keyMap struct {
	Open    key.Binding
	Back    key.Binding
	Refresh key.Binding
	Help    key.Binding
	Quit    key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:    key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Open, k.Back, k.Refresh, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Open, k.Back, k.Refresh}, {k.Help, k.Quit}}
}

// ---- table -----------------------------------------------------------------

func newStyledTable() table.Model {
	t := table.New(table.WithFocused(true))
	st := table.DefaultStyles()
	st.Header = st.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(dim).
		BorderBottom(true).
		Bold(true).
		Foreground(accent)
	st.Selected = st.Selected.
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(accent).
		Bold(false)
	t.SetStyles(st)
	return t
}

// inboxColumns gives the Subject column the width that the fixed columns leave.
func inboxColumns(width int) []table.Column {
	const idW, fromW, dateW = 7, 24, 12
	// Each of the 4 cells has one pad column on each side.
	subjectW := max(width-idW-fromW-dateW-4*2, 10)
	return []table.Column{
		{Title: "ID", Width: idW},
		{Title: "From", Width: fromW},
		{Title: "Subject", Width: subjectW},
		{Title: "Date", Width: dateW},
	}
}

// shortDate shows an RFC 3339 date in local time. It returns any other text as is.
func shortDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return oneLine(s)
	}
	return t.Local().Format("Jan 02 15:04")
}

// ---- model -----------------------------------------------------------------

type model struct {
	keys keyMap
	help help.Model
	spin spinner.Model

	inbox  table.Model
	reader viewport.Model

	envelopes []envelope // ordered, mirrors inbox rows
	open      *envelope  // the message in the reader. nil shows the inbox.

	busy          bool // a himalaya call is in flight
	err           error
	width, height int
}

func newModel() model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	return model{
		keys:   newKeyMap(),
		help:   help.New(),
		spin:   sp,
		inbox:  newStyledTable(),
		reader: viewport.New(0, 0),
		busy:   true, // startup lists the inbox
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, listCmd)
}

// ---- messages and commands -------------------------------------------------

type envelopesMsg struct {
	envelopes []envelope
	err       error
}

type messageMsg struct {
	envelope envelope
	text     string
	err      error
}

func listCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	envelopes, err := listEnvelopes(ctx)
	return envelopesMsg{envelopes: envelopes, err: err}
}

func readCmd(e envelope, width int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		text, err := readMessage(ctx, e.ID, width)
		return messageMsg{envelope: e, text: text, err: err}
	}
}

// ---- update ----------------------------------------------------------------

// Update sizes the panes after every message, because the error banner and the
// full help change the body height.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.update(msg)
	m.layout()
	return m, cmd
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case envelopesMsg:
		m.busy = false
		m.err = msg.err
		if msg.err == nil {
			m.envelopes = msg.envelopes
			rows := make([]table.Row, 0, len(m.envelopes))
			for _, e := range m.envelopes {
				rows = append(rows, table.Row{e.ID, oneLine(e.sender()), oneLine(e.Subject), shortDate(e.Date)})
			}
			m.inbox.SetRows(rows)
		}
		return m, nil

	case messageMsg:
		m.busy = false
		m.err = msg.err
		if msg.err == nil {
			m.open = &msg.envelope
			head := dimStyle.Render(oneLine(msg.envelope.sender()) + "  " + shortDate(msg.envelope.Date))
			m.reader.SetContent(head + "\n\n" + msg.text)
			m.reader.GotoTop()
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m, nil

	case key.Matches(msg, m.keys.Back):
		m.open = nil
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		if m.busy {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, listCmd)

	case key.Matches(msg, m.keys.Open):
		i := m.inbox.Cursor()
		if m.busy || m.open != nil || i < 0 || i >= len(m.envelopes) {
			return m, nil
		}
		m.busy = true
		// hread renders at 100 columns. A narrow pane gets its own width.
		return m, tea.Batch(m.spin.Tick, readCmd(m.envelopes[i], min(m.reader.Width, 100)))
	}

	var cmd tea.Cmd
	if m.open != nil {
		m.reader, cmd = m.reader.Update(msg)
	} else {
		m.inbox, cmd = m.inbox.Update(msg)
	}
	return m, cmd
}

// ---- view ------------------------------------------------------------------

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	top, banner, bottom, _ := m.chrome()

	var body string
	if m.open != nil {
		// 6 leaves room for the frame corners, the spaces and one rule column.
		title := titleStyle.MaxWidth(max(m.width-6, 1)).Render(oneLine(m.open.Subject))
		body = frame(title, m.reader.View(), m.width)
	} else {
		body = frame(titleStyle.Render(fmt.Sprintf("Inbox (%d)", len(m.envelopes))), m.inbox.View(), m.width)
	}

	parts := []string{top}
	if banner != "" {
		parts = append(parts, banner, "")
	}
	parts = append(parts, body, bottom)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// chrome renders everything around the body, and returns the height that is
// left for the body.
func (m model) chrome() (top, banner, bottom string, bodyH int) {
	bar := tabActiveStyle.Render("himaplaya")
	if m.busy {
		bar += "  " + m.spin.View()
	}
	top = tabBarStyle.MaxWidth(m.width).Render(bar)
	bottom = footerStyle.Width(m.width).Render(m.help.View(m.keys))
	if m.err != nil {
		banner = lipgloss.NewStyle().Width(m.width).Render(errorStyle.Render("error: ") + oneLine(m.err.Error()))
	}
	bodyH = m.height - lipgloss.Height(top) - lipgloss.Height(bottom)
	if banner != "" {
		bodyH -= lipgloss.Height(banner) + 1
	}
	return top, banner, bottom, max(bodyH, 3)
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	w := max(m.width-frameCols, 10)
	_, _, _, bodyH := m.chrome()
	h := max(bodyH-frameRows, 2)

	m.inbox.SetColumns(inboxColumns(w))
	m.inbox.SetWidth(w)
	m.inbox.SetHeight(h)
	m.reader.Width = w
	m.reader.Height = h
}
