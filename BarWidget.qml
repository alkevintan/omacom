import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// Omacom — bar widget companion to the service daemon.
//
// Hold to talk: press and hold the button (or configured PTT key) while the
// service streams Opus over LAN / Tailscale. This file holds no secrets — it
// only reflects daemon state and forwards press/release.

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

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    // nf-md-bullhorn (0xF0E6F) — intercom/broadcast
    text: root.talking ? "󰸞" : "󰸢"
    tooltipText: root.daemonUp
      ? (root.talking ? "Omacom — talking (release to stop)" : "Omacom — " + root.peerCount + " peer(s) on :" + root.discoveryPort + " — hold to talk")
      : "Omacom — daemon not running (click to start)"
    slotSize: Style.bar.statusSlot
    // Visual feedback while transmitting — uses bar accent, not menu tokens
    // so it stays visible on every theme (like 1Passchy's selectedFill).
    property color baseFg: root.bar ? root.bar.foreground : Color.foreground
    property color activeBg: root.talking ? Style.selectedFillFor(baseFg, Color.accent) : "transparent"

    onPressed: function(buttonCode) {
      if (!root.bar || !root.bar.shell) return
      if (buttonCode === Qt.LeftButton) {
        if (svc && typeof svc.startTalking === "function") svc.startTalking()
      } else if (buttonCode === Qt.RightButton) {
        if (svc && typeof svc.togglePanel === "function") svc.togglePanel()
      }
    }
    onReleased: function(buttonCode) {
      if (buttonCode === Qt.LeftButton && svc && typeof svc.stopTalking === "function") svc.stopTalking()
    }
  }

  // Keyboard PTT — when the bar has focus, Space/M as configured. The service
  // owns the real audio; this just mirrors the key so a keyboard-only demo works.
  Item {
    id: pttKey
    focus: true
    Keys.onPressed: function(event) {
      if (event.key === Qt.Key_Space && svc && typeof svc.startTalking === "function") { svc.startTalking(); event.accepted = true }
    }
    Keys.onReleased: function(event) {
      if (event.key === Qt.Key_Space && svc && typeof svc.stopTalking === "function") { svc.stopTalking(); event.accepted = true }
    }
  }
}
