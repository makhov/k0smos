package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Guests are identified by name, and each one's runtime state — console log,
// control socket, metadata — lives in its own directory.
//
// Not in dist/: that is build output, and mixing a running machine's state into it
// means `make clean-dist` deletes the way to reach a live guest, and a second guest
// has nowhere to go. A per-user state directory follows the XDG convention, so
// nothing is written into the working tree at all.
const (
	defaultGuestName = "default"

	// A unix socket path cannot exceed roughly this on macOS (104 including the
	// terminator; Linux allows 108). Long names would otherwise fail inside QEMU
	// with an unhelpful error, so it is checked up front.
	maxSocketPath = 100
)

// guest state file names.
const (
	consoleFile = "console.log"
	socketFile  = "control.sock"
	metaFile    = "guest.json"
)

// guestMeta is what machine up records so the other subcommands, and a later
// `machine list`, can
// describe a guest without being told about it again.
type guestMeta struct {
	Name    string    `json:"name"`
	PID     int       `json:"pid"`
	Disk    string    `json:"disk,omitempty"`
	APIPort int       `json:"apiPort,omitempty"`
	Started time.Time `json:"started"`
}

// stateRoot is the directory holding every guest's state.
func stateRoot() (string, error) {
	if dir := os.Getenv("K0SMOS_STATE_DIR"); dir != "" {
		return dir, nil
	}
	// XDG_STATE_HOME is the right home for data that should persist between runs
	// but is not configuration and not a cache.
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "k0smos"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "k0smos"), nil
}

// guestDir returns the state directory for a named guest without creating it:
// reading a guest's logs must not bring a guest into existence, which is how a
// mistyped --name once turned up in `machine list`.
func guestDir(name string) (string, error) {
	if err := validGuestName(name); err != nil {
		return "", err
	}
	root, err := stateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// ensureGuestDir creates the state directory. Only machine up does this.
func ensureGuestDir(name string) (string, error) {
	dir, err := guestDir(name)
	if err != nil {
		return "", err
	}
	// 0700: the directory holds a control socket that grants cluster-admin.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// guestPaths returns where a named guest keeps its console, socket and metadata.
//
// The socket's length is deliberately not checked here: `logs` wants only the
// console, and failing it with a complaint about socket paths — which is what
// happened — explains nothing about what it was asked to do.
func guestPaths(name string) (console, socket, meta string, err error) {
	dir, err := guestDir(name)
	if err != nil {
		return "", "", "", err
	}
	return filepath.Join(dir, consoleFile), filepath.Join(dir, socketFile), filepath.Join(dir, metaFile), nil
}

// checkSocketPath is called by the commands that actually open the socket. A path
// over the kernel's limit otherwise fails inside QEMU, or as a bare ENOENT from
// dial, with nothing pointing at the length as the cause.
func checkSocketPath(socket string) error {
	if len(socket) > maxSocketPath {
		return fmt.Errorf(
			"the control socket path is %d characters, over the %d a unix socket allows: "+
				"use a shorter --name, or set K0SMOS_STATE_DIR to a shorter path",
			len(socket), maxSocketPath)
	}
	return nil
}

// validGuestName keeps a name usable as a single directory component.
func validGuestName(name string) error {
	if name == "" {
		return fmt.Errorf("empty guest name")
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return fmt.Errorf("guest name %q must be a single path component", name)
	}
	return nil
}

func saveMeta(path string, m guestMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func loadMeta(path string) (guestMeta, error) {
	var m guestMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}

// listGuests returns every guest with state on disk, newest first.
func listGuests() ([]guestMeta, error) {
	root, err := stateRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing has ever been booted
		}
		return nil, err
	}
	var out []guestMeta
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, err := loadMeta(filepath.Join(root, e.Name(), metaFile))
		if err != nil {
			// A directory with no readable metadata is still a guest that was
			// booted at some point; report what is known rather than hiding it.
			m = guestMeta{Name: e.Name()}
		}
		m.Name = e.Name()
		out = append(out, m)
	}
	return out, nil
}

// A guest's own images. The root is a copy only when booting from a disk; with the
// root carried in the initramfs, which is the default, only the data volume exists.
const (
	rootFile     = "root.img"
	artifactFile = "machine.qcow2"
	dataFile     = "data.img"
)

// guestData returns the guest's data volume, creating it sparse if absent.
//
// Every guest needs one: the root is read-only, so /var — k0s's state, kubelet's,
// containerd's images — has nowhere else to live. It is per-guest and persists
// across reboots of that guest, and `k0smosctl machine rm` discards it.
func guestData(name, size string) (string, error) {
	dir, err := guestDir(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, dataFile)
	if err := ensureDataVolume(path, size); err != nil {
		return "", err
	}
	return path, nil
}

// guestDisk returns the guest's root disk, cloning it from the image on first use.
//
// The image is a template and must stay one: booting it directly would allow only
// one guest per machine, and any clone taken afterwards would carry that guest's
// PKI — identical CA and node UID — since k0s generates it on first boot.
func guestDisk(name, image string) (string, error) {
	return cloneGuestDisk(name, image, rootFile, "root image", "build it with `make disk`, or point at one with --image")
}

// guestArtifact clones the complete firmware-bootable platform artifact. It has
// a different state filename from the legacy ext4 root: reusing root.img from a
// previous direct-kernel guest as qcow2 would make QEMU reject or corrupt it.
func guestArtifact(name, image string) (string, error) {
	return cloneGuestDisk(name, image, artifactFile, "platform artifact", "build it with `make metal`, or point at one with --image")
}

func cloneGuestDisk(name, image, filename, what, hint string) (string, error) {
	dir, err := guestDir(name)
	if err != nil {
		return "", err
	}
	disk := filepath.Join(dir, filename)
	if _, err := os.Stat(disk); err == nil {
		return disk, nil // reuse it: a reboot should keep the cluster
	}
	if _, err := os.Stat(image); err != nil {
		return "", fmt.Errorf("%s %s not found — %s", what, image, hint)
	}
	if err := cloneFile(image, disk); err != nil {
		return "", fmt.Errorf("clone %s: %w", image, err)
	}
	return disk, nil
}

// cloneFile copies a file, preferring a copy-on-write clone where the filesystem
// offers one. `cp -c` is instant on APFS and the images are several gigabytes
// apparent, so the difference is minutes.
func cloneFile(src, dst string) error {
	if err := exec.Command("cp", "-c", src, dst).Run(); err == nil {
		return nil
	}
	out, err := exec.Command("cp", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
