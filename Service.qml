import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons

// Omacom Service — owns the omacom-engine daemon lifecycle.
//
// Real audio path now implemented in cmd/omacom-engine (Go):
//   PipeWire capture (pw-cat --record) → Opus/raw s16le@48k → UDP broadcast
//   on :53318 to discovered peers, and reverse for playback. Discovery is
//   UDP hello every 2s, peers expire after 10s. IPC via unix socket at
//   $XDG_RUNTIME_DIR/omacom.sock — QML only reflects state and forwards PTT.
//
// Security: no sudo, no package install, no remote download. `bin/omacom-setup`
// builds the daemon from the checkout's source (`go build ./cmd/omacom-engine`)
// so the running binary is auditable from the reviewed commit. Without Go it
// stops and explains.

Service {
  id: root
  moduleName: "com.aktivesolutions.omacom"

  property bool daemonUp: false
  property int peerCount: 0
  property bool talking: false
  property bool available: true
  property string lastError: ""
  property var peers: []

  readonly property string socketPath: {
    var s = String(root.setting("daemonSocket", "")).trim()
    if (s) return s
    var rt = Quickshell.env("XDG_RUNTIME_DIR")
    if (rt) return rt + "/omacom.sock"
    return "/tmp/omacom.sock"
  }
  readonly property int discoveryPort: Number(root.setting("discoveryPort", 53318))
  readonly property bool autoStart: root.setting("autoStartDaemon", true) === true
  readonly property string engineBin: {
    var h = Quickshell.env("HOME")
    if (h) return h + "/.local/bin/omacom-engine"
    return "/tmp/omacom-engine"
  }

  // -- daemon lifecycle ----------------------------------------------------

  function ensureDaemon() {
    if (!root.autoStart) return
    // Check if binary exists — if not, show helpful state; bin/omacom-setup builds it.
    // We probe via `test -x` then start daemonProc if present.
    probeProc.running = true
  }

  function startDaemon() {
    if (daemonProc.running) return
    daemonProc.command = [root.engineBin, "--socket", root.socketPath, "--port", String(root.discoveryPort)]
    daemonProc.running = true
  }

  function stopDaemon() {
    if (daemonProc.running) daemonProc.running = false
    root.daemonUp = false
    root.peerCount = 0
    root.talking = false
  }

  // -- PTT over unix socket (via socat/nc) --------------------------------
  // QML can't hold a persistent socket, so we shell out per PTT edge. Payloads
  // are tiny JSON, bounded, with a 1s deadline.

  function sendOp(op) {
    if (!root.daemonUp) { root.ensureDaemon(); return }
    // Support op with payload like setAvailable:true
    var json = op.indexOf(":") !== -1
      ? "{\"op\":\"" + op.split(":")[0] + "\",\"value\":" + op.split(":")[1] + "}"
      : "{\"op\":\"" + op + "\"}"
    socketProc.op = op
    socketProc.command = ["sh", "-c", "printf '" + json + "\\n' | socat -t1 - UNIX-CONNECT:" + root.socketPath + " 2>/dev/null || printf '" + json + "\\n' | nc -U " + root.socketPath + " 2>/dev/null || true"]
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
    root.sendOp(root.available ? "setAvailable:true" : "setAvailable:false")
    // Refresh peers immediately so bar tooltip updates
    Qt.callLater(function() { root.refreshPeers() })
  }

  function togglePanel() {
    // Future: open peer list panel. For now flip talking as demo if daemon down.
    if (!root.daemonUp) root.ensureDaemon()
    else root.talking = !root.talking
  }

  function refreshPeers() {
    if (!root.daemonUp) return
    pollProc.command = ["sh", "-c", "printf '{\"op\":\"getPeers\"}\\n' | socat -t1 - UNIX-CONNECT:" + root.socketPath + " 2>/dev/null | head -c 4096"]
    pollProc.running = true
  }

  Component.onCompleted: {
    if (root.autoStart) root.ensureDaemon()
    pollTimer.start()
  }

  // -- Introspection -------------------------------------------------------

  function state() {
    return JSON.stringify({
      daemonUp: root.daemonUp,
      peerCount: root.peerCount,
      talking: root.talking,
      available: root.available,
      peers: root.peers,
      socketPath: root.socketPath,
      discoveryPort: root.discoveryPort,
      lastError: root.lastError
    })
  }

  IpcHandler {
    target: "com.aktivesolutions.omacom"
    function state(): string { return root.state() }
    function startTalking(): void { root.startTalking() }
    function stopTalking(): void { root.stopTalking() }
    function toggleTalking(): void { root.toggleTalking() }
    function getPeers(): string { root.refreshPeers(); return root.state() }
    function toggleAvailable(): void { root.toggleAvailable() }
    function setAvailable(v: bool): void { root.available = !!v; root.sendOp(v ? "setAvailable:true" : "setAvailable:false") }
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
    // Started on demand via startDaemon(); keepLoaded-equivalent via Service keepLoaded
    stdout: StdioCollector { waitForEnd: false }
    stderr: StdioCollector { waitForEnd: false }
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
        try { var j = JSON.parse(String(socketOut.text)); if (j.talking !== undefined) root.talking = !!j.talking } catch(e) {}
      }
    }
  }

  Process {
    id: pollProc
    stdout: StdioCollector { id: pollOut; waitForEnd: true }
    onExited: function(exitCode) {
      var txt = String(pollOut.text || "").trim()
      if (!txt) return
      try {
        var j = JSON.parse(txt)
        if (Array.isArray(j.peers)) {
          root.peers = j.peers
          root.peerCount = j.peers.length
        }
        if (j.talking !== undefined) root.talking = !!j.talking
        if (j.available !== undefined) root.available = !!j.available
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
