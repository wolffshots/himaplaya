package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
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
	out := renderHTML(parse(t, in), width, nil, nil)

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
	got := renderHTML(parse(t, "<p>ab abcdefghij</p>"), 4, nil, nil)
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
	if got := parseTerminal(base, []byte("\x1bP>|ghostty 1.2.0\x1b\\\x1b[6;36;17t\x1b[?62;22c")); got != (terminal{cell: image.Pt(17, 36), kitty: true}) {
		t.Errorf("ghostty: got %+v", got)
	}
	// tmux needs a wrapper around each Kitty command, so it gets half blocks.
	if got := parseTerminal(base, []byte("\x1bP>|tmux 3.5a\x1b\\\x1b[?1;2c")); got != base {
		t.Errorf("tmux: got %+v", got)
	}
}

func TestKitty(t *testing.T) {
	// Noise does not compress, so the image needs more than one command.
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	var out bytes.Buffer
	k := &kitty{cell: image.Pt(10, 20), ids: map[string]int{}, out: &out}

	// 7 columns are 70 pixels. The image is square, so it takes 4 rows. The
	// terminal makes the image larger, so the data keeps the 64 pixels.
	lines := k.lines("a", img, 7)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	// These are the first 4 row diacritics of the protocol.
	for y, d := range []rune{0x305, 0x30d, 0x30e, 0x310} {
		want := "\x1b[38;2;0;0;1m" + placeholder + string(d) + strings.Repeat(placeholder, 6) + "\x1b[0m"
		if lines[y] != want {
			t.Errorf("line %d: got %q, want %q", y, lines[y], want)
		}
		if got := ansi.StringWidth(lines[y]); got != 7 {
			t.Errorf("line %d is %d columns, want 7", y, got)
		}
	}

	cmds := strings.Split(strings.TrimSuffix(out.String(), "\x1b\\"), "\x1b\\")
	if len(cmds) < 4 {
		t.Fatalf("got %d commands, want 3 or more with data and 1 placement", len(cmds))
	}
	var data string
	for i, cmd := range cmds {
		keys, chunk, _ := strings.Cut(strings.TrimPrefix(cmd, "\x1b_G"), ";")
		wantKeys := "m=1"
		switch i {
		case 0:
			wantKeys = "a=t,i=1,f=24,s=64,v=64,o=z,q=2,m=1"
		case len(cmds) - 2:
			wantKeys = "m=0"
		case len(cmds) - 1:
			wantKeys = "a=p,U=1,i=1,p=1,c=7,r=4,q=2"
		}
		if keys != wantKeys || len(chunk) > 4096 {
			t.Errorf("command %d: got keys %q and %d bytes of data, want keys %q", i, keys, len(chunk), wantKeys)
		}
		data += chunk
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	var px []byte
	for _, p := range scale(img, 64, 64) {
		px = append(px, p[:]...)
	}
	if !bytes.Equal(got, px) {
		t.Errorf("the data is %d bytes and is not the %d bytes of the pixels", len(got), len(px))
	}

	// The terminal has the image, so the next draw only sends a placement.
	out.Reset()
	k.lines("a", img, 2)
	if got, want := out.String(), "\x1b_Ga=p,U=1,i=1,p=1,c=2,r=1,q=2\x1b\\"; got != want {
		t.Errorf("second draw: got %q, want %q", got, want)
	}

	// An image that needs more than 297 rows must shrink.
	lines = k.lines("b", image.NewRGBA(image.Rect(0, 0, 8, 8000)), 10)
	if len(lines) != 297 || ansi.StringWidth(lines[296]) != 1 || !strings.HasSuffix(out.String(), "\x1b_Ga=p,U=1,i=2,p=1,c=1,r=297,q=2\x1b\\") {
		t.Errorf("tall image: got %d lines and the commands end with %q", len(lines), out.String()[max(out.Len()-40, 0):])
	}

	out.Reset()
	k.forget()
	for _, want := range []string{"\x1b_Ga=d,d=I,i=1,q=2\x1b\\", "\x1b_Ga=d,d=I,i=2,q=2\x1b\\"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("forget: got %q, want %q in it", out.String(), want)
		}
	}
	if len(k.ids) != 0 {
		t.Errorf("forget: %d images stay", len(k.ids))
	}
}

