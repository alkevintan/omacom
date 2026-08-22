package main

// omacom-engine — PTT intercom daemon for Omacom.
//
// Audio path: PipeWire capture → UDP to discovered peers, and reverse. Raw
// s16le@48k mono, one 20ms frame per packet, no extra Go deps. Format and
// per-talker playback sinks live in audio.go. Compression is a follow-up:
// an Opus build would use gopkg.in/hraban/opus.v2 and cut the bitrate by
// roughly 20x, at the cost of the first non-stdlib dependency.
//
// IPC: unix socket at $XDG_RUNTIME_DIR/omacom.sock — QML Service writes
// {"op":"startTalking"} / {"op":"stopTalking"} / {"op":"getPeers"} as newline
// JSON. We reply with {"peers":[...],"talkers":[...],"talking":bool,
// "callSign":"..."} where needed.
//
// Call-signs: each daemon announces a call-sign. With no --callsign flag it
// rolls a random word (see callsign.go) and remembers it in
// $XDG_STATE_HOME/omacom/callsign, so a station keeps its handle across
// restarts and the hostname never hits the wire. It can be renamed at runtime
// with {"op":"setCallSign","value":"Falcon"} (empty value re-rolls).
// Hello packets carry "omacom-hello-v1|<call-sign>|<talking01>";
// PTT edges broadcast "omacom-onair-v1|<call-sign>" / "omacom-offair-v1|..."
// 3x so peers can show who is on air immediately. A peer counts as talking
// for talkHold after its last claim. Bare legacy hellos are still accepted
// (peer shown by IP); audio packet format is unchanged.
//
// Discovery: a hello every 2s — LAN broadcast, and unicast to tailnet peers
// and any --peers addresses (see discovery.go). Peers expire after 10s. No
// global relay. All UDP payloads are bounded (max 2048) and carry deadlines.
//
// Inbound is untrusted: every packet passes a per-source token bucket, the
// roster is capped, and audio is only played from an address that has already
// joined the roster. --tailnet-only additionally drops anything from outside
// Tailscale's address ranges and stops broadcasting on the LAN.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxUDP      = 2048
	helloMagic  = "omacom-hello-v1"
	audioMagic  = "omacom-audio-v1|"
	onAirMagic  = "omacom-onair-v1|"
	offAirMagic = "omacom-offair-v1|"
	// A peer counts as "talking" this long after its last on-air/hello claim.
	talkHold = 3 * time.Second
)

