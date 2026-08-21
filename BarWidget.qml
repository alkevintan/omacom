import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// Omacom — bar widget companion to the service daemon.
//
// Status-only indicator: shows peer count / talking / availability state.
// All interaction lives on keybinds (hold SUPER+I to talk — see README) and
// the shell IPC. Omarchy's bar forwards every non-drag release as a click,
// so mouse actions on bar icons can't distinguish click from hold — the
// widget opts out of click handling entirely (pressable: false). This file
// holds no secrets — it only reflects daemon state.

BarWidget {
  id: root
  moduleName: "com.aktivesolutions.omacom"

  readonly property int discoveryPort: Number(root.setting("discoveryPort", 53318))
  readonly property bool autoStart: root.setting("autoStartDaemon", true) === true

  // Daemon state mirrored from Service via IPC — keep minimal for 0.1.0 MVP.
  // Service exposes { listening: bool, peers: int, talking: bool } via shell.
  readonly property var svc: (root.bar && root.bar.shell) ? root.bar.shell.serviceFor(moduleName) : null
  readonly property bool daemonUp: svc ? svc.daemonUp : false
  readonly property int peerCount: svc ? svc.peerCount : 0
  readonly property bool talking: svc ? svc.talking : false
  readonly property bool available: svc ? (svc.available !== false) : true

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    // md-account_voice_outline (0xF1309) — idle; solid md-account_voice
    // (0xF05CB) while transmitting; md-account_tie_voice_off_outline
    // (0xF130B) when unavailable
    text: !root.available ? "󱌋" : (root.talking ? "󰗋" : "󱌉")
    tooltipText: !root.available
      ? "Omacom — unavailable (intercom off) — SUPER+ALT+I to go available"
      : root.daemonUp
        ? (root.talking ? "Omacom — talking (release SUPER+I to stop)" : "Omacom — " + root.peerCount + " peer(s) on :" + root.discoveryPort + " — hold SUPER+I to talk · SUPER+ALT+I to go unavailable")
        : "Omacom — daemon not running"
    slotSize: Style.bar.statusSlot
    opacity: !root.available ? 0.45 : 1.0
    pressable: false
    // Visual feedback while transmitting — uses bar accent, not menu tokens
    // so it stays visible on every theme (like 1Passchy's selectedFill).
    property color baseFg: root.bar ? root.bar.foreground : Color.foreground
    property color activeBg: root.talking ? Style.selectedFillFor(baseFg, Color.accent) : "transparent"
  }
}
