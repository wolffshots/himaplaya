package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"maps"
	"os"
	"path/filepath"
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
	Auto    key.Binding
	Account key.Binding
	Plain   key.Binding
	Images  key.Binding
	Save    key.Binding
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
		Auto:    key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "auto refresh")),
		// The binding is on when himalaya lists more than one account. The
		// shift+tab key goes to the previous account. The help does not show
		// it, because the full help has no room for it at 80 columns.
		Account: key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "next account"), key.WithDisabled()),
		Plain:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "text/html")),
		Images:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "remote images")),
		Save:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "save attachments")),
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
	return [][]key.Binding{{k.Open, k.Back, k.Refresh}, {k.Plain, k.Images, k.Save, k.Auto}, {k.Sixel, k.Prev, k.Next}, {k.Account, k.Help, k.Quit}}
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
	kitty   *kitty // draws the images of the reader. nil draws half blocks.
	viewing bool   // the sixel view shows one image of doc
	picture int    // the index of that image in doc.pictures
	drawn   string // the body of the sixel view

	auto  bool // the auto refresh is on
	timer int  // the number of the auto refresh timer. A tick of an older timer does nothing.

	busy          bool // a himalaya call or an image fetch is in flight
	loading       bool // a list that no key asked for is in flight. It only stops another list.
	err           error
	note          string // the result of the last command, for the banner
	width, height int
}

// inboxTTL is the time that an account switch can show a cached inbox.
const inboxTTL = 5 * time.Minute

// autoInterval is the time between two lists of the auto refresh.
const autoInterval = time.Minute

// cachedInbox is the last inbox that himalaya listed for an account.
type cachedInbox struct {
	envelopes []envelope
	at        time.Time // the time of the first page
	pages     int       // the pages that envelopes holds
	end       bool      // the last page added no envelope
}

func newModel(t terminal) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)

	var k *kitty
	if t.kitty {
		k = &kitty{cell: t.cell, ids: map[string]int{}, out: os.Stdout}
	}
	return model{
		term:   t,
		kitty:  k,
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
	page      int  // a page after page 1 extends the inbox
	keep      bool // page 1 keeps the envelopes of the pages after it
	envelopes []envelope
	err       error
}

// autoMsg is a tick of the auto refresh. It holds the number of its timer.
type autoMsg int

type messageMsg struct {
	doc *document
	err error
}

type imagesMsg struct {
	id     string // the envelope that the images belong to
	images map[string]image.Image
}

type attachmentsMsg struct {
	paths []string // the files that himalaya wrote
	err   error
}

func accountsCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	accounts, err := listAccounts(ctx)
	return accountsMsg{accounts: accounts, err: err}
}

func listCmd(account string, page int, keep bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		envelopes, err := listEnvelopes(ctx, account, page)
		return envelopesMsg{page: page, keep: keep, envelopes: envelopes, err: err}
	}
}

func autoCmd(timer int) tea.Cmd {
	return tea.Tick(autoInterval, func(time.Time) tea.Msg { return autoMsg(timer) })
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

func attachmentsCmd(account, id string) tea.Cmd {
	return func() tea.Msg {
		// A large attachment needs more time than a list.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		paths, err := downloadAttachments(ctx, account, id)
		return attachmentsMsg{paths: paths, err: err}
	}
}

// ---- update ----------------------------------------------------------------

// Update sizes the panes after every message, because the error banner and the
// full help change the body height.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	doc := m.doc
	m, cmd := m.update(msg)
	if m.doc != doc {
		// The terminal keeps the images of a message until a command deletes them.
		m.kitty.forget()
	}
	if m.layout() {
		// Bubble Tea only writes the lines that changed, and a blank line that
		// did not change keeps the pixels of the last sixel image. A clear
		// makes Bubble Tea write every line.
		cmd = tea.Batch(cmd, tea.ClearScreen)
	}
	// The check needs the inbox height that layout sets.
	next := m.nextPage()
	return m, tea.Batch(cmd, next)
}

