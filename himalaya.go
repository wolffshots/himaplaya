package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os/exec"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type address struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type envelope struct {
	ID      string    `json:"id"`
	Subject string    `json:"subject"`
	From    []address `json:"from"`
	Date    string    `json:"date"`
}

// sender returns the display name of the first sender, or the address when
// the sender has no name.
func (e envelope) sender() string {
	if len(e.From) == 0 {
		return ""
	}
	if e.From[0].Name != "" {
		return e.From[0].Name
	}
	return e.From[0].Email
}

// message holds the fields of `himalaya --json message read` that the reader uses.
type message struct {
	HTMLBody []int  `json:"html_body"`
	TextBody []int  `json:"text_body"`
	Parts    []part `json:"parts"`
}

type part struct {
	Headers []struct {
		// A known header has a string name. Any other header has an object.
		Name  any             `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"headers"`
	Body partBody `json:"body"`
}

// partBody holds one variant. himalaya writes a binary part as an array of
// byte values.
type partBody struct {
	HTML         string `json:"Html"`
	Text         string `json:"Text"`
	Binary       []byte `json:"Binary"`
	InlineBinary []byte `json:"InlineBinary"`
}

// first returns the body of the part that idx names first.
func (m message) first(idx []int) partBody {
	if len(idx) == 0 || idx[0] < 0 || idx[0] >= len(m.Parts) {
		return partBody{}
	}
	return m.Parts[idx[0]].Body
}

// html returns the HTML part. himalaya points html_body at the plain-text part
// when the message has no HTML part, so the result can be empty.
func (m message) html() string { return m.first(m.HTMLBody).HTML }

// text returns the plain-text part. The result can be empty for the same reason.
func (m message) text() string { return m.first(m.TextBody).Text }

// inline returns the bytes of the part that has the given Content-ID.
func (m message) inline(cid string) []byte {
	for _, p := range m.Parts {
		for _, h := range p.Headers {
			var v struct{ Text string }
			if h.Name != "content_id" || json.Unmarshal(h.Value, &v) != nil || strings.Trim(v.Text, "<>") != cid {
				continue
			}
			if len(p.Body.InlineBinary) > 0 {
				return p.Body.InlineBinary
			}
			return p.Body.Binary
		}
	}
	return nil
}

// document is an open message, ready to render at any width.
type document struct {
	envelope envelope
	text     string                 // the plain-text part
	root     *html.Node             // the HTML part. nil when the message has none.
	figures  []figure               // each image once, in document order
	images   map[string]image.Image // decoded images, by src
	remote   []string               // http and https image sources that are not loaded
}

// figure is an image in the HTML part.
type figure struct {
	src  string
	href string // the target of the link around the image, or ""
}

// pictures returns the figures that the sixel view can show. These are the
// loaded images that the reader also draws.
func (d *document) pictures() []figure {
	var out []figure
	for _, f := range d.figures {
		if img := d.images[f.src]; img != nil && img.Bounds().Dx() >= 8 && img.Bounds().Dy() >= 8 {
			out = append(out, f)
		}
	}
	return out
}

// account is an entry of `himalaya --json account list`.
type account struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// himalaya runs himalaya with JSON output and decodes stdout into out. An empty
// account is the default account of the himalaya configuration.
func himalaya(ctx context.Context, out any, account string, args ...string) error {
	args = append([]string{"--json"}, args...)
	if account != "" {
		args = append(args, "--account", account)
	}
	stdout, err := exec.CommandContext(ctx, "himalaya", args...).Output()
	if err != nil {
		// With --json, himalaya reports a failure as {"error": "..."}.
		var fail struct {
			Error string `json:"error"`
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			for _, b := range [][]byte{stdout, exit.Stderr} {
				if json.Unmarshal(b, &fail) == nil && fail.Error != "" {
					return errors.New("himalaya: " + fail.Error)
				}
			}
		}
		return fmt.Errorf("himalaya: %w", err)
	}
	return json.Unmarshal(stdout, out)
}

func listAccounts(ctx context.Context) ([]account, error) {
	var out struct {
		Accounts []account `json:"accounts"`
	}
	err := himalaya(ctx, &out, "", "account", "list")
	return out.Accounts, err
}

// listEnvelopes lists one page of the inbox. Page 1 holds the most recent
// envelopes. A page after the last page is empty.
func listEnvelopes(ctx context.Context, account string, page int) ([]envelope, error) {
	var out struct {
		Envelopes []envelope `json:"envelopes"`
	}
	err := himalaya(ctx, &out, account, "envelope", "list", "--page", strconv.Itoa(page))
	return out.Envelopes, err
}

// readMessage reads a message and prepares it for the reader. The read does not
// mark the message as seen.
func readMessage(ctx context.Context, account string, e envelope) (*document, error) {
	var m message
	if err := himalaya(ctx, &m, account, "message", "read", e.ID); err != nil {
		return nil, err
	}
	return newDocument(e, m)
}

// newDocument parses the HTML part and decodes the images that the message
// carries. It only lists the remote images. The reader fetches them on request,
// because a fetch tells the sender that the user opened the message.
func newDocument(e envelope, m message) (*document, error) {
	d := &document{envelope: e, text: clean(m.text()), images: map[string]image.Image{}}
	if m.html() == "" {
		return d, nil
	}
	root, err := html.Parse(strings.NewReader(m.html()))
	if err != nil {
		return nil, err
	}
	d.root = root

	seen := map[string]bool{}
	for n := range root.Descendants() {
		src := attr(n, "src")
		if n.DataAtom != atom.Img || src == "" || seen[src] {
			continue
		}
		seen[src] = true
		f := figure{src: src}
		for p := n.Parent; p != nil && f.href == ""; p = p.Parent {
			f.href = linkTarget(p)
		}
		d.figures = append(d.figures, f)
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			d.remote = append(d.remote, src)
		} else if img, err := decodeImage(localImage(m, src)); err == nil {
			d.images[src] = img
		}
	}
	return d, nil
}

// clean drops control characters, so that mail text cannot send an escape
// sequence to the terminal. It keeps the newline and turns a tab into a space.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// oneLine cleans s and folds its white space, for a table cell or a title.
func oneLine(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}
