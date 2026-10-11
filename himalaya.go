package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
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
	HTMLBody []int `json:"html_body"`
	Parts    []struct {
		Body partBody `json:"body"`
	} `json:"parts"`
}

type partBody struct {
	HTML string `json:"Html"`
	Text string `json:"Text"`
}

// body returns the part that html_body names first. himalaya points html_body
// at the plain-text part when the message has no HTML part.
func (m message) body() partBody {
	if len(m.HTMLBody) == 0 || m.HTMLBody[0] < 0 || m.HTMLBody[0] >= len(m.Parts) {
		return partBody{}
	}
	return m.Parts[m.HTMLBody[0]].Body
}

// himalaya runs himalaya with JSON output and decodes stdout into out.
func himalaya(ctx context.Context, out any, args ...string) error {
	stdout, err := exec.CommandContext(ctx, "himalaya", append([]string{"--json"}, args...)...).Output()
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

func listEnvelopes(ctx context.Context) ([]envelope, error) {
	var out struct {
		Envelopes []envelope `json:"envelopes"`
	}
	err := himalaya(ctx, &out, "envelope", "list")
	return out.Envelopes, err
}

// readMessage returns the message as text, the way the hread shell function
// does. lynx renders the HTML part. A message with no HTML part gives its
// plain text. The read does not mark the message as seen.
func readMessage(ctx context.Context, id string, width int) (string, error) {
	var m message
	if err := himalaya(ctx, &m, "message", "read", id); err != nil {
		return "", err
	}
	body := m.body()
	if body.HTML == "" {
		return clean(body.Text), nil
	}
	cmd := exec.CommandContext(ctx, "lynx", "-dump", "-stdin", "-force_html",
		"-assume_charset=utf-8", "-display_charset=utf-8", "-width="+strconv.Itoa(width))
	cmd.Stdin = strings.NewReader(body.HTML)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("lynx: %w", err)
	}
	return clean(string(out)), nil
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
