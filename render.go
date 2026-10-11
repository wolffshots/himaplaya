package main

import (
	"cmp"
	"image"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// renderHTML draws an HTML document as styled text that fits the given width.
// It draws each image in images in place. Any other image shows its alt text.
//
// ponytail: this styles a fixed set of tags and ignores the CSS of the mail, so
// the colors of the mail do not show. The upgrade path is an external ANSI
// renderer behind this function, for example elinks -dump-color-mode.
func renderHTML(root *html.Node, width int, images map[string]image.Image) string {
	r := &renderer{width: width, images: images}
	r.node(root, lipgloss.NewStyle(), "")
	if r.col > 0 {
		r.newline()
	}
	return strings.Join(r.lines, "\n")
}

type renderer struct {
	width  int
	images map[string]image.Image

	lines   []string        // the finished lines
	cur     strings.Builder // the line in progress
	col     int             // the width of cur. 0 means that cur is empty.
	indent  string          // starts each line: list and quote levels
	space   bool            // white space waits before the next word
	pending int             // line breaks that wait: 1 ends the line, 2 adds a blank line
}

func (r *renderer) node(n *html.Node, st lipgloss.Style, href string) {
	if n.Type == html.TextNode {
		r.text(n.Data, st, href)
		return
	}
	if (n.Type != html.ElementNode && n.Type != html.DocumentNode) || hidden(n) {
		return
	}

	brk := 0
	switch n.DataAtom {
	case atom.Head, atom.Style, atom.Script, atom.Title, atom.Template, atom.Svg:
		return
	case atom.Br:
		r.pending = min(r.pending+1, 2)
		return
	case atom.Hr:
		r.block(1)
		r.flush()
		r.lines = append(r.lines, r.indent+dimStyle.Render(strings.Repeat("─", max(r.width-ansi.StringWidth(r.indent), 0))))
		return
	case atom.Img:
		r.img(n, href)
		return

	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		brk = 2
		st = st.Bold(true).Foreground(accent)
	case atom.P, atom.Blockquote, atom.Ul, atom.Ol, atom.Table, atom.Pre:
		brk = 2
	case atom.Div, atom.Tr, atom.Li, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Center:
		brk = 1
	case atom.Td, atom.Th:
		r.space = true

	case atom.B, atom.Strong:
		st = st.Bold(true)
	case atom.I, atom.Em:
		st = st.Italic(true)
	case atom.U, atom.Ins:
		st = st.Underline(true)
	case atom.S, atom.Strike, atom.Del:
		st = st.Strikethrough(true)
	case atom.A:
		if u := linkTarget(n); u != "" {
			href = u
			st = st.Underline(true).Foreground(accent)
		}
	}

	r.block(brk)
	indent := r.indent
	switch n.DataAtom {
	case atom.Blockquote:
		r.indent += dimStyle.Render("│") + " "
	case atom.Li:
		// The marker uses the indent of the list. The lines after it align
		// with the text.
		r.word(marker(n), dimStyle, "")
		r.space = true
		r.indent += "  "
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.node(c, st, href)
	}
	r.block(brk)
	r.indent = indent
}

// text adds the words of a text node. HTML folds each run of white space into
// one space.
func (r *renderer) text(s string, st lipgloss.Style, href string) {
	s = strings.Map(visible, clean(s))
	words := strings.Fields(s)
	if len(words) == 0 {
		r.space = r.space || s != ""
		return
	}
	r.space = r.space || !strings.HasPrefix(s, words[0])
	for _, w := range words {
		r.word(w, st, href)
		r.space = true
	}
	r.space = !strings.HasSuffix(s, words[len(words)-1])
}

// word adds one word and starts a new line when the word does not fit. It
// styles each word on its own, so each line carries complete escape sequences
// and the viewport can cut the text at any line.
func (r *renderer) word(w string, st lipgloss.Style, href string) {
	r.flush()
	gap := 0 // the space before the word, if the word stays on this line
	if r.space && r.col > 0 {
		gap = 1
	}
	r.space = false

	for w != "" {
		indentW := ansi.StringWidth(r.indent)
		if r.col == 0 {
			r.cur.WriteString(r.indent)
			r.col = indentW
		}
		head := w
		if avail := r.width - r.col - gap; ansi.StringWidth(w) > avail {
			if r.col > indentW {
				r.newline()
				gap = 0
				continue
			}
			// The word is wider than a full line, so cut it.
			if head = ansi.Truncate(w, avail, ""); head == "" {
				head = w
			}
		}
		if gap == 1 {
			r.cur.WriteByte(' ')
			r.col++
			gap = 0
		}
		r.cur.WriteString(hyperlink(href, st.Render(head)))
		r.col += ansi.StringWidth(head)
		if w = w[len(head):]; w != "" {
			r.newline()
		}
	}
}

// img draws a loaded image as half blocks. It shows the alt text of an image
// that is not loaded. Both are a link when the image is in a link.
func (r *renderer) img(n *html.Node, href string) {
	// A size below 8 pixels is a tracker or a spacer.
	w, wErr := strconv.Atoi(attr(n, "width"))
	h, hErr := strconv.Atoi(attr(n, "height"))
	if (wErr == nil && w < 8) || (hErr == nil && h < 8) {
		return
	}

	img := r.images[attr(n, "src")]
	if img == nil {
		alt, st := oneLine(attr(n, "alt")), dimStyle
		if href != "" {
			// An image with no alt text still needs a place to click.
			alt, st = cmp.Or(alt, "image"), st.Underline(true)
		}
		if alt != "" {
			r.word("["+alt+"]", st, href)
		}
		return
	}
	if img.Bounds().Dx() < 8 || img.Bounds().Dy() < 8 {
		return
	}
	if wErr != nil {
		w = img.Bounds().Dx()
	}
	// A cell is about 8 pixels wide.
	cols := min(w/8, r.width-ansi.StringWidth(r.indent))
	if cols < 1 {
		return
	}
	r.block(1)
	r.flush()
	for _, line := range halfBlocks(img, cols) {
		r.lines = append(r.lines, r.indent+hyperlink(href, line))
	}
	r.block(1)
}

// linkTarget returns the href of an <a> element. It returns "" for any other
// element, and for a scheme that is not safe to open.
func linkTarget(n *html.Node) string {
	u := oneLine(attr(n, "href"))
	if n.DataAtom == atom.A && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "mailto:")) {
		return u
	}
	return ""
}

