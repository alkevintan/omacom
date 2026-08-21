import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons

// Omacom Service — owns the omacom-engine daemon lifecycle.
//
// Real audio path now implemented in cmd/omacom-engine (Go):
//   PipeWire capture (pw-cat --record --raw) → raw s16le@48k, one 20ms frame
//   per UDP packet to discovered peers, and reverse for playback. Discovery
//   is a hello every 2s — LAN broadcast plus unicast to tailnet peers — and
//   peers expire after 10s. IPC via unix socket at $XDG_RUNTIME_DIR/omacom.sock
//   — QML only reflects state and forwards PTT.
//
// Security: no sudo, no package install, no remote download. `bin/omacom-setup`
// builds the daemon from the checkout's source (`go build ./cmd/omacom-engine`)
// so the running binary is auditable from the reviewed commit. Without Go it
// stops and explains.

Item {
  id: root

  // Injected by the shell's service host (shell.qml ensureService)
  property var shell: null
  property var manifest: null
  property var pluginRegistry: null
  property var barWidgetRegistry: null
  property string omarchyPath: ""

  // Plugin settings — manifest defaults, overridable via shell.json entry
  property var settings: ({})

  function setting(name, fallback) {
    var value = settings ? settings[name] : undefined
    if (value === undefined || value === null) {
      var defs = manifest && manifest.barWidget && manifest.barWidget.defaults
        ? manifest.barWidget.defaults : {}
      value = defs[name]
    }
    return value === undefined || value === null ? fallback : value
  }

  property bool daemonUp: false
  property int peerCount: 0
  property bool talking: false
  property bool available: true
  property string lastError: ""
  property var peers: []
  // Call-signs of remote peers currently on air (daemon talkHold window).
  property var talkers: []
  // Roster UI (BarWidget panel) reads these via serviceFor(); the panel
  // lifecycle itself lives in the widget, per Omarchy's bluetooth/network
  // convention.

  // The daemon owns call-sign assignment: it rolls a random word on first
  // run and remembers it in $XDG_STATE_HOME/omacom/callsign. We mirror what
  // it reports; the plugin setting is only a startup override.
  property string daemonCallSign: ""
  readonly property string configuredCallSign: root.sanitizeCallSign(root.setting("callSign", ""))
  readonly property string callSign: root.daemonCallSign || root.configuredCallSign

  // Mirror of the daemon's sanitizeCallSign — strip framing/control chars so
  // a name can neither forge a packet field nor smuggle shell syntax.
  function sanitizeCallSign(name) {
    var out = String(name === undefined || name === null ? "" : name)
      .replace(/[|\r\n\x00-\x1f]/g, "")
      .replace(/['"\\`$]/g, "")
      .trim()
    return out.length > 32 ? out.slice(0, 32) : out
  }

  // Everyone currently on air — self first when talking, then remote talkers.
  readonly property var activeTalkers: {
    var list = []
    if (root.talking && root.available) list.push(root.callSign || "me")
    var t = Array.isArray(root.talkers) ? root.talkers : []
    for (var i = 0; i < t.length; i++) {
      if (list.indexOf(String(t[i])) === -1) list.push(String(t[i]))
    }
    return list
  }
  // Bar label: the call-sign when exactly one person talks, a counter when
  // several do, empty when the intercom is quiet.
  readonly property string talkDisplay: root.activeTalkers.length === 0 ? ""
    : root.activeTalkers.length === 1 ? root.activeTalkers[0]
    : "×" + root.activeTalkers.length
  readonly property bool anyoneTalking: root.activeTalkers.length > 0

  // Roster rows for the dropdown — self first, then discovered peers.
  readonly property var roster: {
    var rows = [{ name: root.callSign || "me", self: true, talking: root.talking && root.available }]
    var ps = Array.isArray(root.peers) ? root.peers : []
    for (var i = 0; i < ps.length; i++) {
      var p = ps[i] || {}
      var name = p.callSign ? String(p.callSign) : (p.ip ? String(p.ip) : "?")
      rows.push({ name: name, self: false, talking: root.talkers.indexOf(name) !== -1 })
    }
    return rows
  }

  // Op/poll ordering: timestamps let a poll response that was already in
  // flight before an op was sent get discarded instead of reverting the
  // optimistic state flip (e.g. toggleAvailable snapping back).
  // Last line the daemon logged, for the panel and for `state`.
  property string daemonLog: ""

  function noteDaemonLog(line) {
    var text = String(line || "").trim()
    if (!text) return
    root.daemonLog = text
    console.log("omacom-engine:", text)
  }

  property double _lastOpSent: 0
  property double _pollStarted: 0

  // No /tmp fallback anywhere below. A socket in a world-writable directory
  // is one another local user can plant first and then read every op we send;
  // a binary path there is one they can plant and have us execute. Without a
  // private runtime directory and a home, Omacom declines to run.
  readonly property string socketPath: {
    var s = String(root.setting("daemonSocket", "")).trim()
    if (s) return s
    var rt = Quickshell.env("XDG_RUNTIME_DIR")
    return rt ? rt + "/omacom.sock" : ""
  }
  readonly property int discoveryPort: Number(root.setting("discoveryPort", 53318))
  readonly property bool autoStart: root.setting("autoStartDaemon", true) === true

  // Transport. Broadcast finds LAN peers only — it cannot cross a tunnel —
  // so reaching a tailnet means unicasting to each peer. The daemon pulls
  // those addresses from `tailscale status`; staticPeers adds any it can't
  // see (a port-forwarded host, a non-Tailscale WireGuard mesh).
  readonly property bool tailnetOnly: root.setting("tailnetOnly", false) === true
  readonly property string staticPeers: String(root.setting("staticPeers", "")).trim()

  // Everything the daemon has to be restarted for, as one comparable value.
  readonly property string transportKey: [root.socketPath, root.discoveryPort,
    root.tailnetOnly ? "1" : "0", root.staticPeers].join("|")

  // Reported back by the daemon, for the panel to show what is in use.
  property bool daemonTailnetOnly: false
  property int directTargets: 0
  readonly property string transportText: !root.daemonUp ? ""
    : root.daemonTailnetOnly
      ? "Tailscale only · " + root.directTargets + (root.directTargets === 1 ? " address" : " addresses")
      : root.directTargets > 0
        ? "LAN broadcast + " + root.directTargets + " direct"
        : "LAN broadcast"
  readonly property string engineBin: {
    var h = Quickshell.env("HOME")
    return h ? h + "/.local/bin/omacom-engine" : ""
  }

  // -- daemon lifecycle ----------------------------------------------------

  function ensureDaemon() {
    if (!root.autoStart) return
    if (!root.engineBin || !root.socketPath) {
      root.daemonUp = false
      root.lastError = "no HOME or XDG_RUNTIME_DIR — refusing to fall back to /tmp"
      return
    }
    // Check if binary exists — if not, show helpful state; bin/omacom-setup builds it.
    // We probe via `test -x` then start daemonProc if present.
    probeProc.running = true
  }

  // What the live daemon was actually launched with — a settings edit that
  // changes any of it needs a restart, not just a new binding.
  property string _runningTransport: ""

  function startDaemon() {
    if (daemonProc.running) return
    root._runningTransport = root.transportKey
    var args = [root.engineBin, "--socket", root.socketPath, "--port", String(root.discoveryPort)]
    if (root.tailnetOnly) args.push("--tailnet-only")
    if (root.staticPeers) args.push("--peers", root.staticPeers)
    // Only an explicit setting is passed through; with no flag the daemon
    // restores its persisted call-sign or rolls a new one.
    if (root.configuredCallSign) args.push("--callsign", root.configuredCallSign)
    daemonProc.command = args
    daemonProc.running = true
  }

  function stopDaemon() {
    if (daemonProc.running) daemonProc.running = false
    root.daemonUp = false
    root.peerCount = 0
    root.talking = false
    root.talkers = []
  }

  function restartDaemon() {
    root.stopDaemon()
    restartTimer.restart()
  }

  Timer {
    id: restartTimer
    interval: 250
    onTriggered: root.ensureDaemon()
  }

  // Settings reach the service through the bar widget (the shell injects them
  // into widgets, not services), so they can land after the daemon is already
  // up. React rather than assume they were known at startup.
  onTransportKeyChanged: root._applyTransportSettings()
  onConfiguredCallSignChanged: {
    if (root.configuredCallSign && root.daemonUp && root.configuredCallSign !== root.daemonCallSign)
      root.setCallSign(root.configuredCallSign)
  }

  function _applyTransportSettings() {
    if (!daemonProc.running) return
    if (root.transportKey === root._runningTransport) return
    root.restartDaemon()
  }

  // -- PTT over unix socket (via socat/nc) --------------------------------
  // QML can't hold a persistent socket, so we shell out per PTT edge. Payloads
  // are tiny JSON, bounded, with a 1s deadline.

  // Single-quote a string for `sh -c`. Every value that reaches the shell
  // goes through here — call-signs are user text and must never be able to
  // close the quote and append a command.
  function shQuote(s) {
    return "'" + String(s).split("'").join("'\\''") + "'"
  }

  function sendOp(op, value) {
    if (!root.daemonUp) { root.ensureDaemon(); return }
    var req = { op: String(op) }
    // The daemon takes "value" as a JSON string and coerces it per-op.
    if (value !== undefined && value !== null) req.value = String(value)
    var payload = root.shQuote(JSON.stringify(req))
    socketProc.op = op
    root._lastOpSent = Date.now()
    // printf '%s\n' keeps the payload literal — no % or backslash expansion.
    var sock = root.shQuote(root.socketPath)
    socketProc.command = ["sh", "-c",
      "printf '%s\\n' " + payload + " | socat -t1 - UNIX-CONNECT:" + sock + " 2>/dev/null"
      + " || printf '%s\\n' " + payload + " | nc -U " + sock + " 2>/dev/null || true"]
    socketProc.running = true
  }

  function startTalking() {
    if (!root.available) return
    root.talking = true
    root.sendOp("startTalking")
  }

  function stopTalking() {
    root.talking = false
    root.sendOp("stopTalking")
  }

  function toggleTalking() {
    if (!root.available) return
    if (root.talking) root.stopTalking()
    else root.startTalking()
  }

  function toggleAvailable() {
    root.available = !root.available
    if (!root.available) root.talking = false
    root.sendOp("setAvailable", root.available ? "true" : "false")
    // socketProc.onExited triggers refreshPeers once the op has landed —
    // polling earlier would read the old state and revert the toggle.
  }

  // Rename this station. An empty name asks the daemon to roll a fresh word.
  function setCallSign(name) {
    var clean = root.sanitizeCallSign(name)
    // Optimistic so the panel field settles immediately; the poll response
    // carries the daemon's final answer (it may re-roll on a clash).
    if (clean) root.daemonCallSign = clean
    root.sendOp("setCallSign", clean)
  }

  function rerollCallSign() {
    root.sendOp("setCallSign", "")
  }

  function togglePanel() {
    // Panel lifecycle lives in the bar widget (Ui.Panel). Summon it through
    // the generic shell IPC, which routes to the focused monitor's instance:
    //   omarchy-shell shell toggle com.aktivesolutions.omacom
    if (root.shell && typeof root.shell.summon === "function") {
      root.shell.summon("com.aktivesolutions.omacom")
      return
    }
    // Fallback when the shell handle is missing: refresh so the widget at
    // least shows a fresh roster when it does open.
    root.refreshPeers()
  }

  function refreshPeers() {
    if (!root.daemonUp) return
    if (socketProc.running) return // an op is in flight — poll after it lands
    root._pollStarted = Date.now()
    pollProc.command = ["sh", "-c", "printf '{\"op\":\"getPeers\"}\\n' | socat -t1 - UNIX-CONNECT:" + root.socketPath + " 2>/dev/null | head -c 4096"]
    pollProc.running = true
  }

  Component.onCompleted: {
    if (root.autoStart) root.ensureDaemon()
    pollTimer.start()
  }

  // -- Introspection -------------------------------------------------------

  function stateJson() {
    return JSON.stringify({
      daemonUp: root.daemonUp,
      peerCount: root.peerCount,
      talking: root.talking,
      available: root.available,
      peers: root.peers,
      callSign: root.callSign,
      talkers: root.talkers,
      activeTalkers: root.activeTalkers,
      talkDisplay: root.talkDisplay,
      socketPath: root.socketPath,
      discoveryPort: root.discoveryPort,
      tailnetOnly: root.daemonTailnetOnly,
      directTargets: root.directTargets,
      transport: root.transportText,
      daemonLog: root.daemonLog,
      lastError: root.lastError
    })
  }

  IpcHandler {
    target: "com.aktivesolutions.omacom"
    function state(): string { return root.stateJson() }
    function startTalking(): void { root.startTalking() }
    function stopTalking(): void { root.stopTalking() }
    function toggleTalking(): void { root.toggleTalking() }
    function getPeers(): string { root.refreshPeers(); return root.stateJson() }
    function toggleAvailable(): void { root.toggleAvailable() }
    function setAvailable(v: bool): void { root.available = !!v; root.sendOp("setAvailable", v ? "true" : "false") }
    function setCallSign(name: string): string { root.setCallSign(name); return root.callSign }
    function rerollCallSign(): void { root.rerollCallSign() }
    function togglePanel(): void { root.togglePanel() }
  }

  // -- Processes -----------------------------------------------------------

  Process {
    id: probeProc
    command: ["sh", "-c", "test -x " + root.engineBin + " && echo ok || echo missing"]
    stdout: StdioCollector { id: probeOut; waitForEnd: true }
    onExited: function(exitCode) {
      var out = String(probeOut.text || "").trim()
      if (out === "ok") {
        root.startDaemon()
      } else {
        root.daemonUp = false
        root.lastError = "daemon not built — run bin/omacom-setup (needs Go)"
        // Still show bar widget as "not running" rather than failing validation
      }
    }
  }

  Process {
    id: daemonProc
    // Started on demand via startDaemon(); keepLoaded-equivalent via Service keepLoaded.
    //
    // The daemon's log is the only account of what it did — why a call-sign
    // was re-rolled, which peers it found, why audio was dropped. Collecting
    // it into a buffer nobody reads makes those questions unanswerable, so
    // each line goes to the shell's own log (journalctl --user -t omarchy-shell)
    // and the last one is kept for the panel.
    stdout: SplitParser { onRead: function(line) { root.noteDaemonLog(line) } }
    stderr: SplitParser { onRead: function(line) { root.noteDaemonLog(line) } }
    onExited: function(exitCode) {
      root.daemonUp = false
      if (exitCode !== 0) root.lastError = "daemon exited " + exitCode + " — check ~/.local/bin/omacom-engine"
    }
    onStarted: {
      root.daemonUp = true
      root.lastError = ""
      // Give daemon 300ms to bind socket, then poll peers
      Qt.callLater(function() { daemonUpTimer.restart() })
    }
  }

  Timer {
    id: daemonUpTimer
    interval: 300
    onTriggered: root.refreshPeers()
  }

  Process {
    id: socketProc
    property string op: ""
    stdout: StdioCollector { id: socketOut; waitForEnd: true }
    onExited: function(exitCode) {
      // PTT is best-effort; peer poll will correct talking state
      if (socketOut.text) {
        try {
          var j = JSON.parse(String(socketOut.text))
          if (j.talking !== undefined) root.talking = !!j.talking
          if (j.callSign !== undefined) root.daemonCallSign = String(j.callSign)
          if (Array.isArray(j.talkers)) root.talkers = j.talkers
        } catch(e) {}
      }
      // The op has landed — refresh so the UI reflects the daemon's state
      root.refreshPeers()
    }
  }

  Process {
    id: pollProc
    stdout: StdioCollector { id: pollOut; waitForEnd: true }
    onExited: function(exitCode) {
      // Discard responses that were already in flight before the latest op —
      // they carry pre-op state and would revert e.g. toggleAvailable.
      if (root._lastOpSent > root._pollStarted) return
      var txt = String(pollOut.text || "").trim()
      if (!txt) return
      try {
        var j = JSON.parse(txt)
        if (Array.isArray(j.peers)) {
          root.peers = j.peers
          root.peerCount = j.peers.length
        }
        root.talkers = Array.isArray(j.talkers) ? j.talkers : []
        if (j.talking !== undefined) root.talking = !!j.talking
        if (j.available !== undefined) root.available = !!j.available
        if (j.callSign !== undefined) root.daemonCallSign = String(j.callSign)
        if (j.tailnetOnly !== undefined) root.daemonTailnetOnly = !!j.tailnetOnly
        if (j.directTarget !== undefined) root.directTargets = Number(j.directTarget)
        root.daemonUp = true
        root.lastError = ""
      } catch(e) {
        // ignore malformed
      }
    }
  }

  Timer {
    id: pollTimer
    interval: 3000
    repeat: true
    onTriggered: root.refreshPeers()
  }
}
