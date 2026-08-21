package main

// Audio capture and playback.
//
// Everything on the wire is raw little-endian s16 mono at 48kHz, one 20ms
// frame per UDP packet. Both pw-cat and its fallbacks need to be told that
// explicitly: without --raw, pw-cat wraps the stream in an AU container, so
// the first "frame" of a transmission carries a file header and every frame
// after it is headerless — which is neither a valid file nor valid PCM.
//
// Playback keeps one sink process per talking peer for as long as they are
// talking, rather than one per packet. A process per packet meant ~90 forks
// a second per talker, driven entirely by unauthenticated remote input, and
// it also sounded terrible: each 20ms fragment paid a fresh stream setup.

import (
	"io"
	"log"
	"os/exec"
	"sync"
	"time"
)

const (
	sampleRate = 48000
	channels   = 1
	// 20ms of s16 mono @48k: 960 samples × 2 bytes.
	frameBytes = 960 * 2
	// How long a sink waits for its next frame before shutting down. Long
	// enough to ride out a gap between words, short enough to free the
	// device promptly when someone releases PTT.
	sinkIdle = 1500 * time.Millisecond
	// Frames a sink will hold before dropping. Realtime audio: a full
	// buffer means we are behind, and late audio is worth less than
	// current audio, so the newest frame is dropped rather than queued.
	jitterFrames = 25
	// Ceiling on simultaneous talkers we will open a device for.
	maxSinks = 8
)

// captureCommand builds the recorder, preferring PipeWire's own tool.
func captureCommand() *exec.Cmd {
	if _, err := exec.LookPath("pw-cat"); err == nil {
		return exec.Command("pw-cat", "--record", "--raw",
			"--format", "s16", "--rate", "48000", "--channels", "1", "-")
	}
	if _, err := exec.LookPath("parec"); err == nil {
		return exec.Command("parec", "--raw", "--format=s16le", "--rate=48000", "--channels=1")
	}
	if _, err := exec.LookPath("arecord"); err == nil {
		return exec.Command("arecord", "-f", "S16_LE", "-r", "48000", "-c", "1", "-t", "raw")
	}
	return nil
}

// playbackCommand builds a sink that reads raw PCM from stdin until closed.
func playbackCommand() *exec.Cmd {
	if _, err := exec.LookPath("pw-cat"); err == nil {
		return exec.Command("pw-cat", "--playback", "--raw",
			"--format", "s16", "--rate", "48000", "--channels", "1",
			"--latency", "40ms", "-")
	}
	if _, err := exec.LookPath("paplay"); err == nil {
		return exec.Command("paplay", "--raw", "--format=s16le", "--rate=48000", "--channels=1")
	}
	if _, err := exec.LookPath("aplay"); err == nil {
		return exec.Command("aplay", "-f", "S16_LE", "-r", "48000", "-c", "1", "-t", "raw")
	}
	return nil
}

// sink is one talker's open audio device.
type sink struct {
	frames chan []byte
	done   chan struct{}
	once   sync.Once
}

func (s *sink) stop() {
	s.once.Do(func() { close(s.frames) })
}

// playback routes incoming frames to a per-talker sink. PipeWire mixes the
// streams for us, so several people talking at once needs no mixing here.
type playback struct {
	mu    sync.Mutex
	sinks map[string]*sink
	last  map[string]time.Time
	// noDevice latches once so a machine with no audio tooling logs a
	// single line instead of one per packet.
	noDevice bool
}

func newPlayback() *playback {
	return &playback{
		sinks: make(map[string]*sink),
		last:  make(map[string]time.Time),
	}
}

// push queues one frame for a talker, opening a sink if this is the start of
// their transmission. Never blocks: a full jitter buffer drops the frame.
func (p *playback) push(key string, frame []byte) {
	p.mu.Lock()
	s, ok := p.sinks[key]
	if !ok {
		if len(p.sinks) >= maxSinks || p.noDevice {
			p.mu.Unlock()
			return
		}
		var err error
		s, err = p.openSink(key)
		if err != nil {
			// openSink already logged; latch if there is no tool at all.
			p.mu.Unlock()
			return
		}
		p.sinks[key] = s
	}
	p.last[key] = time.Now()
	p.mu.Unlock()

	select {
	case s.frames <- frame:
	default: // behind — drop this frame rather than add latency
	}
}

// openSink starts a device for one talker. Caller holds p.mu.
func (p *playback) openSink(key string) (*sink, error) {
	cmd := playbackCommand()
	if cmd == nil {
		p.noDevice = true
		log.Printf("no audio playback tool (pw-cat/paplay/aplay) — incoming audio dropped")
		return nil, errNoAudioTool
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("playback stdin: %v", err)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		log.Printf("playback start: %v", err)
		return nil, err
	}
	s := &sink{frames: make(chan []byte, jitterFrames), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		writeFrames(stdin, s.frames)
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	return s, nil
}

// writeFrames drains the jitter buffer into the device until the sink is
// stopped. A write error ends the transmission rather than spinning.
func writeFrames(w io.Writer, frames <-chan []byte) {
	for f := range frames {
		if _, err := w.Write(f); err != nil {
			// Drain what is queued so push() never blocks on a dead sink.
			for range frames {
			}
			return
		}
	}
}

// reap closes sinks whose talker has gone quiet.
func (p *playback) reap() {
	cutoff := time.Now().Add(-sinkIdle)
	p.mu.Lock()
	var stopping []*sink
	for key, s := range p.sinks {
		if p.last[key].Before(cutoff) {
			stopping = append(stopping, s)
			delete(p.sinks, key)
			delete(p.last, key)
		}
	}
	p.mu.Unlock()
	for _, s := range stopping {
		s.stop()
	}
}

// stopAll closes every sink — used when the intercom is switched off.
func (p *playback) stopAll() {
	p.mu.Lock()
	stopping := make([]*sink, 0, len(p.sinks))
	for key, s := range p.sinks {
		stopping = append(stopping, s)
		delete(p.sinks, key)
		delete(p.last, key)
	}
	p.mu.Unlock()
	for _, s := range stopping {
		s.stop()
	}
}

// errNoAudioTool marks a machine with no playback binary at all.
var errNoAudioTool = errNoTool("no audio playback tool available")

type errNoTool string

func (e errNoTool) Error() string { return string(e) }
