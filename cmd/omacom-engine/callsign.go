package main

// Call-sign assignment and persistence.
//
// Every station needs a human-readable handle. Rather than leaking the
// hostname onto the LAN by default, a station that has never been named
// picks a random word from the list below and keeps it — the same machine
// answers to the same call-sign across restarts.
//
// Precedence, highest first:
//   1. --callsign flag (set by the shell from plugin settings)
//   2. the persisted state file ($XDG_STATE_HOME/omacom/callsign)
//   3. a freshly rolled random word, written to the state file
//
// The state file is plain text, one line, 0600. It is the only thing the
// daemon writes to disk.

import (
	"crypto/rand"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// callSignWords — short, phonetically distinct, unambiguous over a speaker.
// Aviation/weather/mineral flavour so a roster reads like a radio net.
var callSignWords = []string{
	"Alder", "Amber", "Anchor", "Arrow", "Aspen", "Atlas", "Aurora", "Avalon",
	"Badger", "Basalt", "Beacon", "Birch", "Bishop", "Blaze", "Bolt", "Bramble",
	"Bravo", "Breaker", "Bronze", "Canyon", "Cedar", "Cinder", "Cobalt", "Comet",
	"Compass", "Condor", "Copper", "Coral", "Cortex", "Cosmos", "Cypress", "Dagger",
	"Delta", "Dingo", "Domino", "Drifter", "Dusk", "Eagle", "Ember", "Falcon",
	"Fathom", "Ferrous", "Fjord", "Flint", "Forge", "Fox", "Gale", "Gambit",
	"Garnet", "Geyser", "Glacier", "Granite", "Griffin", "Gully", "Harbor", "Hawk",
	"Heron", "Hollow", "Hornet", "Indigo", "Ion", "Iris", "Ivory", "Jackal",
	"Jasper", "Jetty", "Juniper", "Kestrel", "Kite", "Krypton", "Lancer", "Lantern",
	"Larch", "Lattice", "Lichen", "Lima", "Lynx", "Magnet", "Mammoth", "Maple",
	"Marlin", "Meridian", "Mesa", "Meteor", "Mirage", "Monsoon", "Mustang", "Nebula",
	"Nickel", "Nimbus", "Nomad", "Nova", "Oakum", "Obsidian", "Onyx", "Opal",
	"Orbit", "Orchid", "Osprey", "Otter", "Outrun", "Pacer", "Panther", "Papyrus",
	"Pebble", "Peregrine", "Phoenix", "Pilot", "Pine", "Piper", "Pivot", "Plume",
	"Prairie", "Prism", "Puffin", "Quarry", "Quartz", "Quasar", "Quiver", "Radar",
	"Rampart", "Raven", "Reef", "Relay", "Ridge", "Rocket", "Rover", "Rowan",
	"Sable", "Saffron", "Salvo", "Sandbar", "Sapphire", "Scout", "Sentry", "Shadow",
	"Sierra", "Silo", "Sirius", "Slate", "Sonar", "Spruce", "Squall", "Stellar",
	"Sterling", "Stratus", "Summit", "Talon", "Tandem", "Tangent", "Tempest", "Thistle",
	"Thunder", "Tidal", "Timber", "Titan", "Topaz", "Torrent", "Trellis", "Tundra",
	"Tusk", "Umbra", "Vagrant", "Valor", "Vector", "Verdant", "Vertex", "Vesper",
	"Viper", "Vista", "Voyager", "Walrus", "Warden", "Whistle", "Willow", "Wolfram",
	"Wren", "Zenith", "Zephyr", "Zodiac",
}

// randomInt returns a uniform value in [0,n) from crypto/rand, falling back
// to 0 rather than panicking — a predictable call-sign beats a dead daemon.
func randomInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// randomCallSign rolls a fresh word, avoiding anything in `taken` when it
// can. Once every word is spoken for it appends a two-digit suffix, so two
// stations on a busy net still end up distinguishable.
func randomCallSign(taken map[string]bool) string {
	for i := 0; i < 24; i++ {
		w := callSignWords[randomInt(len(callSignWords))]
		if !taken[strings.ToLower(w)] {
			return w
		}
	}
	w := callSignWords[randomInt(len(callSignWords))]
	return w + "-" + string(rune('0'+randomInt(10))) + string(rune('0'+randomInt(10)))
}

// callSignStatePath is where an auto-assigned call-sign is remembered.
func callSignStatePath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "omacom", "callsign")
}

// loadCallSign reads the persisted call-sign, or "" if there is none.
func loadCallSign() string {
	p := callSignStatePath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return sanitizeCallSign(string(b))
}

// saveCallSign persists a call-sign so the same machine keeps it across
// restarts. Best-effort: a read-only state dir is not worth failing over.
func saveCallSign(cs string) {
	p := callSignStatePath()
	if p == "" || cs == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(cs+"\n"), 0600)
}
