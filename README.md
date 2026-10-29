# himaplaya

himaplaya is a Bubble Tea TUI for the [himalaya](https://github.com/pimalaya/himalaya) mail client. It lists the inbox envelopes and opens a selected message as styled text with images. This is a proof of concept.

## Requirements

- `himalaya` v2 on the `PATH`, with a configured account
- A terminal with 24-bit color, to show images
- Go 1.26.10 or later, to build from source

## Install

### Homebrew (macOS / Linux)

```sh
brew install wolffshots/tap/himaplaya
```

The formula builds from source through [wolffshots/homebrew-tap](https://github.com/wolffshots/homebrew-tap). It also installs `himalaya`.

### Scoop (Windows)

```powershell
scoop bucket add wolffshots https://github.com/wolffshots/scoop-bucket
scoop install wolffshots/himaplaya
```

The manifest installs the release binary through [wolffshots/scoop-bucket](https://github.com/wolffshots/scoop-bucket). It also installs `himalaya`.

### From source

```
go install github.com/wolffshots/himaplaya@latest
```

## Run

```
himaplaya
```

`himaplaya --version` prints the version.

## Keys

| Key | Action |
|---|---|
| `up`, `down`, `j`, `k` | Move in the inbox, or scroll the message |
| `enter` | Open the selected message |
| `esc` | Go back to the inbox |
| `r` | Refresh the inbox |
| `R` | Turn the auto refresh on or off |
| `tab` | Show the inbox of the next account |
| `shift+tab` | Show the inbox of the previous account |
| `t` | Switch between the HTML part and the plain-text part |
| `i` | Load the remote images of the open message |
| `a` | Save the attachments of the open message |
| `s` | Open or close the sixel view of the open message |
| `left`, `right`, `h`, `l` | Show the previous or the next image in the sixel view |
| `?` | Show or hide the full help |
| `q` | Quit |

## How it works

The inbox comes from `himalaya --json envelope list --page <n>`. himaplaya shows the first page at startup, at the page size in the himalaya configuration.

When the inbox shows its last envelope, himaplaya reads the next page and adds it to the inbox. This occurs when you select the last envelope, or when all the envelopes fit in the window. You can open a message while himaplaya reads the page. The `r` key reads the first page again and drops the other pages.

A message comes from `himalaya --json message read <id>`. The read does not mark the message as seen.

himaplaya renders the HTML part itself. It styles headings, bold, italic, underline, links, lists and quotes. It ignores the CSS of the mail. If the message has no HTML part, himaplaya shows the plain text.

The `t` key shows the plain-text part. If the message has no plain-text part, himaplaya shows the HTML part without styles and images. The choice stays for the next message.

## Auto refresh

The `R` key turns the auto refresh on or off. It is off at startup. When it is on, the tab bar shows `auto refresh`.

Each minute, himaplaya reads the first page of the inbox that the window shows. It does not read the inbox of another account. A new message goes to the top of the inbox. The other pages stay, and the selection stays on the same envelope.

## Attachments

The `a` key saves the attachments of the open message. himaplaya runs `himalaya attachment download <id>` for this. himalaya writes the files to the `downloads-dir` of its configuration, and it does not overwrite a file. himaplaya then shows the names of the files and the directory.

himalaya also saves the inline parts of the message, such as the image of a signature.

## Accounts

At startup, himaplaya reads the accounts from `himalaya --json account list`. The tab bar shows one tab for each account. The first inbox is the inbox of the default account.

The `tab` key shows the inbox of the next account, and the `shift+tab` key shows the inbox of the previous account. Each key closes the open message. If the himalaya configuration has one account, the keys do nothing.

himaplaya keeps the last inbox of each account in memory for 5 minutes after it reads the first page. In that time, an account switch shows the kept inbox and does not call himalaya. The `r` key always reads the inbox again.

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

In kitty and Ghostty, himaplaya draws an image with the Kitty graphics protocol. The image shows at the full resolution, and it scrolls with the message. An image uses 297 rows at most, so a taller image shrinks. In tmux, himaplaya draws half-block characters.

himaplaya shows an image that the message carries immediately. This is an inline `cid:` part or a `data:` URI.

himaplaya does not load a remote image until you press `i`. A fetch tells the sender that you opened the message. Until then, a remote image shows its alt text.

## Sixel view

The `s` key shows one loaded image of the open message at the full resolution. The view needs a terminal with sixel support, such as Windows Terminal 1.22 or later. An image keeps its size, unless it must shrink to fit the window. The caption shows the size of the image and the link around it.

kitty and Ghostty have no sixel view. The reader already shows each image at the full resolution, and the `s` key says so.

At startup, himaplaya asks the terminal for its name, for the cell size and for sixel support. If the terminal does not report sixel support, the `s` key shows an error. If the terminal does not report a cell size, himaplaya uses 10 by 20 pixels.

## Links

A link is an OSC 8 link, so the terminal opens it on a click. Windows Terminal opens a link when you hold `ctrl` and click it. This applies to link text, to an image in a link, to the alt text of an image that is not loaded, and to the link in the caption of the sixel view. himaplaya only makes a link for an `http`, `https` or `mailto` target.

## Test

```
go test ./...
```
