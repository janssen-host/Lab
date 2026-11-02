# Lab Controller TUI

Run the TUI from this directory with Go 1.24 or later:

```sh
go run .
```

The default API address is `http://10.10.10.2:8080`. To use another controller:

```sh
go run . -api http://controller.example:8080
```

The first stack and its first container are selected automatically. Container tabs show live logs. Database tabs show connection details, the backup schedule, and recorded backup history. Switching selections closes the previous WebSocket. The display keeps at most 5,000 log lines in memory; the API supplies up to 200 historical lines on each connection.

## Controls

| Control | Action |
| --- | --- |
| Up / Down | Select a stack. |
| `s` | Search stacks by name; use up/down and Enter to open a result, or Esc to cancel. |
| Left / Right | Move between the selected stack's container and database tabs. |
| Page Up / Page Down | Scroll logs vertically by a page. |
| Vertical trackpad scroll | Scroll logs vertically. |
| Horizontal trackpad scroll, Shift + vertical scroll, or `h` / `l` | Scroll across long log lines. |
| `c` | Copy the selected database's connection string to the clipboard. |
| `b` | Start an immediate backup of the selected database. |
| `S` (Shift+S) | Select a database backup; use up/down and Enter to restore, or Esc to exit. Restoring requires two confirmations. |
| `U` (Shift+U) | Pull images and update the selected stack. |
| `R` (Shift+R) | Restart the selected container. |
| `r` | Refresh and reconnect. |
| `T` (Shift+T) | Trust a new SSH host key for the selected stack's node. |
| `m` | Toggle text selection mode and mouse capture. |
| `q` / Ctrl+C | Quit. |

Trusting a new host key replaces the previous key with the one presented by a successfully authenticated connection and displays its fingerprint. Verify the fingerprint independently, because this bypasses the previous key check. Press `r` to reconnect after trusting the key.

Run the tests with `go test ./...`. The TUI uses the API's existing stack and WebSocket endpoints and does not connect to workload hosts directly.

Trackpad scrolling requires a terminal that reports mouse wheel events. Shift plus vertical scrolling can also move horizontally in terminals that report the Shift modifier.

Press `m` to show only log lines across the full terminal and turn off mouse capture, then select and copy text using your terminal. Press `m` again to restore mouse scrolling. Keyboard scrolling remains available in selection mode.

Log colours are defined in `log-colours.json`: named colours map to terminal
colour values, and ordered regex patterns reference those names. The first
matching pattern colours the whole line. The default file is embedded when
the binary is built. Use `-log-colours /path/to/file.json` to load a custom
file at startup without rebuilding. Selection mode stays uncoloured.
