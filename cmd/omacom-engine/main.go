package main

// omacom-engine — stub daemon for Omacom 0.1.0 MVP.
//
// The real audio path (PipeWire capture → Opus encode → UDP broadcast on
// :53318, and the reverse) will live here. For competition 0.1.0 we keep it
// minimal so `omarchy plugin validate` + review is clean and the bar widget
// can demo without hardware.
//
// Build from the checkout's source so the running binary is auditable from
// the reviewed commit — see bin/omacom-setup.

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
)

func main() {
	var (
		socket = flag.String("socket", "", "unix socket path (default $XDG_RUNTIME_DIR/omacom.sock)")
		port   = flag.Int("port", 53318, "discovery/audio UDP port")
	)
	flag.Parse()
	if *socket == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			*socket = rt + "/omacom.sock"
		} else {
			*socket = "/tmp/omacom.sock"
		}
	}
	// Minimal bind to prove the port is usable — no broadcast yet in MVP.
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("resolve :%d: %v", *port, err)
	}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("listen :%d: %v", *port, err)
	}
	defer c.Close()
	log.Printf("omacom-engine stub listening on %s (socket %s) — hold PTT in bar to talk", c.LocalAddr(), *socket)
	// Keep alive until killed — real loop will handle PTT + Opus here.
	select {}
}
