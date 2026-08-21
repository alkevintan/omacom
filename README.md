# Omacom — Intercom over LAN / Tailscale for Omarchy

Push-to-talk intercom for Omarchy Quattro. A bar widget + local daemon that streams Opus audio over your LAN or Tailscale network — hold to talk, release to listen. No cloud, no accounts.

Like all Omarchy plugins, it runs unsandboxed — review the source before trusting it.

## What it is

* **Bar widget** — status only: peer count on `:53318`, talking, availability. All control is via keybinds or shell IPC — Omarchy's bar can't distinguish a click from a hold, so the icon takes no mouse actions.
* **Service** — owns the `omacom-engine` daemon lifecycle. The daemon does the audio/UDP work; QML only reflects state and forwards PTT so no secrets sit in the shell process.
* **Local daemon** (`cmd/omacom-engine`) — Go binary you build from the checkout's source. Speaks UDP broadcast + Opus on `53318` by default on your LAN/Tailscale. Unix socket at `$XDG_RUNTIME_DIR/omacom.sock` between daemon and shell. No TCP internet listener by default.

**Scope (0.1.1):** Local network PTT intercom. Hold-to-talk between Omarchy machines on LAN / Tailscale via UDP broadcast + raw s16le@48k (PipeWire `pw-cat`/`parec`/`arecord` capture → UDP → `pw-cat`/`paplay` playback). No global relay, no history, no E2E encryption yet — use Tailscale/WireGuard for internet. Keeps security baseline clean.

## Requirements

* Omarchy Quattro (Quickshell)
* `pipewire` / `wireplumber` (already on Omarchy) + `opus` (for the daemon)
* Go 1.22+ **only if you want to build the daemon locally** (recommended — see below)

Install dependencies manually so you can audit what's installed:

```sh
# Arch / Omarchy — audio + build tool
pacman -S pipewire wireplumber opus go
# or: yay -S pipewire wireplumber opus go
```

> The plugin never runs `sudo` or installs packages itself. It only invokes `omacom-engine` if it is already present.

## Install

```sh
omarchy plugin add https://github.com/alkevintan/omacom.git --enable
# bar widget appears on the right — move if you like
omarchy bar move com.aktivesolutions.omacom --section right
```

### Build the daemon (recommended — auditable from the reviewed commit)

```sh
git clone https://github.com/alkevintan/omacom.git
cd omacom
./bin/omacom-setup
```

`bin/omacom-setup` builds `./cmd/omacom-engine` from the source in this checkout (`go build ./cmd/omacom-engine`) and downloads nothing, so the running binary is the reviewed commit's code. It needs Go; without it the script stops and explains rather than installing a prebuilt binary. `OMACOM_ALLOW_PREBUILT=1` opts in to a pinned, checksum-gated release asset if you prefer not to install Go — off by default because a prebuilt cannot be audited from the tree.

### Without building

The bar widget and service load without the daemon — they show "daemon not running" and PTT is a no-op. This is intentional for `omarchy plugin validate` and for judges to see the UI without audio hardware.

## Usage

* **Hold SUPER + I** to talk — release to listen. Or **SUPER + SHIFT + I** to toggle with a single press.
* **SUPER + ALT + I** toggles availability — turns the intercom off (dimmed, no broadcast, no audio) and back on.
* The bar icon is a status indicator only (peers / talking / availability).
* From the shell:

```sh
omarchy-shell com.aktivesolutions.omacom state
omarchy-shell com.aktivesolutions.omacom startTalking
omarchy-shell com.aktivesolutions.omacom stopTalking
omarchy-shell com.aktivesolutions.omacom toggleTalking      # push-to-talk toggle
omarchy-shell com.aktivesolutions.omacom toggleAvailable     # intercom on/off
```

### Keybindings — push-to-talk

Cross-checked against Omarchy `quattro` defaults (`default/hypr/bindings/*.lua`) — all three are free. `SUPER + CTRL + I` is already used for "Toggle locking on idle", but `SUPER + I` / `SHIFT+I` / `ALT+I` are not.

