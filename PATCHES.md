# Changes from upstream

This is the Asciiverse packaging of [ASSHTEROIDS](https://github.com/tomz197/asshteroids)
by Tomas Zifcak: multiplayer Asteroids played over SSH, everyone in one
shared field (Go, January to March 2026). Upstream is the `upstream`
remote; the `asciiverse` branch carries the changes below.

Built from upstream `main` at `906a54b` (2026-03-16, "chore: updated
website"), its newest commit; there are no tags or releases.

## Licence

MIT (LICENSE, "Copyright (c) 2026 Tomas Zifcak"). The Go modules built into
the binary, checked module by module 2026-11-16, are MIT (the charmbracelet
modules, creack/pty, mattn, muesli, rivo/uniseg, xo/terminfo and others),
BSD-3-Clause (charmbracelet/ssh from Glider Labs) or BSD-style
(golang.org/x/crypto, sync, sys and term).

## Our changes

One commit, "Play here, with computer pilots":

- **One program that plays and hosts.** Upstream has a local single-player
  binary (`cmd/game`) and a separate SSH server (`cmd/ssh`). The new
  `cmd/asshteroids` plays in the terminal it runs in, and while it runs the
  field is open to the local network on port 2222 or the next free one up
  to 2231; the start screen shows the line friends use
  (`ssh -p 2222 <address>`). If no port is free it plays on this machine
  only. Options: `-offline` (not open to the network), `-server` (only
  serve, upstream's `cmd/ssh`; `SSH_PORT` still works), `-join
  host[:port]` (runs `ssh` to a friend's game), `-port N`, `-bots N`,
  `-name NAME` (default the login name), `-demo`, `-version`. Upstream's
  `cmd/game`, `cmd/ssh` and `cmd/web` are left as they were.
- **Computer pilots** (`internal/loop/server/bots.go`). Three by default
  (`-bots N`, up to eight; none with `-server` unless asked), named after
  asteroids: Vesta, Pallas, Juno, Hygiea, Eros, Ida, Gaspra, Psyche. They
  fly by the same keys a person holds (rotate, thrust, fire): they shoot
  the rock most likely to hit them, lead their shots, back away from rocks
  too close, now and then go after a ship within reach, and roam towards
  the other ships when nothing is near. Each has its own aim and temper,
  three lives like a person, and a score on the board. Each pilot adds 60
  to the rocks the field keeps (counted as upstream counts them, a large
  rock as 4), so they do not strip the field bare.
- **`-demo`**: the player's own ship on the same autopilot, drawn at 30
  frames a second; any key ends it. The catalog preview is recorded with
  it.
- **One lock between the world and its screens.** The snapshot each screen
  draws from holds the live objects, so screens read ships, rocks and
  particles while the server tick moved them, and the server copied the
  object list while a screen was reading it (the race detector reported
  both, in upstream as it is). A tick now holds the world under a lock and
  screens draw under a shared one, sending to the terminal only after
  letting go, so a slow SSH player never holds up the game. Race detector
  clean over a game with the host, an ssh player and eight computer
  pilots.
- **The player at this machine is never sent away for idling** (upstream
  disconnects after two minutes without a key, which ended a local game).
- **Best score kept** in the state folder (`best.json`) and shown on the
  start screen. The state folder is `$XDG_STATE_HOME/asshteroids`
  (`~/.local/state/asshteroids`), `~/Library/Application Support/asshteroids`
  on macOS, `%LOCALAPPDATA%\asshteroids` on Windows. The host key for the
  SSH side is made there the first time (upstream: `/app/keys/host_key`).
- **Session logging.** With `ASSHTEROIDS_LOG=file`, every key as it arrives
  (before anything acts on it), joins and leaves, screen changes (start,
  playing, dead), chat opening and closing, ships destroyed and by whom,
  and twice a second every ship in the field (position, speed, heading,
  keys held, score). With `-server` the log goes to stderr as upstream's
  did.
- **Narrow terminals**: the start screen's Top Scores moves left to stay
  inside 80 columns (it ran off the edge and wrapped onto the next lines).
- **Closing message**: when the host quits, ssh players read "The host has
  closed this game." instead of upstream's "The server is restarting for
  maintenance. Please reconnect in a moment."
- **Windows consoles** are put into VT mode, so the escape codes and half
  blocks draw.
- No profiling server: upstream's `cmd/ssh` starts `net/http/pprof` on
  port 6060; the program we ship does not include it.

## Packaging

- `.github/workflows/release.yml`: av archives `asshteroids-<target>` for
  Linux (x86-64, ARM64; static, CGO off), macOS (x86-64, ARM64) and
  Windows (x86-64, ARM64), cross-built on one Linux runner, with the
  binary, LICENSE, README.md and this file, plus SHA256SUMS. Runs on our
  tags only (`*-av.*`).
- `asciiverse/record-preview.sh` records the catalog preview,
  `asciiverse/asshteroids.cast`: 100x30, `asshteroids -demo -bots 6`, 32
  seconds, under 2 MB.

av starts it as `asshteroids` with no arguments, at the start screen.

## Checked

Linux (Go 1.26.8), in tmux: the start screen at 80x24 and 100x30, a game
against three computer pilots, a game hosted with an OpenSSH player and an
`-join` player in the same field, the closing message on the joined
screen when the host quit, the best score saved and shown again,
`-offline` making no host key, `-server` with two computer pilots and
a joined player (stopped with Ctrl+C, the player shown the closing screen), `-demo` ended by a key, and the race
detector over a hosted game with eight computer pilots. macOS and Windows:
cross-built for both architectures, not run.

## Ours

- **How to play it, at the top of README.md.** A short note on playing it
  with av and on this edition's local play; the original text follows
  unchanged.
