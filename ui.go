package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

	tabStyle = lipgloss.NewStyle().Foreground(dim).Padding(0, 2)

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
	Account key.Binding
	Plain   key.Binding
	Images  key.Binding
	Sixel   key.Binding
	Prev    key.Binding
	Next    key.Binding
	Help    key.Binding
	Quit    key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:    key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		// The binding is on when himalaya lists more than one account.
		Account: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next account"), key.WithDisabled()),
		Plain:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "text/html")),
		Images:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "remote images")),
		Sixel:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sixel view")),
		Prev:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("left", "previous image")),
		Next:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("right", "next image")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Open, k.Back, k.Refresh, k.Account, k.Plain, k.Images, k.Sixel, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Open, k.Back, k.Refresh}, {k.Plain, k.Images}, {k.Sixel, k.Prev, k.Next}, {k.Account, k.Help, k.Quit}}
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

	accounts  []string   // the account names of the himalaya configuration
	account   string     // the account that the inbox shows. Empty is the default account.
	envelopes []envelope // ordered, mirrors inbox rows
	doc       *document  // the message in the reader. nil shows the inbox.
	plain     bool       // the reader shows the plain-text part, not the HTML part
	stale     bool       // the reader content does not match doc, plain or the size

	cache map[string]cachedInbox // the last inbox of each account, by account name

	term    terminal
	viewing bool   // the sixel view shows one image of doc
	picture int    // the index of that image in doc.pictures
	drawn   string // the body of the sixel view

	busy          bool // a himalaya call or an image fetch is in flight
	err           error
	width, height int
}

// inboxTTL is the time that an account switch can show a cached inbox.
const inboxTTL = 5 * time.Minute

// cachedInbox is the last inbox that himalaya listed for an account.
type cachedInbox struct {
	envelopes []envelope
	at        time.Time
}

func newModel(t terminal) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	return model{
		term:   t,
		keys:   newKeyMap(),
		help:   help.New(),
		spin:   sp,
		inbox:  newStyledTable(),
		reader: viewport.New(0, 0),
		cache:  map[string]cachedInbox{},
		busy:   true, // startup lists the accounts, then the inbox
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, accountsCmd)
}

// ---- messages and commands -------------------------------------------------

type accountsMsg struct {
	accounts []account
	err      error
}

type envelopesMsg struct {
	envelopes []envelope
	err       error
}

type messageMsg struct {
	doc *document
	err error
}

type imagesMsg struct {
	id     string // the envelope that the images belong to
	images map[string]image.Image
}

func accountsCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	accounts, err := listAccounts(ctx)
	return accountsMsg{accounts: accounts, err: err}
}

func listCmd(account string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		envelopes, err := listEnvelopes(ctx, account)
		return envelopesMsg{envelopes: envelopes, err: err}
	}
}

func readCmd(account string, e envelope) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		doc, err := readMessage(ctx, account, e)
		return messageMsg{doc: doc, err: err}
	}
}

func imagesCmd(id string, srcs []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return imagesMsg{id: id, images: fetchImages(ctx, srcs)}
	}
}

// ---- update ----------------------------------------------------------------

