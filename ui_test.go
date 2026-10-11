package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/net/html"
)

// testPNG returns a 16 by 16 image. The top half is red and the bottom half is blue.
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
			if y >= 8 {
				img.Set(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func parse(t *testing.T, s string) *html.Node {
	t.Helper()
	root, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMessageParts(t *testing.T) {
	const in = `{"html_body":[2],"text_body":[1],"parts":[
		{"body":{"Multipart":[1,2]}},
		{"body":{"Text":"hi"}},
		{"body":{"Html":"<p>hi</p>"}},
		{"headers":[{"name":{"Other":"X-Mailer"},"value":{"Text":"x"}},{"name":"content_id","value":{"Text":"logo@x"}}],"body":{"InlineBinary":[1,2,3]}}]}`
	var m message
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	if m.html() != "<p>hi</p>" || m.text() != "hi" {
		t.Errorf("got html %q and text %q", m.html(), m.text())
	}
	if got := m.inline("logo@x"); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Errorf("inline: got %v", got)
	}
	if got := m.inline("other"); got != nil {
		t.Errorf("inline with an unknown id: got %v", got)
	}

	// himalaya points both indexes at the one part of a text-only message.
	m = message{HTMLBody: []int{0}, TextBody: []int{0}, Parts: []part{{Body: partBody{Text: "hi"}}}}
	if m.html() != "" || m.text() != "hi" {
		t.Errorf("text only: got html %q and text %q", m.html(), m.text())
	}
	if got := (message{HTMLBody: []int{3}}).html(); got != "" {
		t.Errorf("bad index: got %q", got)
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

func TestRenderHTML(t *testing.T) {
	const in = `<html><head><title>no</title><style>p{}</style></head><body>
		<div style="display: none">preheader&zwnj;&nbsp;</div>
		<h1>Title</h1>
		<p>one <b>two</b> three four five six seven</p>
		<ul><li>first</li><li>second</li></ul>
		<ol><li>a</li><li>b</li></ol>
		<blockquote>quoted</blockquote>
		line<br>break &#27;[2J
		<a href="https://example.com/x">link text</a>
		<a href="javascript:alert(1)">bad</a>
		<img src="https://example.com/a.png" alt="A logo">
		<img src="https://example.com/pixel.gif" width="1" height="1" alt="pixel">
		</body></html>`
	const width = 20
	out := renderHTML(parse(t, in), width, nil)

	want := strings.Join([]string{
		"Title",
		"",
		"one two three four",
		"five six seven",
		"",
		"• first",
		"• second",
		"",
		"1. a",
		"2. b",
		"",
		"│ quoted",
		"",
		"line",
		"break [2J link text",
		"bad [A logo]",
	}, "\n")
	if got := ansi.Strip(out); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line %q is %d columns, want at most %d", ansi.Strip(line), w, width)
		}
	}
	// Each word of a safe link is an OSC 8 link. An unsafe link is plain text.
	if n := strings.Count(out, "\x1b]8;;https://example.com/x\x1b\\"); n != 2 {
		t.Errorf("got %d link words, want 2", n)
	}
	if strings.Contains(out, "javascript") {
		t.Error("the output has a javascript link")
	}
}

func TestRenderHTMLCutsLongWord(t *testing.T) {
	got := renderHTML(parse(t, "<p>ab abcdefghij</p>"), 4, nil)
	if want := "ab\nabcd\nefgh\nij"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHalfBlocks(t *testing.T) {
	img, err := decodeImage(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	// 2 columns give 2 by 2 pixels, which is 1 row of cells.
	cell := "\x1b[38;2;255;0;0;48;2;0;0;255m▀"
	if got, want := halfBlocks(img, 2), []string{cell + cell + "\x1b[0m"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %q, want %q", got, want)
	}
	// A transparent pixel shows as white.
	px := scale(image.NewRGBA(image.Rect(0, 0, 4, 4)), 1, 1)
	if px[0] != [3]uint8{255, 255, 255} {
		t.Errorf("transparent pixel: got %v", px[0])
	}
}

// decodeSixel reads the output of sixel. It returns the color of each pixel as
// the percentages that the palette gives.
func decodeSixel(t *testing.T, s string) (size image.Point, px map[image.Point][3]int) {
	t.Helper()
	s, ok := strings.CutPrefix(s, "\x1bP0;1;0q")
	s, ok2 := strings.CutSuffix(s, "\x1b\\")
	if !ok || !ok2 {
		t.Fatal("the sixel sequence has no DCS start or no string terminator")
	}
	// number reads the decimal number at the start of s.
	number := func() int {
		n := 0
		for ; s != "" && s[0] >= '0' && s[0] <= '9'; s = s[1:] {
			n = n*10 + int(s[0]-'0')
		}
		return n
	}
	palette := map[int][3]int{}
	px = map[image.Point][3]int{}
	var x, y, color int
	for s != "" {
		ch := s[0]
		s = s[1:]
		repeat := 1
		switch {
		case ch == '"':
			var v [4]int
			for i := range v {
				v[i] = number()
				s = strings.TrimPrefix(s, ";")
			}
			size = image.Pt(v[2], v[3])
			continue
		case ch == '#':
			color = number()
			if strings.HasPrefix(s, ";2;") {
				s = s[3:]
				var v [3]int
				for i := range v {
					v[i] = number()
					s = strings.TrimPrefix(s, ";")
				}
				palette[color] = v
			}
			continue
		case ch == '$':
			x = 0
			continue
		case ch == '-':
			x, y = 0, y+6
			continue
		case ch == '!':
			repeat = number()
			ch = s[0]
			s = s[1:]
		}
		if ch < '?' || ch > '~' {
			t.Fatalf("unexpected sixel byte %q", ch)
		}
		for ; repeat > 0; repeat-- {
			for dy := range 6 {
				if (ch-'?')>>dy&1 == 1 {
					px[image.Pt(x, y+dy)] = palette[color]
				}
			}
			x++
		}
	}
	return size, px
}

func TestSixel(t *testing.T) {
	img, err := decodeImage(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	// 16 rows are 2 full bands and 1 band of 4 rows.
	size, px := decodeSixel(t, sixel(img, 16, 16))
	if size != image.Pt(16, 16) || len(px) != 16*16 {
		t.Fatalf("got size %v and %d pixels, want 16 by 16", size, len(px))
	}
	for p, c := range px {
		want := [3]int{100, 0, 0}
		if p.Y >= 8 {
			want = [3]int{0, 0, 100}
		}
		if c != want {
			t.Fatalf("pixel %v: got %v, want %v", p, c, want)
		}
	}

	// The dither keeps the end values, and a middle value uses both neighbors.
	for _, v := range []uint8{0, 255} {
		for i := range 16 {
			if got, want := quantize(v, 6, i%4, i/4), int(v)/255*5; got != want {
				t.Errorf("quantize(%d) at %d: got %d, want %d", v, i, got, want)
			}
		}
	}
	seen := map[int]bool{}
	for i := range 16 {
		seen[quantize(128, 6, i%4, i/4)] = true
	}
	if !seen[2] || !seen[3] || len(seen) != 2 {
		t.Errorf("quantize(128): got levels %v, want 2 and 3", seen)
	}
}

func TestParseTerminal(t *testing.T) {
	base := terminal{cell: image.Pt(10, 20)}
	if got := parseTerminal(base, []byte("\x1b[6;36;17t\x1b[?61;4;6;22c")); got != (terminal{cell: image.Pt(17, 36), sixel: true}) {
		t.Errorf("with sixel: got %+v", got)
	}
	// 14 has a 4 in it, but it is not attribute 4.
	if got := parseTerminal(base, []byte("\x1b[?1;14c")); got != base {
		t.Errorf("without sixel: got %+v", got)
	}
}

// A message shows the images that it carries. It only lists a remote image.
func TestDocumentImages(t *testing.T) {
	data := testPNG(t)
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	body := `<a href="https://example.com/go"><b><img src="cid:logo@x"></b></a><img src="` + dataURI + `">
		<a href="https://example.com/far"><img src="https://example.com/a.png"></a><img src="cid:missing">`

	cidPart := part{Body: partBody{Binary: data}}
	if err := json.Unmarshal([]byte(`[{"name":"content_id","value":{"Text":"<logo@x>"}}]`), &cidPart.Headers); err != nil {
		t.Fatal(err)
	}
	m := message{HTMLBody: []int{0}, Parts: []part{{Body: partBody{HTML: body}}, cidPart}}

	d, err := newDocument(envelope{ID: "1"}, m)
	if err != nil {
		t.Fatal(err)
	}
	if d.images["cid:logo@x"] == nil || d.images[dataURI] == nil || len(d.images) != 2 {
		t.Errorf("got %d local images, want the cid image and the data image", len(d.images))
	}
	if fmt.Sprint(d.remote) != "[https://example.com/a.png]" {
		t.Errorf("remote: got %v", d.remote)
	}
	if got := fmt.Sprint(d.figures); got != "[{cid:logo@x https://example.com/go} {"+dataURI+" } {https://example.com/a.png https://example.com/far} {cid:missing }]" {
		t.Errorf("figures: got %v", got)
	}
	if got := d.pictures(); len(got) != 2 || got[0].src != "cid:logo@x" {
		t.Errorf("pictures: got %v", got)
	}

	out := renderHTML(d.root, 80, d.images)
	// Each 16 pixel image is 2 columns and 1 row.
	if got := strings.Count(out, "▀"); got != 4 {
		t.Errorf("got %d half blocks, want 4", got)
	}
	// An image in a link is a link. So is the placeholder of a remote image
	// with no alt text.
	if !strings.Contains(out, "\x1b]8;;https://example.com/go\x1b\\\x1b[38;2;255;0;0;48;2;0;0;255m▀") {
		t.Error("the half blocks of the image in a link are not a link")
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com/far\x1b\\[image]") {
		t.Error("the placeholder of the remote image in a link is not a link")
	}
}

func TestFetchImages(t *testing.T) {
	data := testPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Write(data)
		case "/not-an-image":
			w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got := fetchImages(context.Background(), []string{srv.URL + "/ok.png", srv.URL + "/not-an-image", srv.URL + "/missing.png"})
	if len(got) != 1 || got[srv.URL+"/ok.png"] == nil {
		t.Errorf("got %d images, want only ok.png", len(got))
	}
}

// The view must fill the window exactly in every state, or the header scrolls
// off the alternate screen.
func TestViewFitsWindow(t *testing.T) {
	const w, h = 80, 24
	e := envelope{ID: "1", Subject: strings.Repeat("long subject ", 20), From: []address{{Email: "a@example.com"}}, Date: "2026-10-11T10:02:35Z"}
	doc := &document{
		envelope: e,
		text:     strings.Repeat("plain ", 200),
		root:     parse(t, strings.Repeat("<p>"+strings.Repeat("word ", 40)+"</p>", 20)),
		figures:  []figure{{src: "https://example.com/a.png", href: "https://example.com/" + strings.Repeat("long/", 30)}},
		images:   map[string]image.Image{},
		remote:   []string{"https://example.com/a.png"},
	}
	key := func(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	// A large image must shrink to fit the sixel view.
	large := image.NewRGBA(image.Rect(0, 0, 2000, 3000))

	steps := []struct {
		name string
		msg  tea.Msg
		want string // text that the view must show
	}{
		{"empty", tea.WindowSizeMsg{Width: w, Height: h}, "Inbox (0)"},
		{"inbox", envelopesMsg{envelopes: []envelope{e, e, e}}, "Inbox (3)"},
		{"error banner", envelopesMsg{err: errors.New("himalaya: no connection")}, "no connection"},
		{"reader", messageMsg{doc: doc}, "1 remote images, i to load"},
		{"plain text", key("t"), "plain text"},
		{"html", key("t"), "word word"},
		{"no image for sixel", key("s"), "this message has no loaded image"},
		{"images loaded", imagesMsg{id: "1", images: map[string]image.Image{"https://example.com/a.png": large}}, "a@example.com  Oct"},
		{"sixel view", key("s"), "Image 1 of 1"},
		{"sixel caption", key("l"), "2000 x 3000 px  https://example.com/long/"},
		{"full help", key("?"), "next image"},
		{"narrow", tea.WindowSizeMsg{Width: 40, Height: h}, "Image 1 of 1"},
		{"reader again", tea.KeyMsg{Type: tea.KeyEsc}, "word"},
		{"inbox again", tea.KeyMsg{Type: tea.KeyEsc}, "Inbox (3)"},
	}

	var m tea.Model = newModel(terminal{cell: image.Pt(10, 20), sixel: true})
	width := w
	for _, s := range steps {
		if size, ok := s.msg.(tea.WindowSizeMsg); ok {
			width = size.Width
		}
		m, _ = m.Update(s.msg)
		view := m.View()
		if got := lipgloss.Height(view); got != h {
			t.Errorf("%s: view is %d rows, want %d", s.name, got, h)
		}
		if got := lipgloss.Width(view); got > width {
			t.Errorf("%s: view is %d columns, want at most %d", s.name, got, width)
		}
		if !strings.Contains(ansi.Strip(view), s.want) {
			t.Errorf("%s: view does not show %q", s.name, s.want)
		}
		if strings.HasPrefix(s.name, "sixel") {
			checkSixelView(t, view, width, h)
		}
	}
	if got := doc.remote; len(got) != 0 {
		t.Errorf("remote after the load: got %v", got)
	}

	m = newModel(terminal{cell: image.Pt(10, 20)})
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: w, Height: h}, messageMsg{doc: doc}, key("s")} {
		m, _ = m.Update(msg)
	}
	if !strings.Contains(m.View(), "the terminal does not report sixel support") {
		t.Error("the s key did not report a terminal with no sixel support")
	}
}

// The tab key shows the inbox of the next account. An envelope of the last
// account must not stay open or stay in the inbox.
func TestSwitchAccount(t *testing.T) {
	const w, h = 80, 24
	e := envelope{ID: "1", Subject: "from home"}
	tab := tea.KeyMsg{Type: tea.KeyTab}
	state := func(m tea.Model) string {
		mm := m.(model)
		return fmt.Sprintf("%s %d %v %v", mm.account, len(mm.envelopes), mm.doc != nil, mm.busy)
	}

	var m tea.Model = newModel(terminal{})
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m, _ = m.Update(tab)
	if got := state(m); got != " 0 false true" {
		t.Errorf("tab before the accounts load: got %q", got)
	}

	steps := []struct {
		name string
		msg  tea.Msg
		want string // the account, the envelope count, an open message, busy
	}{
		{"accounts", accountsMsg{accounts: []account{{Name: "work"}, {Name: "home", Default: true}}}, "home 0 false true"},
		{"inbox", envelopesMsg{envelopes: []envelope{e, e}}, "home 2 false false"},
		{"reader", messageMsg{doc: &document{envelope: e, text: "hi"}}, "home 2 true false"},
		{"switch", tab, "work 0 false true"},
		{"switch when busy", tab, "work 0 false true"},
		{"next inbox", envelopesMsg{envelopes: []envelope{e}}, "work 1 false false"},
		{"cached", tab, "home 2 false false"},
		{"cached again", tab, "work 1 false false"},
	}
	for _, s := range steps {
		m, _ = m.Update(s.msg)
		if got := state(m); got != s.want {
			t.Errorf("%s: got %q, want %q", s.name, got, s.want)
		}
		view := ansi.Strip(m.View())
		if got := lipgloss.Height(view); got != h {
			t.Errorf("%s: view is %d rows, want %d", s.name, got, h)
		}
		if !strings.Contains(view, "work") || !strings.Contains(view, "home") {
			t.Errorf("%s: the tab bar does not show both accounts", s.name)
		}
	}

	// A cached inbox selects its first envelope.
	for _, msg := range []tea.Msg{tab, tea.KeyMsg{Type: tea.KeyDown}, tab, tab} {
		m, _ = m.Update(msg)
	}
	if got := m.(model).inbox.Cursor(); got != 0 || state(m) != "home 2 false false" {
		t.Errorf("cached inbox: got cursor %d and %q", got, state(m))
	}

	// An old inbox is listed again, and the new inbox selects its first envelope.
	cache := m.(model).cache
	cache["work"] = cachedInbox{envelopes: cache["work"].envelopes, at: time.Now().Add(-inboxTTL)}
	m, _ = m.Update(tab)
	if got := state(m); got != "work 0 false true" {
		t.Errorf("old inbox: got %q", got)
	}
	m, _ = m.Update(envelopesMsg{envelopes: []envelope{e, e}})
	if got := m.(model).inbox.Cursor(); got != 0 || state(m) != "work 2 false false" {
		t.Errorf("new inbox: got cursor %d and %q", got, state(m))
	}
	// The refresh key lists a cached inbox again.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got := state(m); got != "work 2 false true" {
		t.Errorf("refresh: got %q", got)
	}

	// One account leaves the tab key off.
	m = newModel(terminal{})
	for _, msg := range []tea.Msg{accountsMsg{accounts: []account{{Name: "home", Default: true}}}, envelopesMsg{}, tab} {
		m, _ = m.Update(msg)
	}
	if got := state(m); got != "home 0 false false" {
		t.Errorf("tab with one account: got %q", got)
	}
}

// checkSixelView checks that the image fits the frame of the sixel view. The
// draw sequence must start at the first cell of the image and stay above the
// footer.
func checkSixelView(t *testing.T, view string, width, height int) {
	t.Helper()
	const cellW, cellH = 10, 20
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		var cols, up, w, h int
		_, seq, ok := strings.Cut(line, "\x1b7")
		if !ok {
			continue
		}
		if _, err := fmt.Sscanf(seq, "\x1b[%dD\x1b[%dA\x1bP0;1;0q\"1;1;%d;%d", &cols, &up, &w, &h); err != nil {
			t.Fatalf("draw sequence: %v", err)
		}
		// The frame takes 2 columns on each side. The tab bar, the top edge
		// and the caption take 4 rows.
		if cols != width-4 || w > cols*cellW {
			t.Errorf("the image is %d pixels wide in %d columns of a %d column window", w, cols, width)
		}
		if first := i - up; first != 4 || (h+cellH-1)/cellH != up+1 {
			t.Errorf("the image of %d pixels starts at row %d and ends at row %d", h, first, i)
		}
		if !strings.HasSuffix(line, "\x1b\\\x1b8 │") && !strings.Contains(line, "\x1b\\\x1b8 \x1b") {
			t.Errorf("the line does not restore the cursor before the right edge")
		}
		return
	}
	t.Error("the view has no draw sequence")
}