type peer struct {
	IP       string    `json:"ip"`
	CallSign string    `json:"callSign,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
	LastTalk time.Time `json:"-"`
}

type daemon struct {
	socketPath  string
	udpPort     int
	callSign    string
	tailnetOnly bool

	sinks   *playback
	limiter *limiter
	targets *targets

	mu           sync.Mutex
	callSignAuto bool             // rolled by us, so a clash may be re-rolled
	peers        map[string]*peer // ip string -> peer info
	talking      bool
	available    bool // intercom power — false = do not broadcast hello, ignore audio
	audioCmd     *exec.Cmd
	udpConn      *net.UDPConn
}

// sanitizeCallSign strips separators/control chars so a call-sign can never
// forge packet framing (fields are "|" delimited) and stays one short line.
const maxCallSignRunes = 32

func sanitizeCallSign(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '|': // packet field separator
			return -1
		case r == '<' || r == '>': // QML Text auto-detects rich text
			return -1
		case r == utf8.RuneError: // invalid UTF-8 from the wire
			return -1
		case unicode.IsControl(r): // covers NUL, CR, LF, DEL, C1
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	// Truncate by runes, not bytes. `len(s)` counts bytes, so a name of 12
	// emoji measured 48 and was then sliced to []rune(s)[:32] -- past the
	// end of a 12-element slice. Go permits that whenever the conversion
	// over-allocated, so instead of panicking it silently appended NUL
	// runes, which then went out in hello packets and into the roster.
	if r := []rune(s); len(r) > maxCallSignRunes {
		s = string(r[:maxCallSignRunes])
	}
	return s
}

func main() {
	var (
		socket   = flag.String("socket", "", "unix socket path (default $XDG_RUNTIME_DIR/omacom.sock)")
		port     = flag.Int("port", 53318, "discovery/audio UDP port")
		callsign = flag.String("callsign", "", "call-sign announced to peers (default: a random word)")
		peers    = flag.String("peers", "", "comma-separated peer addresses to reach directly (tailnet IPs, port-forwarded hosts)")
		tsOnly   = flag.Bool("tailnet-only", false, "only talk to Tailscale addresses; skip LAN broadcast")
	)
	flag.Parse()
	if *socket == "" {
		// Deliberately no /tmp fallback: a control socket in a shared
		// world-writable directory can be squatted by another local user,
		// who then receives every op the shell sends. Without a private
		// runtime directory the caller must name the path themselves.
		rt := os.Getenv("XDG_RUNTIME_DIR")
		if rt == "" {
			log.Fatalf("omacom-engine: XDG_RUNTIME_DIR is unset — pass --socket with a path only you can write")
		}
		*socket = filepath.Join(rt, "omacom.sock")
	}
	// Explicit flag wins; then whatever this machine was called last time;
	// otherwise roll a word and remember it.
	cs := sanitizeCallSign(*callsign)
	auto := false
	if cs == "" {
		cs = loadCallSign()
		auto = true
	}
	if cs == "" {
		cs = randomCallSign(nil)
		saveCallSign(cs)
		log.Printf("assigned call-sign %q (change it in the Omacom panel)", cs)
	}
	d := &daemon{
		socketPath:   *socket,
		udpPort:      *port,
		callSign:     cs,
		callSignAuto: auto,
		tailnetOnly:  *tsOnly,
		sinks:        newPlayback(),
		limiter:      newLimiter(),
		targets:      newTargets(*peers),
		peers:        make(map[string]*peer),
		available:    true,
	}
	if err := d.run(); err != nil {
		log.Fatalf("omacom-engine: %v", err)
	}
}

func (d *daemon) run() error {
	// UDP listener for discovery + audio.
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", d.udpPort))
	if err != nil {
		return fmt.Errorf("resolve :%d: %w", d.udpPort, err)
	}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen :%d: %w", d.udpPort, err)
	}
	d.udpConn = c
	defer c.Close()
	log.Printf("omacom-engine listening udp :%d socket %s", d.udpPort, d.socketPath)

	// Unix socket for QML IPC.
	_ = os.Remove(d.socketPath)
	ul, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", d.socketPath, err)
	}
	defer func() { ul.Close(); os.Remove(d.socketPath) }()
	_ = os.Chmod(d.socketPath, 0600)

	if d.tailnetOnly {
		log.Printf("tailnet-only: LAN broadcast off, non-Tailscale sources dropped")
	}
	go d.broadcastLoop()
	go d.udpReadLoop()
	go d.reapLoop()
	go d.tailnetLoop()
	go d.serveUnix(ul)

	select {}
}

func (d *daemon) broadcastLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		_ = d.broadcastHello()
	}
}

func (d *daemon) broadcastHello() error {
	d.mu.Lock()
	available := d.available
	d.mu.Unlock()
	if !available {
		return nil
	}
	// LAN broadcast, plus a unicast to every tailnet and static peer —
	// broadcast does not cross a tunnel, so those need addressing directly.
	var addrs []string
	if !d.tailnetOnly {
		addrs = broadcastAddrs()
	}
	addrs = append(addrs, d.targets.unicast()...)
	// Hello carries call-sign and current talking state so peers can show
	// who is on air even if an on-air edge packet was lost (UDP).
	talkByte := byte('0')
	d.mu.Lock()
	if d.talking {
		talkByte = '1'
	}
	cs := d.callSign
	d.mu.Unlock()
	msg := append([]byte(helloMagic+"|"), []byte(cs+"|")...)
	msg = append(msg, talkByte)
	d.sendTo(addrs, msg)
	return nil
}

// sendTo writes one payload to each address, best-effort with a deadline —
// an unreachable peer must not stall the ones behind it.
func (d *daemon) sendTo(addrs []string, msg []byte) {
	port := strconv.Itoa(d.udpPort)
	for _, a := range addrs {
		target := a
		// A bare address gets the discovery port; "host:1234" and
		// "[v6]:1234" are left as the user wrote them.
		if _, _, err := net.SplitHostPort(target); err != nil {
			target = net.JoinHostPort(a, port)
		}
		raddr, err := net.ResolveUDPAddr("udp", target)
		if err != nil {
			continue
		}
		d.udpConn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = d.udpConn.WriteToUDP(msg, raddr)
	}
}

// tailnetLoop keeps the tailnet peer list fresh. Nodes come and go, and a
// peer that just came online should be reachable within a poll.
func (d *daemon) tailnetLoop() {
	d.targets.refreshTailnet()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		d.targets.refreshTailnet()
	}
}

func (d *daemon) udpReadLoop() {
	buf := make([]byte, maxUDP)
	for {
		n, raddr, err := d.udpConn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("udp read: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if n == 0 || n > maxUDP {
			continue
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		d.handleUDP(payload, raddr)
	}
}

func (d *daemon) handleUDP(payload []byte, raddr *net.UDPAddr) {
	// Everything below this point costs work, so the cheap rejections come
	// first: wrong network, then too fast, then switched off.
	if d.tailnetOnly && !isTailnetAddr(raddr.IP) {
		return
	}
	ip := raddr.IP.String()
	if !d.limiter.allow(ip) {
		return
	}
	d.mu.Lock()
	available := d.available
	d.mu.Unlock()
	if !available {
		return
	}
	s := string(payload)
	now := time.Now()
	switch {
	case s == helloMagic:
		// Legacy peer without a call-sign — still counts as joined.
		d.mu.Lock()
		if p, ok := d.peers[ip]; ok {
			p.LastSeen = now
		} else if len(d.peers) < maxPeers {
			d.peers[ip] = &peer{IP: ip, LastSeen: now}
		}
		d.mu.Unlock()
	case strings.HasPrefix(s, helloMagic+"|"):
		// omacom-hello-v1|<call-sign>|<talking 0|1>
		fields := strings.SplitN(s[len(helloMagic)+1:], "|", 2)
		cs := sanitizeCallSign(fields[0])
		talking := len(fields) == 2 && fields[1] == "1"
		d.mu.Lock()
		p, ok := d.peers[ip]
		if !ok {
			if len(d.peers) >= maxPeers {
				d.mu.Unlock()
				return
			}
			p = &peer{IP: ip}
			d.peers[ip] = p
		}
		p.CallSign = cs
		p.LastSeen = now
		if talking {
			p.LastTalk = now
		}
		d.mu.Unlock()
		d.resolveCallSignClash(cs, raddr.IP)
	case strings.HasPrefix(s, onAirMagic):
		cs := sanitizeCallSign(s[len(onAirMagic):])
		d.mu.Lock()
		p, ok := d.peers[ip]
		if !ok {
			if len(d.peers) >= maxPeers {
				d.mu.Unlock()
				return
			}
			p = &peer{IP: ip}
			d.peers[ip] = p
		}
		p.CallSign = cs
		p.LastSeen = now
		p.LastTalk = now
		d.mu.Unlock()
	case strings.HasPrefix(s, offAirMagic):
		cs := sanitizeCallSign(s[len(offAirMagic):])
		d.mu.Lock()
		// Clear by IP and by call-sign in case the peer's address changed.
		for _, p := range d.peers {
			if p.IP == ip || (cs != "" && p.CallSign == cs) {
				p.LastTalk = time.Time{}
			}
		}
		d.mu.Unlock()
	default:
		if !strings.HasPrefix(s, audioMagic) {
			return
		}
		frame := payload[len(audioMagic):]
		if len(frame) == 0 || len(frame) > frameBytes {
			return
		}
		d.mu.Lock()
		talking := d.talking
		p, joined := d.peers[ip]
		if joined {
			// Audio is itself an on-air claim, which keeps the roster
			// honest when an edge packet is lost.
			p.LastTalk = now
			p.LastSeen = now
		}
		d.mu.Unlock()
		// An address that has not said hello does not get to use the
		// speakers. Without this, one forged packet is enough.
		if !joined || talking {
			return
		}
		d.sinks.push(ip, frame)
	}
}

func (d *daemon) reapLoop() {
	// Sinks are reaped on a shorter beat than peers: a released PTT should
	// free the audio device promptly, while a peer is allowed to miss a
	// couple of hellos before dropping off the roster.
	sinkTick := time.NewTicker(500 * time.Millisecond)
	defer sinkTick.Stop()
	peerTick := time.NewTicker(5 * time.Second)
	defer peerTick.Stop()
	for {
		select {
		case <-sinkTick.C:
			d.sinks.reap()
		case <-peerTick.C:
			cutoff := time.Now().Add(-10 * time.Second)
			d.mu.Lock()
			for ip, p := range d.peers {
				if p.LastSeen.Before(cutoff) {
					delete(d.peers, ip)
				}
			}
			d.mu.Unlock()
			d.limiter.reap()
		}
	}
}

func (d *daemon) serveUnix(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			log.Printf("unix accept: %v", err)
			continue
		}
		go d.handleConn(c)
	}
}

func (d *daemon) handleConn(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(c)
	// Bounded scanner — max 4k per line.
	sc.Buffer(make([]byte, 4096), 4096)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			d.reply(c, map[string]string{"error": "bad json"})
			continue
		}
		op, _ := req["op"].(string)
		switch op {
		case "startTalking":
			d.mu.Lock()
			if !d.talking && d.available {
				d.talking = true
				go d.startCapture()
				go d.announceTalk(true)
			}
			d.mu.Unlock()
			d.replyPeers(c)
		case "stopTalking":
			d.mu.Lock()
			wasTalking := d.talking
			d.talking = false
			cmd := d.audioCmd
			d.audioCmd = nil
			d.mu.Unlock()
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			if wasTalking {
				go d.announceTalk(false)
			}
			d.replyPeers(c)
		case "setAvailable":
			// Accept bool or string JSON values — fmt.Sprint renders both
			v := strings.ToLower(strings.TrimSpace(fmt.Sprint(req["value"])))
			on := v == "true" || v == "1" || v == "on"
			d.mu.Lock()
			d.available = on
			if !on {
				wasTalking := d.talking
				d.talking = false
				// Clear peers so UI shows 0 while unavailable, and stop announcing
				d.peers = make(map[string]*peer)
				cmd := d.audioCmd
				d.audioCmd = nil
				d.mu.Unlock()
				if cmd != nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				d.sinks.stopAll()
				if wasTalking {
					go d.announceTalk(false)
				}
			} else {
				d.mu.Unlock()
			}
			d.replyPeers(c)
		case "setCallSign":
			// Empty or absent value means "roll me a new one" — the panel's
			// dice button. Only a string is a name; anything else is a typo.
			raw, _ := req["value"].(string)
			d.setCallSign(sanitizeCallSign(raw))
			d.replyPeers(c)
		case "getPeers", "state":
			d.replyPeers(c)
		default:
			d.reply(c, map[string]string{"error": "unknown op"})
		}
	}
}

func (d *daemon) reply(c net.Conn, v any) {
	b, _ := json.Marshal(v)
	_, _ = c.Write(append(b, '\n'))
}

// setCallSign renames this station. A name the user typed is sticky: it is
// persisted and never re-rolled behind their back. An empty name rolls a
// fresh word (avoiding the ones already on the net) and stays auto. Either
// way peers hear about it on the next hello, which we send immediately so
// the roster does not lag by up to 2s.
func (d *daemon) setCallSign(want string) {
	d.mu.Lock()
	if want == "" {
		taken := make(map[string]bool, len(d.peers))
		for _, p := range d.peers {
			if p.CallSign != "" {
				taken[strings.ToLower(p.CallSign)] = true
			}
		}
		taken[strings.ToLower(d.callSign)] = true
		want = randomCallSign(taken)
		d.callSignAuto = true
	} else {
		d.callSignAuto = false
	}
	if want == d.callSign {
		d.mu.Unlock()
		return
	}
	d.callSign = want
	talking := d.talking
	d.mu.Unlock()

	saveCallSign(want)
	log.Printf("call-sign is now %q", want)
	go func() {
		_ = d.broadcastHello()
		// Re-assert on-air identity so a mid-transmission rename does not
		// leave the old handle lit on other rosters.
		if talking {
			d.announceTalk(true)
		}
	}()
}

// resolveCallSignClash re-rolls when another station is already using our
// auto-assigned word. Only auto call-signs move — a name the user chose is
// theirs to keep, clash or not.
//
// A hello from one of our own addresses is never a clash. Restarts overlap:
// a daemon that is shutting down can still be broadcasting the name the
// daemon replacing it just loaded from disk, and re-rolling on that would
// rename the station every time the plugin reloads.
func (d *daemon) resolveCallSignClash(theirs string, src net.IP) {
	if theirs == "" || isLocalAddr(src) {
		return
	}
	d.mu.Lock()
	clash := d.callSignAuto && strings.EqualFold(theirs, d.callSign)
	d.mu.Unlock()
	if !clash {
		return
	}
	log.Printf("call-sign %q is taken on this net — re-rolling", theirs)
	d.setCallSign("")
}

// announceTalk broadcasts a PTT edge 3x — UDP is lossy and the edge is what
// lets peers show your call-sign immediately instead of at next hello.
func (d *daemon) announceTalk(on bool) {
	d.mu.Lock()
	cs := d.callSign
	d.mu.Unlock()
	magic := offAirMagic
	if on {
		magic = onAirMagic
	}
	msg := []byte(magic + cs)
	for i := 0; i < 3; i++ {
		d.sendToPeers(msg)
		time.Sleep(150 * time.Millisecond)
	}
}

func (d *daemon) replyPeers(c net.Conn) {
	d.mu.Lock()
	now := time.Now()
	peers := make([]peer, 0, len(d.peers))
	talkers := make([]string, 0, 2)
	seen := make(map[string]bool)
	for _, p := range d.peers {
		peers = append(peers, peer{IP: p.IP, CallSign: p.CallSign, LastSeen: p.LastSeen})
		if now.Sub(p.LastTalk) < talkHold {
			name := p.CallSign
			if name == "" {
				name = p.IP
			}
			if !seen[name] {
				seen[name] = true
				talkers = append(talkers, name)
			}
		}
	}
	talking := d.talking
	available := d.available
	callSign := d.callSign
	d.mu.Unlock()
	tailnet := d.targets.unicast()
	d.reply(c, map[string]any{
		"peers":        peers,
		"talkers":      talkers,
		"talking":      talking,
		"available":    available,
		"callSign":     callSign,
		"tailnetOnly":  d.tailnetOnly,
		"directTarget": len(tailnet),
	})
}

func (d *daemon) startCapture() {
	// Capture mono s16le 48k from PipeWire and stream as UDP audio packets.
	cmd := captureCommand()
	if cmd == nil {
		log.Printf("no capture tool (pw-cat/parec/arecord) — PTT no-op")
		d.mu.Lock()
		d.talking = false
		d.mu.Unlock()
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("capture stdout: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("capture start %v: %v", cmd.Path, err)
		return
	}
	d.mu.Lock()
	d.audioCmd = cmd
	d.mu.Unlock()
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		d.mu.Lock()
		d.audioCmd = nil
		d.talking = false
		d.mu.Unlock()
	}()
	// One whole 20ms frame per packet. A partial read would put a fragment
	// on the wire that the receiver has no way to reassemble, so read until
	// the frame is full; stopTalking kills the process, which unblocks this.
	buf := make([]byte, frameBytes)
	for {
		d.mu.Lock()
		talking := d.talking
		d.mu.Unlock()
		if !talking {
			return
		}
		if _, err := io.ReadFull(stdout, buf); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("capture read: %v", err)
			}
			return
		}
		payload := make([]byte, 0, len(audioMagic)+frameBytes)
		payload = append(payload, audioMagic...)
		payload = append(payload, buf...)
		d.sendToPeers(payload)
	}
}

func (d *daemon) sendToPeers(payload []byte) {
	d.mu.Lock()
	peers := make([]string, 0, len(d.peers))
	for ip := range d.peers {
		peers = append(peers, ip)
	}
	d.mu.Unlock()
	for _, ip := range peers {
		raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip, d.udpPort))
		if err != nil {
			continue
		}
		d.udpConn.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
		_, _ = d.udpConn.WriteToUDP(payload, raddr)
	}
}
