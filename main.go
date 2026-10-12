// Command himaplaya is a TUI for the himalaya mail client. It lists the inbox
// envelopes and opens a selected message as text.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// version is overridden at release build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("himaplaya", version)
		return
	}
	if _, err := tea.NewProgram(newModel(queryTerminal()), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