// A terminal with Kitty placeholders shows an image in the reader at the full
// resolution, so it has no sixel view.
func TestKittyReader(t *testing.T) {
	const w, h = 80, 24
	img, err := decodeImage(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := &document{
		envelope: envelope{ID: "1"},
		root:     parse(t, `<p>before</p><a href="https://example.com/go"><img src="cid:a"></a><p>after</p>`),
		images:   map[string]image.Image{"cid:a": img},
	}
	var out bytes.Buffer
	start := newModel(terminal{cell: image.Pt(10, 20), kitty: true})
	start.kitty.out = &out
	var m tea.Model = start

	steps := []struct {
		name string
		msg  tea.Msg
		view string // text that the view must show
		sent string // text that the commands must have. Empty means no command.
	}{
		{"size", tea.WindowSizeMsg{Width: w, Height: h}, "Inbox (0)", ""},
		// The 16 pixel image is 2 columns and 1 row. It is a link.
		{"reader", messageMsg{doc: doc}, "\x1b]8;;https://example.com/go\x1b\\\x1b[38;2;0;0;1m" + placeholder + string(rune(0x305)) + placeholder + "\x1b[0m", "a=t,i=1,f=24,s=16,v=16,"},
		{"resize", tea.WindowSizeMsg{Width: 60, Height: h}, placeholder, "\x1b_Ga=p,U=1,i=1,p=1,c=2,r=1,q=2\x1b\\"},
		// The error banner changes the size of the reader, so the reader draws again.
		{"no sixel view", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, "already shows each image", "\x1b_Ga=p,U=1,i=1,p=1,c=2,r=1,q=2\x1b\\"},
		{"inbox", tea.KeyMsg{Type: tea.KeyEsc}, "Inbox (0)", "\x1b_Ga=d,d=I,i=1,q=2\x1b\\"},
	}
	width := w
	for _, s := range steps {
		if size, ok := s.msg.(tea.WindowSizeMsg); ok {
			width = size.Width
		}
		out.Reset()
		m, _ = m.Update(s.msg)
		view := m.View()
		if got := lipgloss.Height(view); got != h {
			t.Errorf("%s: view is %d rows, want %d", s.name, got, h)
		}
		if got := lipgloss.Width(view); got > width {
			t.Errorf("%s: view is %d columns, want at most %d", s.name, got, width)
		}
		if !strings.Contains(view, s.view) {
			t.Errorf("%s: view does not show %q", s.name, s.view)
		}
		if got := out.String(); !strings.Contains(got, s.sent) || (s.sent == "") != (got == "") {
			t.Errorf("%s: got the commands %q, want %q in them", s.name, got, s.sent)
		}
	}
	if strings.Contains(out.String(), "a=t") {
		t.Error("the last step sent an image")
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

	out := renderHTML(d.root, 80, d.images, nil)
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

	// The shift+tab key shows the previous account, and goes from the first
	// account to the last.
	back := tea.KeyMsg{Type: tea.KeyShiftTab}
	m = newModel(terminal{})
	m, _ = m.Update(accountsMsg{accounts: []account{{Name: "a"}, {Name: "b", Default: true}, {Name: "c"}}})
	for _, want := range []string{"a", "c", "b"} {
		m, _ = m.Update(envelopesMsg{})
		m, _ = m.Update(back)
		if got := m.(model).account; got != want {
			t.Errorf("shift+tab: got account %q, want %q", got, want)
		}
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

// A move down from the last envelope lists the next page, and that page
// extends the inbox.
func TestNextPage(t *testing.T) {
	list := func(page int, ids ...string) envelopesMsg {
		msg := envelopesMsg{page: page}
		for _, id := range ids {
			msg.envelopes = append(msg.envelopes, envelope{ID: id})
		}
		return msg
	}
	down := tea.KeyMsg{Type: tea.KeyDown}

	steps := []struct {
		name string
		msg  tea.Msg
		want string // the envelope IDs, the cursor, the pages, busy
	}{
		{"first page", list(1, "4", "3"), "[4 3] 0 1 false"},
		{"down", down, "[4 3] 1 1 false"},
		{"down at the last envelope", down, "[4 3] 1 1 true"},
		{"down when busy", down, "[4 3] 1 1 true"},
		// A new message moved envelope 3 to page 2.
		{"second page", list(2, "3", "2"), "[4 3 2] 2 2 false"},
		{"down again", down, "[4 3 2] 2 2 true"},
		{"error", envelopesMsg{page: 3, err: errors.New("himalaya: no connection")}, "[4 3 2] 2 2 false"},
		{"down after the error", down, "[4 3 2] 2 2 true"},
		{"empty page", list(3), "[4 3 2] 2 3 false"},
		{"down after the last page", down, "[4 3 2] 2 3 false"},
		{"refresh", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, "[4 3 2] 2 3 true"},
		{"first page again", list(1, "5", "4"), "[5 4] 1 1 false"},
		{"down after the refresh", down, "[5 4] 1 1 true"},
	}

	var m tea.Model = newModel(terminal{})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, s := range steps {
		m, _ = m.Update(s.msg)
		mm := m.(model)
		var ids []string
		for _, e := range mm.envelopes {
			ids = append(ids, e.ID)
		}
		got := fmt.Sprintf("%v %d %d %v", ids, mm.inbox.Cursor(), mm.cache[mm.account].pages, mm.busy)
		if got != s.want {
			t.Errorf("%s: got %q, want %q", s.name, got, s.want)
		}
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
