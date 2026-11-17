package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// identityFile is where a machine keeps its identity, under the data volume.
// On the volume rather than the root because the root is read-only and the
// same bytes on every machine.
const identityFile = "lib/k0smos/node-identity"

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

// Identity is a machine's stable identity, and how it came by one.
type Identity struct {
	Value  string
	Source string
}

// readIdentity returns the identity kept on the data volume at dir, or "" when
// there is none to read. A file that holds something other than an identity is
// reported and replaced: it is not an identity, so keeping it would only mean
// refusing to have one.
func readIdentity(dir string) string {
	path := filepath.Join(dir, identityFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if _, err := hex.DecodeString(id); err != nil || len(id) != 64 {
		return ""
	}
	return id
}

// newIdentity takes a random identity and keeps it at dir, returning it only
// once it is safely on disk: an identity that was not stored would be a
// different one on the next boot, and a node that renames itself every boot
// leaves a stale Node object behind each time.
func newIdentity(dir string, mkdirAll func(string, os.FileMode) error, writeFile func(string, []byte, os.FileMode) error) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	id := hex.EncodeToString(buf)
	path := filepath.Join(dir, identityFile)
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeFile(path, []byte(id+"\n"), 0o400); err != nil {
		return "", err
	}
	return id, nil
}

// derivedIdentity is what the hardware says this machine is: DMI's product
// UUID where the firmware offers one, a network card's MAC otherwise.
//
// The fallback for a machine with nowhere to keep an identity, and the reason
// it is not the first choice: a shared DMI uuid is common on cheap boards, a
// hypervisor may hand every guest the same MAC, and replacing a network card
// would otherwise rename the node.
func derivedIdentity() string {
	uuid, _ := os.ReadFile("/sys/class/dmi/id/product_uuid")
	return identityFrom(
		[2]string{"dmi", string(uuid)},
		[2]string{"mac", firstMAC("/sys/class/net")},
	)
}

// machineIdentity is this machine's identity: the one kept on its data volume,
// a new one kept there now, or -- with nowhere to keep one -- whatever the
// hardware says it is.
func machineIdentity(dir string, mkdirAll func(string, os.FileMode) error, writeFile func(string, []byte, os.FileMode) error) Identity {
	if id := readIdentity(dir); id != "" {
		return Identity{id, "kept"}
	}
	id, err := newIdentity(dir, mkdirAll, writeFile)
	if err == nil {
		return Identity{id, "new"}
	}
	if derived := derivedIdentity(); derived != "" {
		return Identity{derived, fmt.Sprintf("from this machine's hardware (%v)", err)}
	}
	return Identity{"", fmt.Sprintf("none: %v, and the hardware offers none either", err)}
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