// hyperlink makes s an OSC 8 link. The terminal opens the link on a click with
// its modifier key, which is ctrl in Windows Terminal.
func hyperlink(href, s string) string {
	if href == "" {
		return s
	}
	return "\x1b]8;;" + href + "\x1b\\" + s + "\x1b]8;;\x1b\\"
}

// block asks for at least n line breaks before the next word.
func (r *renderer) block(n int) {
	r.pending = max(r.pending, n)
}

// flush applies the line breaks that wait. It folds a run of blank lines into
// one, and it adds no blank line at the top.
func (r *renderer) flush() {
	if r.pending == 0 {
		return
	}
	if r.col > 0 {
		r.newline()
	}
	if r.pending > 1 && len(r.lines) > 0 && r.lines[len(r.lines)-1] != "" {
		r.lines = append(r.lines, "")
	}
	r.pending = 0
	r.space = false
}

func (r *renderer) newline() {
	r.lines = append(r.lines, r.cur.String())
	r.cur.Reset()
	r.col = 0
}

// marker returns the number of an item in an ordered list, or a bullet.
func marker(li *html.Node) string {
	if li.Parent == nil || li.Parent.DataAtom != atom.Ol {
		return "•"
	}
	i := 1
	for s := li.PrevSibling; s != nil; s = s.PrevSibling {
		if s.DataAtom == atom.Li {
			i++
		}
	}
	return strconv.Itoa(i) + "."
}

// hidden reports whether the mail hides the element. A preheader is the usual
// case.
func hidden(n *html.Node) bool {
	style := strings.ToLower(strings.ReplaceAll(attr(n, "style"), " ", ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

// visible drops the characters that take no column. Mail uses them to pad a
// preheader, and they make lines that look blank.
func visible(r rune) rune {
	switch r {
	case 0x00ad, 0x034f, 0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x2060, 0xfeff:
		return -1
	}
	return r
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