// nextPage lists the next page when the inbox shows its last envelope.
//
// ponytail: the table does not report the rows that it shows. The check is
// true when the last envelope is selected or when all envelopes fit. It is
// false when a taller window shows the last envelope below the selected one.
// The upgrade path is a table that reports its scroll offset.
func (m *model) nextPage() tea.Cmd {
	c, n := m.cache[m.account], len(m.envelopes)
	// An error stops the lists until the next key press. A list that fails
	// each time must not run in a loop.
	if m.busy || m.loading || m.err != nil || m.doc != nil || n == 0 || c.end {
		return nil
	}
	if m.inbox.Cursor() != n-1 && n > m.inbox.Height() {
		return nil
	}
	// An inbox that shows envelopes is always in the cache.
	m.loading = true
	return tea.Batch(m.spin.Tick, listCmd(m.account, c.pages+1, false))
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		return m, nil

	case spinner.TickMsg:
		if !m.busy && !m.loading {
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
		return m, listCmd(m.account, 1, false)

	case autoMsg:
		if !m.auto || int(msg) != m.timer {
			return m, nil
		}
		if m.busy || m.loading {
			return m, autoCmd(m.timer)
		}
		// The list is for the account that the inbox shows at this time.
		m.loading = true
		return m, tea.Batch(m.spin.Tick, autoCmd(m.timer), listCmd(m.account, 1, true))

	case envelopesMsg:
		if msg.page > 1 || msg.keep {
			m.loading = false
		} else {
			m.busy = false
		}
		// A list that no key asked for must not remove the banner of a key.
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// A list only runs when the model is busy or loading, and the account
		// cannot change in that time.
		c := cachedInbox{envelopes: msg.envelopes, at: time.Now(), pages: 1}
		old := m.cache[m.account]
		index := func(id string) int {
			return slices.IndexFunc(old.envelopes, func(o envelope) bool { return o.ID == id })
		}
		switch {
		case msg.page > 1:
			c = old
			// A new message moves the last envelope of each page to the next
			// page, so skip an envelope that the inbox already shows.
			for _, e := range msg.envelopes {
				if index(e.ID) < 0 {
					c.envelopes = append(c.envelopes, e)
				}
			}
			// The next page lists without a key press. A backend that gives
			// the same page for each number must not make a loop.
			c.pages, c.end = msg.page, len(c.envelopes) == len(old.envelopes)
		case msg.keep:
			// The old envelopes after the last envelope of the new page are
			// the envelopes of the other pages. With no such envelope, the
			// new page replaces the inbox.
			for i := len(msg.envelopes) - 1; i >= 0; i-- {
				if j := index(msg.envelopes[i].ID); j >= 0 {
					c.envelopes = append(c.envelopes, old.envelopes[j+1:]...)
					c.pages, c.end = old.pages, old.end
					break
				}
			}
		}
		m.cache[m.account] = c

		var selected string
		if i := m.inbox.Cursor(); i >= 0 && i < len(m.envelopes) {
			selected = m.envelopes[i].ID
		}
		m.setEnvelopes(c.envelopes)
		if !msg.keep {
			return m, nil
		}
		// A new message moves each envelope down, so move the cursor with the
		// selected envelope. SetCursor can put the cursor below the last row
		// that the table shows.
		if i := slices.IndexFunc(c.envelopes, func(e envelope) bool { return e.ID == selected }); i >= 0 {
			if d := i - m.inbox.Cursor(); d > 0 {
				m.inbox.MoveDown(d)
			} else {
				m.inbox.MoveUp(-d)
			}
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

	case attachmentsMsg:
		m.busy = false
		m.err = msg.err
		switch {
		case msg.err != nil:
		case len(msg.paths) == 0:
			m.err = errors.New("this message has no attachment")
		default:
			names := make([]string, len(msg.paths))
			for i, p := range msg.paths {
				names[i] = filepath.Base(p)
			}
			m.note = "saved " + strings.Join(names, ", ") + " to " + filepath.Dir(msg.paths[0])
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
	m.err, m.note = nil, ""
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.kitty.forget()
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
		case m.kitty != nil:
			m.err = errors.New("the reader already shows each image at the full resolution")
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

	case key.Matches(msg, m.keys.Save):
		if m.busy || m.doc == nil {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, attachmentsCmd(m.account, m.doc.envelope.ID))

	case key.Matches(msg, m.keys.Refresh):
		if m.busy || m.loading {
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, listCmd(m.account, 1, false))

	case key.Matches(msg, m.keys.Auto):
		m.auto = !m.auto
		if !m.auto {
			return m, nil
		}
		m.timer++
		return m, autoCmd(m.timer)

	case key.Matches(msg, m.keys.Account):
		if m.busy || m.loading {
			return m, nil
		}
		step := 1
		if msg.Type == tea.KeyShiftTab {
			step = len(m.accounts) - 1
		}
		m.account = m.accounts[(slices.Index(m.accounts, m.account)+step)%len(m.accounts)]
		// An envelope ID only applies to its account, so drop the old inbox.
		m.doc = nil
		m.setEnvelopes(nil)
		if c, ok := m.cache[m.account]; ok && time.Since(c.at) < inboxTTL {
			m.setEnvelopes(c.envelopes)
			return m, nil
		}
		m.busy = true
		return m, tea.Batch(m.spin.Tick, listCmd(m.account, 1, false))

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
	if m.auto {
		bar += "  " + dimStyle.Render("auto refresh")
	}
	if m.busy || m.loading {
		bar += "  " + m.spin.View()
	}
	top = tabBarStyle.MaxWidth(m.width).Render(bar)
	bottom = footerStyle.Width(m.width).Render(m.help.View(m.keys))
	if m.err != nil {
		banner = lipgloss.NewStyle().Width(m.width).Render(errorStyle.Render("error: ") + oneLine(m.err.Error()))
	} else if m.note != "" {
		banner = lipgloss.NewStyle().Width(m.width).Render(oneLine(m.note))
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

	if m.reader.Width != w || m.reader.Height != h {
		m.stale = true
		// Each of these calls renders the rows of the inbox. Only a new size
		// needs them, or a stream of key presses makes the terminal wait.
		m.inbox.SetColumns(inboxColumns(w))
		m.inbox.SetWidth(w)
		m.inbox.SetHeight(h)
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
		body = renderHTML(d.root, w, d.images, m.kitty)
		if n := len(d.remote); n > 0 {
			head += fmt.Sprintf("  %d remote images, i to load", n)
		}
	case d.text != "":
		head += "  plain text"
		body = ansi.Wrap(d.text, w, "")
	default:
		// The message has no plain-text part, so remove the styles from the HTML part.
		head += "  plain text"
		body = ansi.Strip(renderHTML(d.root, w, nil, nil))
	}
	return dimStyle.Render(head) + "\n\n" + body
}
