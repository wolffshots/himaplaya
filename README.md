# himaplaya

himaplaya is a Bubble Tea TUI for the [himalaya](https://github.com/pimalaya/himalaya) mail client. It lists the inbox envelopes and opens a selected message as text. This is a proof of concept.

## Requirements

- Go 1.26
- `himalaya` v2 on the `PATH`, with a configured account
- `lynx` on the `PATH`, to render an HTML message

## Run

```
go run .
```

## Keys

| Key | Action |
|---|---|
| `up`, `down`, `j`, `k` | Move in the inbox, or scroll the message |
| `enter` | Open the selected message |
| `esc` | Go back to the inbox |
| `r` | Refresh the inbox |
| `?` | Show or hide the full help |
| `q` | Quit |

## How it works

The inbox comes from `himalaya --json envelope list`. himaplaya shows the first page, at the page size in the himalaya configuration.

A message comes from `himalaya --json message read <id>`. If the message has an HTML part, `lynx -dump` renders it as text. If the message has no HTML part, himaplaya shows the plain text. This is the same pipeline as the `hread` shell function. The read does not mark the message as seen.

## Test

```
go test ./...
```
