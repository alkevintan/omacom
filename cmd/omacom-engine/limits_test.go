package main

import (
	"fmt"
	"net"
	"testing"
)

// A flood from one address must be cut down to roughly the burst size: the
// bucket only refills with elapsed time, and these sends take almost none.
func TestLimiterCutsFloodToBurst(t *testing.T) {
	l := newLimiter()
	allowed := 0
	for i := 0; i < 10000; i++ {
		if l.allow("10.0.0.1") {
			allowed++
		}
	}
	if allowed > packetBurst+50 {
		t.Fatalf("flood of 10000 let %d through, want <= %d", allowed, packetBurst+50)
	}
	if allowed < packetBurst/2 {
		t.Fatalf("flood of 10000 let only %d through, want a real talker to fit", allowed)
	}
}

// A talker's own traffic — 50 audio frames a second plus hellos — must never
// be limited, or PTT would stutter.
func TestLimiterPassesOneTalker(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 60; i++ {
		if !l.allow("10.0.0.2") {
			t.Fatalf("dropped packet %d of a single talker's second", i)
		}
	}
}

// Spoofed sources must not grow the bucket table without bound; past the cap
// the limiter fails closed for addresses it has not seen before.
func TestLimiterCapsDistinctSources(t *testing.T) {
	l := newLimiter()
	for i := 0; i < maxSources; i++ {
		if !l.allow(fmt.Sprintf("10.1.%d.%d", i/256, i%256)) {
			t.Fatalf("rejected source %d, still under the cap", i)
		}
	}
	if l.allow("203.0.113.9") {
		t.Fatal("accepted a new source past maxSources, want fail-closed")
	}
	// An address already holding a bucket keeps working.
	if !l.allow("10.1.0.0") {
		t.Fatal("dropped an established source once the table filled")
	}
}

func TestIsTailnetAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.101.102.103", true},
		{"100.127.255.254", true},
		{"fd7a:115c:a1e0::1", true},
		{"100.63.255.255", false}, // just below the CGNAT range
		{"100.128.0.1", false},    // just above it
		{"192.168.1.20", false},
		{"127.0.0.1", false},
		{"8.8.8.8", false},
		{"fd00::1", false},
	}
	for _, c := range cases {
		if got := isTailnetAddr(net.ParseIP(c.addr)); got != c.want {
			t.Errorf("isTailnetAddr(%s) = %v, want %v", c.addr, got, c.want)
		}
	}
	if isTailnetAddr(nil) {
		t.Error("isTailnetAddr(nil) = true, want false")
	}
}
