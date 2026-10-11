# himaplaya

himaplaya is a Bubble Tea TUI for the [himalaya](https://github.com/pimalaya/himalaya) mail client. It lists the inbox envelopes and opens a selected message as styled text with images. This is a proof of concept.

## Requirements

- Go 1.26
- `himalaya` v2 on the `PATH`, with a configured account
- A terminal with 24-bit color, to show images

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
| `tab` | Show the inbox of the next account |
| `t` | Switch between the HTML part and the plain-text part |
| `i` | Load the remote images of the open message |
| `s` | Open or close the sixel view of the open message |
| `left`, `right`, `h`, `l` | Show the previous or the next image in the sixel view |
| `?` | Show or hide the full help |
| `q` | Quit |

## How it works

The inbox comes from `himalaya --json envelope list`. himaplaya shows the first page, at the page size in the himalaya configuration.

A message comes from `himalaya --json message read <id>`. The read does not mark the message as seen.

himaplaya renders the HTML part itself. It styles headings, bold, italic, underline, links, lists and quotes. It ignores the CSS of the mail. If the message has no HTML part, himaplaya shows the plain text.

The `t` key shows the plain-text part. If the message has no plain-text part, himaplaya shows the HTML part without styles and images. The choice stays for the next message.

## Accounts

At startup, himaplaya reads the accounts from `himalaya --json account list`. The tab bar shows one tab for each account. The first inbox is the inbox of the default account.

The `tab` key shows the inbox of the next account. It closes the open message. If the himalaya configuration has one account, the `tab` key does nothing.

himaplaya passes the account to each himalaya command with the `--account` option. It shows one account at a time.

### Add an account

himalaya keeps each account as an `[accounts.<name>]` table in its configuration file. It reads the first file that it finds in this list:

- `$XDG_CONFIG_HOME/himalaya/config.toml`
- `$HOME/.config/himalaya/config.toml`
- `$HOME/.himalayarc`

1. Run `himalaya configure`. The wizard asks for an account name and an email address, then prints the account as TOML.
2. Copy the printed `[accounts.<name>]` table to the end of the configuration file.
3. Keep `default = true` in the table of one account only. himaplaya opens that account first.
4. Run `himalaya account check --account <name>`. The command connects to each backend of the account.
5. Run `himalaya account list`. Make sure that the list shows the new account.
6. Start himaplaya again. It reads the accounts only at startup.

You can also write the table by hand. This example adds an account with the name `work`:

```toml
[accounts.work]
email = "me@example.com"

imap.server = "imaps://imap.example.com:993"
imap.sasl.plain.username = "me@example.com"
imap.sasl.plain.password.command = "pass show work"

smtp.server = "smtps://smtp.example.com:465"
smtp.sasl.plain.username = "me@example.com"
smtp.sasl.plain.password.command = "pass show work"
```

The `password.command` value is a command that prints the password. The himalaya [sample configuration](https://github.com/pimalaya/himalaya/blob/master/config.sample.toml) describes each option.

## Images

himaplaya draws an image with half-block characters. Each character cell shows 2 pixels, so text in an image is not readable. It decodes PNG, JPEG and GIF images.

himaplaya shows an image that the message carries immediately. This is an inline `cid:` part or a `data:` URI.

himaplaya does not load a remote image until you press `i`. A fetch tells the sender that you opened the message. Until then, a remote image shows its alt text.

## Sixel view

The `s` key shows one loaded image of the open message at the full resolution. The view needs a terminal with sixel support, such as Windows Terminal 1.22 or later. An image keeps its size, unless it must shrink to fit the window. The caption shows the size of the image and the link around it.

At startup, himaplaya asks the terminal for the cell size and for sixel support. If the terminal does not report sixel support, the `s` key shows an error. If the terminal does not report a cell size, himaplaya uses 10 by 20 pixels.

## Links

A link is an OSC 8 link, so the terminal opens it on a click. Windows Terminal opens a link when you hold `ctrl` and click it. This applies to link text, to an image in a link, to the alt text of an image that is not loaded, and to the link in the caption of the sixel view. himaplaya only makes a link for an `http`, `https` or `mailto` target.

## Test

```
go test ./...
```
