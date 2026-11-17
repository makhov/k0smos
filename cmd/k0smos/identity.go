package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// identityFrom derives a machine's stable identity from whatever the hardware
// offers, hex encoded. The sources are tried in order and the first that says
// anything wins; "" when none of them do.
//
// Hashed rather than used verbatim, so that a serial number or a MAC is not
// published in a node's name, and so that every source yields the same shape.
// Prefixed with the source, so two machines cannot collide by one's UUID
// happening to equal another's MAC.
func identityFrom(sources ...[2]string) string {
	for _, s := range sources {
		v := strings.TrimSpace(s[1])
		if v == "" || strings.Trim(v, "0:-") == "" {
			continue // absent, or the all-zero address a device with none reports
		}
		sum := sha256.Sum256([]byte(s[0] + ":" + v))
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// machineIdentity is this machine's stable identity.
//
// Derived rather than stored, so it is the same on every boot of the same
// hardware and survives a data volume that was wiped or never existed. DMI's
// product UUID first, which is what a cloud and most server firmware give each
// machine; a network card's MAC otherwise, which a machine without DMI still
// has.
func machineIdentity() string {
	uuid, _ := os.ReadFile("/sys/class/dmi/id/product_uuid")
	return identityFrom(
		[2]string{"dmi", string(uuid)},
		[2]string{"mac", firstMAC("/sys/class/net")},
	)
}

// firstMAC is the address of this machine's first network device, preferring
// one that is real hardware: a node's physical cards keep their addresses
// across reboots, while bridges and veths that a CNI creates do not -- and
// those do not exist this early anyway. Deterministic, because the identity
// derived from it has to be the same on every boot.
func firstMAC(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name() != "lo" {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	var fallback string
	for _, n := range names {
		addr, err := os.ReadFile(filepath.Join(dir, n, "address"))
		if err != nil || strings.TrimSpace(string(addr)) == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, n, "device")); err == nil {
			return string(addr) // a real card, which is what we want
		}
		if fallback == "" {
			fallback = string(addr)
		}
	}
	return fallback
}

// hostnameFor is the name a machine calls itself when nothing else named it.
//
// Every node answering to "k0smos" is the problem this solves: a node's name
// in Kubernetes is its hostname, so two machines booting the same image with
// no configuration fight over one Node object, and the second quietly takes
// the first's place. Short enough to read, long enough that machines on one
// cluster do not collide, and a valid DNS label either way.
func hostnameFor(identity string) string {
	if identity == "" {
		return "k0smos"
	}
	return "k0smos-" + identity[:12]
}

// machineIDFor is the content of /etc/machine-id: 32 hex digits and a newline,
// as every reader of that file expects. kubelet reports it as the node's
// machineID, and an empty one is what k0smos reported before.
func machineIDFor(identity string) string {
	if identity == "" {
		return ""
	}
	return identity[:32] + "\n"
}
