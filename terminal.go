package main

import (
	"image"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
)

// terminal holds what the sixel view must know about the terminal.
type terminal struct {
	cell  image.Point // the size of one character cell, in pixels
	sixel bool        // the terminal reports sixel support
}

var (
	cellSizeRe = regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`)
	attrsRe    = regexp.MustCompile(`\x1b\[\?([\d;]*)c`)
)

// queryTerminal asks the terminal for its cell size and its device attributes.
// Call it before Bubble Tea starts, because Bubble Tea reads a reply as key
// presses.
func queryTerminal() terminal {
	// 10 by 20 pixels is a common cell size. It applies when the terminal
	// does not report one.
	t := terminal{cell: image.Pt(10, 20)}
	in := os.Stdin.Fd()
	if !term.IsTerminal(in) || !term.IsTerminal(os.Stdout.Fd()) {
		return t
	}
	state, err := term.MakeRaw(in)
	if err != nil {
		return t
	}
	defer term.Restore(in, state)
	if restore, err := termenv.EnableVirtualTerminalProcessing(termenv.DefaultOutput()); err == nil {
		defer restore()
	}

	// CSI 16 t asks for the cell size. CSI c asks for the device attributes.
	// Every terminal answers the second request, so the read stops there.
	os.Stdout.WriteString("\x1b[16t\x1b[c")

	reply := make(chan []byte, 1)
	var stop atomic.Bool
	go func() {
		var buf []byte
		b := make([]byte, 256)
		for !stop.Load() && !attrsRe.Match(buf) {
			n, err := os.Stdin.Read(b)
			if err != nil {
				break
			}
			buf = append(buf, b[:n]...)
		}
		reply <- buf
	}()
	select {
	case buf := <-reply:
		return parseTerminal(t, buf)
	case <-time.After(time.Second):
		// ponytail: a read cannot be stopped, so this goroutine takes the
		// first key press in a terminal that does not answer. The upgrade
		// path is a read with a deadline on each platform.
		stop.Store(true)
		return t
	}
}

// parseTerminal reads the replies to the requests of queryTerminal. Device
// attribute 4 means sixel support.
func parseTerminal(t terminal, reply []byte) terminal {
	if m := cellSizeRe.FindSubmatch(reply); m != nil {
		h, _ := strconv.Atoi(string(m[1]))
		w, _ := strconv.Atoi(string(m[2]))
		if w > 0 && h > 0 {
			t.cell = image.Pt(w, h)
		}
	}
	if m := attrsRe.FindSubmatch(reply); m != nil {
		t.sixel = slices.Contains(strings.Split(string(m[1]), ";"), "4")
	}
	return t
}