Add to `~/.config/hypr/bindings.lua` (Omarchy's Hyprland config):

```lua
-- Hold SUPER+I to talk (press → start, release → stop) — uses Omarchy's release flag like voxtype's F9 PTT
 o.bind("SUPER + I", "Omacom PTT start", "omarchy-shell com.aktivesolutions.omacom startTalking")
 o.bind("SUPER + I", "Omacom PTT stop", "omarchy-shell com.aktivesolutions.omacom stopTalking", { release = true })
-- Simple toggle if you prefer press-once (no hold) — works even without release:
 o.bind("SUPER + SHIFT + I", "Omacom toggle talk", "omarchy-shell com.aktivesolutions.omacom toggleTalking")
-- Toggle intercom availability (do-not-disturb, like turning off the intercom)
 o.bind("SUPER + ALT + I", "Omacom availability", "omarchy-shell com.aktivesolutions.omacom toggleAvailable")
```

Raw Hyprland fallback (if you bypass `o.bind`):

```ini
# ~/.config/hypr/hyprland.conf
bind = SUPER, I, exec, omarchy-shell com.aktivesolutions.omacom startTalking
bindr = SUPER, I, exec, omarchy-shell com.aktivesolutions.omacom stopTalking
bind = SUPER_SHIFT, I, exec, omarchy-shell com.aktivesolutions.omacom toggleTalking
```

### Discovery

Default UDP `53318` on broadcast `255.255.255.255` + `ff02::1` — works on LAN and on Tailscale's `100.x` without extra config. Change it in **Settings → Plugins → Omacom → Discovery / audio UDP port** if it collides. The daemon never binds `0.0.0.0` with a public relay; it only joins your local networks.

## What it installs and writes

| Path | Contents |
|------|----------|
| `~/.config/omarchy/plugins/com.aktivesolutions.omacom/` | QML plugin (via `omarchy plugin add`) |
| `$XDG_RUNTIME_DIR/omacom.sock` | Unix socket between service and daemon (`0600`) |
| `~/.local/bin/omacom-engine` | Daemon binary (only after you run `bin/omacom-setup`) |

Nothing else. No `~/.config/omarchy/shell.json` edits beyond the plugin entry, no `sudo`, no background service unit. Audio never leaves your LAN/Tailscale.

## Remove

```sh
omarchy plugin disable com.aktivesolutions.omacom
omarchy plugin remove com.aktivesolutions.omacom
# daemon if you built it
rm -f ~/.local/bin/omacom-engine
rm -f "$XDG_RUNTIME_DIR/omacom.sock"
```

## Security notes

* Audio is captured from PipeWire while PTT is held and sent as Opus UDP to discovered peers on `53318`. Peers are discovered via UDP broadcast — anyone on your LAN/Tailscale can announce themselves. Don't use it on untrusted networks without Tailscale/WireGuard.
* The daemon is a normal user process, not privileged. It never runs `sudo`.
* This is an MVP — no end-to-end encryption yet. Use Tailscale if you need it over the internet.
* Like all Omarchy plugins, this runs unsandboxed with your user permissions. Review `Service.qml`, `BarWidget.qml`, and `cmd/omacom-engine/` before trusting it.

## Development

```sh
omarchy plugin validate . # check manifest
qmllint -I "$OMARCHY_PATH/shell" BarWidget.qml Service.qml
omarchy-shell shell call com.aktivesolutions.omacom state
```

Structure:

```
manifest.json          # id: com.aktivesolutions.omacom, 0.1.1, bar-widget + service
BarWidget.qml          # bar icon, hold-to-talk, peer count + Space PTT
Service.qml            # daemon lifecycle, unix-socket IPC, peer polling
cmd/omacom-engine/     # Go daemon — UDP discovery + audio (PipeWire/Opus, build from checkout)
bin/omacom-setup       # build script — go build from source, no download by default
```

## License

MIT — see [LICENSE](LICENSE).