// Update sizes the panes after every message, because the error banner and the
// full help change the body height.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.update(msg)
	if m.layout() {
		// Bubble Tea only writes the lines that changed, and a blank line that
		// did not change keeps the pixels of the last sixel image. A clear
		// makes Bubble Tea write every line.
		cmd = tea.Batch(cmd, tea.ClearScreen)
	}
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

	case accountsMsg:
		m.err = msg.err
		if msg.err != nil {
			m.busy = false
			return m, nil
		}
		for _, a := range msg.accounts {
			m.accounts = append(m.accounts, a.Name)
			if a.Default {
				m.account = a.Name
			}
		}
		m.keys.Account.SetEnabled(len(m.accounts) > 1)
		return m, listCmd(m.account)

	case envelopesMsg:
		m.busy = false
		m.err = msg.err
		if msg.err == nil {
			// A list only runs when the model is busy, and the account cannot
			// change in that time.
			m.cache[m.account] = cachedInbox{envelopes: msg.envelopes, at: time.Now()}
			m.setEnvelopes(msg.envelopes)
		}
		return m, nil

	case messageMsg:
		m.busy = false
		m.err = msg.err
		if msg.err == nil {
			m.doc = msg.doc
			m.stale = true
			m.reader.GotoTop()
		}
		return m, nil

	case imagesMsg:
		m.busy = false
		// The user can open another message before the fetch ends.
		if m.doc != nil && m.doc.envelope.ID == msg.id {
			maps.Copy(m.doc.images, msg.images)
			m.doc.remote = slices.DeleteFunc(m.doc.remote, func(src string) bool { return msg.images[src] != nil })
			m.stale = true
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// setEnvelopes shows the envelopes in the inbox.
func (m *model) setEnvelopes(envelopes []envelope) {
	m.envelopes = envelopes
	rows := make([]table.Row, 0, len(envelopes))
	for _, e := range envelopes {
		rows = append(rows, table.Row{e.ID, oneLine(e.sender()), oneLine(e.Subject), shortDate(e.Date)})
	}
	m.inbox.SetRows(rows)
	// The table has no cursor after an account switch empties it.
	m.inbox.SetCursor(max(m.inbox.Cursor(), 0))
}

func (m model) handleKey(msg tea.KeyMsg) (model, tea.Cmd) {
	m.err = nil
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		return m, nil
	}

	if m.viewing {
		n := len(m.doc.pictures())
		switch {
		case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Sixel):
			m.viewing = false
			return m, tea.ClearScreen
		case key.Matches(msg, m.keys.Prev):
			m.picture = (m.picture + n - 1) % n
			m.stale = true
		case key.Matches(msg, m.keys.Next):
			m.picture = (m.picture + 1) % n
			m.stale = true
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		m.doc = nil
		return m, nil

	case key.Matches(msg, m.keys.Sixel):
		switch {
		case m.doc == nil:
		case !m.term.sixel:
			m.err = errors.New("the terminal does not report sixel support")
		case len(m.doc.pictures()) == 0:
			m.err = errors.New("this message has no loaded image")
		default:
			m.viewing, m.picture, m.stale = true, 0, true
		}
		return m, nil

	case key.Matches(msg, m.keys.Plain):
		m.plain = !m.plain
		m.stale = true
		return m, nil

	case key.Matches(msg, m.keys.Images):
		if m.busy || m.doc == nil || len(m.doc.remote) == 0 {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, imagesCmd(m.doc.envelope.ID, m.doc.remote))

	case key.Matches(msg, m.keys.Refresh):
		if m.busy {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, listCmd(m.account))

	case key.Matches(msg, m.keys.Account):
		if m.busy {
			return m, nil
		}
		m.account = m.accounts[(slices.Index(m.accounts, m.account)+1)%len(m.accounts)]
		// An envelope ID only applies to its account, so drop the old inbox.
		m.doc = nil
		m.setEnvelopes(nil)
		if c, ok := m.cache[m.account]; ok && time.Since(c.at) < inboxTTL {
			m.setEnvelopes(c.envelopes)
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, listCmd(m.account))

	case key.Matches(msg, m.keys.Open):
		i := m.inbox.Cursor()
		if m.busy || m.doc != nil || i < 0 || i >= len(m.envelopes) {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, readCmd(m.account, m.envelopes[i]))
	}

	var cmd tea.Cmd
	if m.doc != nil {
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
	if m.viewing {
		title := fmt.Sprintf("Image %d of %d", m.picture+1, len(m.doc.pictures()))
		body = frame(titleStyle.Render(title), m.drawn, m.width)
	} else if m.doc != nil {
		// 6 leaves room for the frame corners, the spaces and one rule column.
		title := titleStyle.MaxWidth(max(m.width-6, 1)).Render(oneLine(m.doc.envelope.Subject))
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
	var bar string
	for _, a := range m.accounts {
		style := tabStyle
		if a == m.account {
			style = tabActiveStyle
		}
		bar += style.Render(a)
	}
	if bar == "" {
		bar = tabActiveStyle.Render("himaplaya")
	}
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

// layout sizes the panes and renders the content that is stale. It reports
// whether it drew a new sixel image.
func (m *model) layout() (drew bool) {
	if m.width == 0 {
		return false
	}
	w := max(m.width-frameCols, 10)
	_, _, _, bodyH := m.chrome()
	h := max(bodyH-frameRows, 2)

	m.inbox.SetColumns(inboxColumns(w))
	m.inbox.SetWidth(w)
	m.inbox.SetHeight(h)
	if m.reader.Width != w || m.reader.Height != h {
		m.stale = true
	}
	m.reader.Width = w
	m.reader.Height = h
	if m.doc == nil || !m.stale {
		return false
	}
	m.stale = false
	m.reader.SetContent(m.readerContent())
	if m.viewing {
		m.drawn = m.sixelView(w, h)
	}
	return m.viewing
}

// sixelView draws the selected image in an area of cols by rows cells. The
// first row is a caption. The image keeps its size, unless it must shrink to
// fit.
//
// ponytail: the view starts at the first image and shows one image at a time.
// The upgrade path is to start at the image that the reader shows.
func (m model) sixelView(cols, rows int) string {
	f := m.doc.pictures()[m.picture]
	img := m.doc.images[f.src]
	size := img.Bounds().Size()
	caption := dimStyle.Render(fmt.Sprintf("%d x %d px", size.X, size.Y))
	if f.href != "" {
		text := ansi.Truncate(f.href, max(cols-lipgloss.Width(caption)-2, 0), "…")
		caption += "  " + hyperlink(f.href, titleStyle.Underline(true).Render(text))
	}

	w, h := size.X, size.Y
	if maxW := cols * m.term.cell.X; w > maxW {
		w, h = maxW, h*maxW/w
	}
	if maxH := (rows - 1) * m.term.cell.Y; h > maxH {
		w, h = w*maxH/h, maxH
	}
	w, h = max(w, 1), max(h, 1)
	used := (h + m.term.cell.Y - 1) / m.term.cell.Y // the rows that the image covers

	// A sixel image is one escape sequence that draws down from the cursor.
	// Bubble Tea writes the lines from the top, and a blank line that it
	// writes after the image erases the pixels in that row. So the sequence
	// is at the end of the last row of the image: it saves the cursor, moves
	// to the first cell of the image, draws, and restores the cursor. The
	// spaces before it give the line its full width, so that the frame adds
	// no padding over the image.
	draw := strings.Repeat(" ", cols) + "\x1b7" + fmt.Sprintf("\x1b[%dD", cols)
	if used > 1 {
		draw += fmt.Sprintf("\x1b[%dA", used-1)
	}
	draw += sixel(img, w, h) + "\x1b8"

	lines := make([]string, rows)
	lines[0] = caption
	lines[min(used, rows-1)] = draw
	return strings.Join(lines, "\n")
}

// readerContent renders the open message for the reader width. It shows the
// HTML part, unless the user asked for plain text or the message has no HTML
// part.
func (m model) readerContent() string {
	d := m.doc
	// 100 columns is the widest line that reads well, and the width that hread uses.
	w := min(m.reader.Width, 100)
	head := oneLine(d.envelope.sender()) + "  " + shortDate(d.envelope.Date)

	var body string
	switch {
	case d.root != nil && !m.plain:
		body = renderHTML(d.root, w, d.images)
		if n := len(d.remote); n > 0 {
			head += fmt.Sprintf("  %d remote images, i to load", n)
		}
	case d.text != "":
		head += "  plain text"
		body = ansi.Wrap(d.text, w, "")
	default:
		// The message has no plain-text part, so remove the styles from the HTML part.
		head += "  plain text"
		body = ansi.Strip(renderHTML(d.root, w, nil))
	}
	return dimStyle.Render(head) + "\n\n" + body
}
