import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

// Omacom — bar widget + roster panel, following the Omarchy panel-widget
// setup (bluetooth/network): Ui.Panel owns the open/close lifecycle, the
// icon toggles the popup, KeyboardPanel anchors it under the button, and
// PanelKeyCatcher dismisses on Escape. Summoned from a hotkey through the
// generic shell IPC (`omarchy-shell shell toggle com.aktivesolutions.omacom`),
// which routes to whichever monitor has focus.
//
// PTT itself stays keybind-only (hold ALT+SPACE) — the bar can't distinguish
// click from hold, so the mouse only ever clicks, never holds. The panel is
// summoned with SUPER+CTRL+M.
//
// Daemon state is mirrored from the Service singleton via serviceFor(); this
// file holds no secrets — it only reflects daemon state.

Panel {
  id: root
  moduleName: "com.aktivesolutions.omacom"

  readonly property int discoveryPort: Number(root.setting("discoveryPort", 53318))

  // Daemon state mirrored from Service via IPC.
  readonly property var svc: (root.bar && root.bar.shell) ? root.bar.shell.serviceFor(moduleName) : null
  readonly property bool daemonUp: svc ? svc.daemonUp : false
  readonly property int peerCount: svc ? svc.peerCount : 0
  readonly property bool talking: svc ? svc.talking : false
  readonly property bool available: svc ? (svc.available !== false) : true
  readonly property var roster: svc ? (svc.roster || []) : []
  readonly property bool anyoneTalking: svc ? !!svc.anyoneTalking : false
  readonly property string talkDisplay: svc ? String(svc.talkDisplay || "") : ""
  readonly property string callSign: svc ? String(svc.callSign || "") : ""
  // How the daemon is reaching peers — LAN broadcast, direct addresses, or
  // Tailscale only. Worth showing: it is the difference between "anyone on
  // this wifi" and "only my tailnet".
  readonly property string transportText: svc ? String(svc.transportText || "") : ""
  readonly property bool tailnetOnly: svc ? !!svc.daemonTailnetOnly : false

  // The shell injects settings into widgets but not into services, so the
  // widget is the only place that sees them — hand them over so the service
  // can honour daemonSocket / discoveryPort / callSign.
  function pushSettings() {
    if (svc && "settings" in svc) svc.settings = root.settings
  }

  onSvcChanged: root.pushSettings()
  onSettingsChanged: root.pushSettings()
  Component.onCompleted: root.pushSettings()

  // Rename this station. The daemon persists the name and announces it on the
  // next hello; shell.json keeps a copy so a fresh daemon starts up as you.
  function applyCallSign(name) {
    var clean = String(name || "").trim()
    if (clean === "") { root.rerollCallSign(); return }
    if (svc && typeof svc.setCallSign === "function") svc.setCallSign(clean)
    root.persistCallSign(clean)
  }

  // Ask the daemon for a fresh random word, and drop the shell.json override
  // so the rolled name is what survives a restart.
  function rerollCallSign() {
    if (svc && typeof svc.rerollCallSign === "function") svc.rerollCallSign()
    root.persistCallSign("")
  }

  function persistCallSign(name) {
    if (!root.bar || !root.bar.shell || typeof root.bar.shell.updateEntryInline !== "function") return
    var entry = { id: root.moduleName }
    for (var key in root.settings) if (key !== "id") entry[key] = root.settings[key]
    if (String(entry.callSign || "") === String(name)) return
    entry.callSign = name
    root.settings = entry
    root.bar.shell.updateEntryInline(root.moduleName, entry)
  }

  // md-account_voice_outline (0xF1309) — idle; solid md-account_voice
  // (0xF05CB) while anyone transmits; md-account_tie_voice_off_outline
  // (0xF130B) when unavailable
  readonly property string icon: !root.available ? "󱌋" : (root.anyoneTalking ? "󰗋" : "󱌉")
  readonly property string statusText: !root.available
    ? "Turned Off"
    : !root.daemonUp ? "Daemon Not Running"
    : root.talking ? "On Air"
    : root.anyoneTalking ? "Receiving"
    : root.peerCount > 0 ? root.peerCount + (root.peerCount === 1 ? " Peer" : " Peers")
    : "Idle"

  function toggleAvailability() {
    if (svc && typeof svc.toggleAvailable === "function") svc.toggleAvailable()
  }

  onOpenedChanged: {
    // Fresh roster on open — the poll timer is 3s, too slow for a panel you
    // just asked for.
    if (opened && svc && typeof svc.refreshPeers === "function") svc.refreshPeers()
    if (!opened && callSignField) callSignField.focus = false
  }

  implicitWidth: row.implicitWidth
  implicitHeight: row.implicitHeight

  Row {
    id: row
    spacing: Style.space(5)

    BarIconButton {
      id: button
      bar: root.bar
      text: root.icon
      tooltipText: !root.available
        ? "Omacom — unavailable — SUPER+ALT+I to go available · SUPER+CTRL+M for the panel"
        : root.daemonUp
          ? (root.talking ? "Omacom — on air (release ALT+SPACE to stop)" : "Omacom — " + root.callSign + " · " + root.peerCount + " peer(s) on :" + root.discoveryPort + " · hold ALT+SPACE to talk")
          : "Omacom — daemon not running"
      slotSize: Style.bar.statusSlot
      opacity: !root.available ? 0.45 : 1.0
      onPressed: function(b) { root.toggle() }
    }

    // Who is on air — their call-sign, or ×N when several talk at once.
    Text {
      visible: root.anyoneTalking && !root.vertical
      text: root.talkDisplay
      color: root.talking ? Color.accent : (root.bar ? root.bar.foreground : Color.bar.text)
      font.family: Style.font.family
      font.pixelSize: Style.font.bodySmall
      font.bold: root.talking
      elide: Text.ElideRight
      width: Math.min(implicitWidth, Style.space(140))
      anchors.verticalCenter: parent.verticalCenter
    }
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(320))
    contentHeight: panel.fittedContentHeight(column.implicitHeight)

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      // The call-sign field owns every key while it is focused — otherwise
      // typing "a" would flip availability mid-name.
      blocked: callSignField.activeFocus

      onCloseRequested: root.close()
      onTextKey: function(t) {
        if (t === "a" || t === "A") root.toggleAvailability()
        else if (t === "c" || t === "C") callSignField.forceActiveFocus()
        else if (t === "r" || t === "R") root.rerollCallSign()
      }

      Column {
        id: column
        anchors.fill: parent
        spacing: Style.space(14)

        // ---------- Hero: intercom glyph · status · availability switch ----
        Item {
          width: parent.width
          implicitHeight: Math.max(heroIcon.implicitHeight, heroLabels.implicitHeight, availSwitch.implicitHeight)

          Text {
            id: heroIcon
            anchors.left: parent.left
            anchors.verticalCenter: parent.verticalCenter
            text: root.icon
            color: root.bar ? root.bar.foreground : Color.foreground
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.display
            opacity: root.available ? 1.0 : 0.5
          }

          // Availability switch — same op as SUPER+ALT+I, mouse and keyboard
          // alike (the key catcher forwards "a").
          ToggleSwitch {
            id: availSwitch
            checked: root.available
            foreground: root.bar ? root.bar.foreground : Color.foreground
            anchors.right: parent.right
            anchors.verticalCenter: parent.verticalCenter
            onToggled: root.toggleAvailability()

            PanelToolTip {
              visible: availSwitch.containsMouse
              text: root.available ? "Turn intercom off" : "Turn intercom on"
              fontFamily: root.bar ? root.bar.fontFamily : Style.font.family
            }
          }

          Column {
            id: heroLabels
            anchors.left: heroIcon.right
            anchors.leftMargin: Style.space(14)
            anchors.right: parent.right
            anchors.rightMargin: availSwitch.width + Style.space(12)
            anchors.verticalCenter: parent.verticalCenter
            spacing: Style.space(2)

            Text {
              text: "Omacom"
              color: root.bar ? root.bar.foreground : Color.foreground
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              font.pixelSize: Style.font.title
              font.bold: true
              elide: Text.ElideRight
              width: parent.width
            }

            Text {
              text: root.statusText.toUpperCase()
              color: Qt.darker(root.bar ? root.bar.foreground : Color.foreground, 1.4)
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              font.pixelSize: Style.font.caption
              font.bold: true
              font.letterSpacing: 1.2
              elide: Text.ElideRight
              width: parent.width
            }
          }
        }

        PanelSeparator {
          foreground: root.bar ? root.bar.foreground : Color.foreground
        }

        // ---------- Call sign: how peers see this machine ------------------
        Column {
          width: parent.width
          spacing: Style.space(8)

          PanelSectionHeader {
            text: "YOUR CALL SIGN"
            foreground: root.bar ? root.bar.foreground : Color.foreground
            fontFamily: root.bar ? root.bar.fontFamily : Style.font.family
          }

          Item {
            width: parent.width
            implicitHeight: Math.max(callSignField.implicitHeight, rerollButton.size)

            TextField {
              id: callSignField
              anchors.left: parent.left
              anchors.right: rerollButton.left
              anchors.rightMargin: Style.space(8)
              anchors.verticalCenter: parent.verticalCenter
              foreground: root.bar ? root.bar.foreground : Color.foreground
              placeholderText: "Call sign"
              maximumLength: 32
              enabled: root.daemonUp

              // Not a binding: the user types into this. External changes
              // (a re-roll, a clash resolved by the daemon) are copied in
              // only while the field is idle, so a rename in progress is
              // never yanked out from under the cursor.
              Component.onCompleted: text = root.callSign
              onAccepted: { root.applyCallSign(text); focus = false }
              Keys.onEscapePressed: { text = root.callSign; focus = false }
              onActiveFocusChanged: if (!activeFocus) text = root.callSign

              Connections {
                target: root
                function onCallSignChanged() {
                  if (!callSignField.activeFocus) callSignField.text = root.callSign
                }
              }
            }

            // md-refresh — roll a fresh word. Sized off the field so the two
            // controls read as one row.
            PanelActionButton {
              id: rerollButton
              anchors.right: parent.right
              anchors.verticalCenter: parent.verticalCenter
              iconText: "󰑐"
              tooltipText: "Roll a new call sign"
              foreground: root.bar ? root.bar.foreground : Color.foreground
              fontFamily: root.bar ? root.bar.fontFamily : Style.font.family
              size: callSignField.implicitHeight
              bordered: true
              enabled: root.daemonUp
              onClicked: root.rerollCallSign()
            }
          }

          Text {
            width: parent.width
            text: "Enter to rename · " + (root.daemonUp ? "peers see this name" : "daemon not running")
            color: Qt.darker(root.bar ? root.bar.foreground : Color.foreground, 1.5)
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.caption
            elide: Text.ElideRight
          }
        }

        PanelSeparator {
          foreground: root.bar ? root.bar.foreground : Color.foreground
        }

        // ---------- Roster: everyone joined, call-signs --------------------
        Column {
          id: rosterList
          visible: root.roster.length > 1
          width: parent.width
          spacing: Style.space(10)

          PanelSectionHeader {
            text: "JOINED"
            foreground: root.bar ? root.bar.foreground : Color.foreground
            fontFamily: root.bar ? root.bar.fontFamily : Style.font.family
          }

          Repeater {
            model: root.roster

            // RosterRow declares `required property var modelData`, which the
            // Repeater fills in directly — no extra plumbing needed.
            delegate: RosterRow {
              width: rosterList.width
            }
          }
        }

        Text {
          visible: root.roster.length <= 1
          width: parent.width
          text: !root.daemonUp ? "Daemon not running"
            : !root.available ? "Turn the intercom on to join"
            : "No one else joined yet"
          color: Qt.darker(root.bar ? root.bar.foreground : Color.foreground, 1.5)
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.bodySmall
          wrapMode: Text.WordWrap
        }

        // ---------- Transport: where this station is reachable ------------
        Row {
          visible: root.transportText !== ""
          width: parent.width
          spacing: Style.space(6)

          // md-lan-connect when broadcasting on the LAN, md-shield-lock when
          // the daemon is refusing everything outside Tailscale.
          Text {
            text: root.tailnetOnly ? "󰦝" : "󰲝"
            color: root.tailnetOnly ? Color.accent : Qt.darker(root.bar ? root.bar.foreground : Color.foreground, 1.5)
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.caption
            anchors.verticalCenter: parent.verticalCenter
          }

          Text {
            text: root.transportText
            color: Qt.darker(root.bar ? root.bar.foreground : Color.foreground, 1.5)
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.caption
            elide: Text.ElideRight
            anchors.verticalCenter: parent.verticalCenter
          }
        }
      }
    }
  }

  // Two-line roster row: call-sign + live status, on-air glyph lit while the
  // station transmits. Same hover treatment as bluetooth's device rows.
  component RosterRow: CursorSurface {
    id: row
    required property var modelData

    readonly property string callSign: modelData.name || "?"
    readonly property bool self: modelData.self === true
    readonly property bool onAir: modelData.talking === true

    hasCursor: rowMouse.containsMouse
    foreground: root.bar ? root.bar.foreground : Color.foreground

    implicitHeight: Math.max(Style.space(34), rowContent.implicitHeight)

    Item {
      id: rowContent
      anchors.left: parent.left
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      anchors.leftMargin: Style.space(10)
      anchors.rightMargin: Style.space(10)
      implicitHeight: Math.max(rowGlyph.implicitHeight, info.implicitHeight)

      Text {
        id: rowGlyph
        anchors.left: parent.left
        anchors.verticalCenter: parent.verticalCenter
        text: row.onAir ? "󰗋" : "󱌉"
        color: row.onAir ? Color.accent : Qt.darker(row.foreground, 1.4)
        font.family: root.bar ? root.bar.fontFamily : Style.font.family
        font.pixelSize: Style.font.heading
      }

      Column {
        id: info
        spacing: Style.space(1)
        anchors.left: rowGlyph.right
        anchors.leftMargin: Style.space(10)
        anchors.right: parent.right
        anchors.verticalCenter: parent.verticalCenter

        Text {
          text: row.callSign + (row.self ? " (you)" : "")
          color: row.onAir ? Color.accent : row.foreground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.body
          font.bold: row.onAir
          elide: Text.ElideRight
          width: parent.width
        }

        Text {
          text: row.onAir ? "ON AIR" : (row.self ? "This machine" : "Listening")
          color: Qt.darker(row.foreground, 1.4)
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.caption
          elide: Text.ElideRight
          width: parent.width
        }
      }
    }

    MouseArea {
      id: rowMouse
      anchors.fill: parent
      hoverEnabled: true
    }
  }
}
