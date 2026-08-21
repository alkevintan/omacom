package main

// Where to send hellos.
//
// Broadcast finds peers on a LAN and nowhere else. It cannot cross Tailscale:
// tailscale0 is a point-to-point interface with no BROADCAST flag, so the
// per-interface loop skips it, and 255.255.255.255 goes out the default route
// instead of the tunnel. Reaching a tailnet means unicasting to each peer.
//
// Two sources of unicast targets:
//   - `tailscale status --json`, refreshed periodically, for online tailnet
//     peers. Zero configuration: if you are on a tailnet, Omacom finds it.
//   - an explicit --peers list, for port-forwarded hosts, a WireGuard mesh
//     that is not Tailscale, or testing without a tailnet at all.
//
// In tailnet-only mode LAN broadcast is skipped entirely and packets from
// outside Tailscale's address ranges are dropped, which is the posture to run
// in on a network you do not trust.

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Tailscale's address space: 100.64.0.0/10 (CGNAT) and fd7a:115c:a1e0::/48.
var (
	tailnetV4 = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}
	tailnetV6 = &net.IPNet{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)}
)

// localAddrs caches this machine's own addresses. A packet from one of them
// is us — an overlapping restart, a second interface, a loopback route — and
// must never join the roster or trigger a call-sign clash against ourselves.
var (
	localOnce  sync.Once
	localAddrs map[string]bool
)

func isLocalAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	localOnce.Do(func() {
		localAddrs = make(map[string]bool)
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				localAddrs[ipnet.IP.String()] = true
			}
		}
	})
	return localAddrs[ip.String()]
}

func isTailnetAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return tailnetV4.Contains(v4)
	}
	return tailnetV6.Contains(ip)
}

type targets struct {
	mu      sync.Mutex
	tailnet []string // 100.x addresses of online tailnet peers
	static  []string // from --peers
	// warned latches the "tailscale not installed" line.
	warned bool
}

func newTargets(static string) *targets {
	t := &targets{}
	for _, raw := range strings.Split(static, ",") {
		host := strings.TrimSpace(raw)
		if host != "" {
			t.static = append(t.static, host)
		}
	}
	return t
}

// unicast returns every address a hello should be sent to directly.
func (t *targets) unicast() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.tailnet)+len(t.static))
	out = append(out, t.static...)
	out = append(out, t.tailnet...)
	return out
}

// tsStatus is the sliver of `tailscale status --json` we need.
type tsStatus struct {
	Peer map[string]struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	} `json:"Peer"`
}

// refreshTailnet asks the local tailscaled for its peers. Missing tailscale
// is not an error — most users are on a LAN — so it logs once and gives up.
func (t *targets) refreshTailnet() {
	if _, err := exec.LookPath("tailscale"); err != nil {
		t.mu.Lock()
		if !t.warned {
			t.warned = true
			log.Printf("tailscale not installed — LAN discovery only")
		}
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		log.Printf("tailscale status: %v", err)
		return
	}
	var st tsStatus
	if err := json.Unmarshal(out, &st); err != nil {
		log.Printf("tailscale status parse: %v", err)
		return
	}
	var addrs []string
	for _, p := range st.Peer {
		if !p.Online {
			continue
		}
		for _, a := range p.TailscaleIPs {
			if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
				addrs = append(addrs, a)
				break // one address per peer is enough
			}
		}
	}
	t.mu.Lock()
	changed := len(addrs) != len(t.tailnet)
	t.tailnet = addrs
	t.mu.Unlock()
	if changed {
		log.Printf("tailnet peers online: %d", len(addrs))
	}
}

// broadcastAddrs computes the per-interface broadcast addresses, plus the
// all-hosts address. LAN only — nothing here reaches a tailnet.
func broadcastAddrs() []string {
	addrs := []string{"255.255.255.255"}
	ifs, err := net.Interfaces()
	if err != nil {
		return addrs
	}
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range ifaceAddrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			mask := net.IP(ipnet.Mask).To4()
			if mask == nil {
				continue
			}
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip[i] | ^mask[i]
			}
			addrs = append(addrs, bcast.String())
		}
	}
	return addrs
}
