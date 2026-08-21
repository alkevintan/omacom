package main

// Inbound abuse limits.
//
// The UDP port is reachable by anyone who can route to it, and every packet
// costs us work — a map write, an audio frame, possibly a device. A token
// bucket per source address bounds that cost, and the bucket table itself is
// bounded so a spoofing flood cannot grow it without limit.

import (
	"sync"
	"time"
)

const (
	// One talker is 50 packets/sec plus a hello every 2s. 120/sec sustained
	// leaves generous headroom while cutting a flood down by orders of
	// magnitude.
	packetsPerSecond = 120
	packetBurst      = 240
	// Distinct source addresses tracked. Past this the limiter fails closed
	// for addresses it has not seen, which is the safe direction: a flood
	// from thousands of forged sources gets dropped, established peers keep
	// their buckets.
	maxSources = 512
	// Peers we will hold in the roster. A hello from a new address past
	// this is ignored.
	maxPeers = 64
)

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
}

func newLimiter() *limiter {
	return &limiter{buckets: make(map[string]*bucket), rate: packetsPerSecond, burst: packetBurst}
}

// allow reports whether a packet from key may be processed, refilling that
// source's bucket for the time elapsed since its last packet.
func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxSources {
			return false
		}
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// reap drops buckets for sources that have gone quiet, so the table tracks
// current traffic rather than everything ever seen.
func (l *limiter) reap() {
	cutoff := time.Now().Add(-60 * time.Second)
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
}
