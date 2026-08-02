package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateIn points the state directory at a temporary one for the duration of a test.
//
// Under /tmp rather than t.TempDir(): a macOS temp path is around 60 characters on
// its own, which leaves no room under the unix socket limit once a guest name and
// "control.sock" are appended.
func stateIn(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "k0s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("K0SMOS_STATE_DIR", dir)
	return dir
}

// Reading a guest's state must not bring the guest into existence. Creating the
// directory on any lookup meant a mistyped --name appeared in `machine list` as a stopped
// guest that had never been booted.
func TestGuestPathsDoNotCreateAnything(t *testing.T) {
	root := stateIn(t)
	if _, _, _, err := guestPaths("ghost"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ghost")); !os.IsNotExist(err) {
		t.Error("looking up a guest's paths created its state directory")
	}

	guests, err := listGuests()
	if err != nil {
		t.Fatal(err)
	}
	if len(guests) != 0 {
		t.Errorf("list reports %v after a lookup alone", guests)
	}
}

// The root image is a template. Booting it in place would allow only one guest per
// machine, and — because k0s writes its PKI on first boot — every later clone would
// carry the first guest's cluster identity.
func TestGuestDiskClonesTheImageOnce(t *testing.T) {
	stateIn(t)
	image := filepath.Join(t.TempDir(), "k0smos.img")
	if err := os.WriteFile(image, []byte("pristine template"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGuestDir("vm"); err != nil {
		t.Fatal(err)
	}

	disk, err := guestDisk("vm", image)
	if err != nil {
		t.Fatal(err)
	}
	if disk == image {
		t.Fatal("the guest was pointed at the image itself")
	}
	if got, _ := os.ReadFile(disk); string(got) != "pristine template" {
		t.Errorf("clone = %q, want the image's contents", got)
	}

	// Whatever the guest writes must not reach the template.
	if err := os.WriteFile(disk, []byte("cluster state"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(image); string(got) != "pristine template" {
		t.Errorf("the image was modified: %q", got)
	}

	// A second boot reuses the disk, so a reboot keeps the cluster.
	again, err := guestDisk("vm", image)
	if err != nil {
		t.Fatal(err)
	}
	if again != disk {
		t.Errorf("second boot used %s, want %s", again, disk)
	}
	if got, _ := os.ReadFile(again); string(got) != "cluster state" {
		t.Errorf("reboot discarded the guest's disk: %q", got)
	}
}

func TestGuestDiskReportsAMissingImage(t *testing.T) {
	stateIn(t)
	if _, err := ensureGuestDir("vm"); err != nil {
		t.Fatal(err)
	}
	_, err := guestDisk("vm", filepath.Join(t.TempDir(), "absent.img"))
	if err == nil {
		t.Fatal("accepted an image that does not exist")
	}
	if !strings.Contains(err.Error(), "make disk") {
		t.Errorf("error = %v, want it to say how to build one", err)
	}
}

func TestGuestArtifactUsesASeparateQCOW2StateDisk(t *testing.T) {
	stateIn(t)
	image := filepath.Join(t.TempDir(), "k0smos-metal-x86_64.qcow2")
	if err := os.WriteFile(image, []byte("firmware artifact"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureGuestDir("vm"); err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy direct-kernel guest. Artifact mode must never reuse it.
	legacy, err := guestDisk("vm", image)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("raw ext4 state"), 0644); err != nil {
		t.Fatal(err)
	}

	disk, err := guestArtifact("vm", image)
	if err != nil {
		t.Fatal(err)
	}
	if disk == legacy || filepath.Base(disk) != artifactFile {
		t.Fatalf("artifact disk = %s, legacy disk = %s", disk, legacy)
	}
	if got, _ := os.ReadFile(disk); string(got) != "firmware artifact" {
		t.Errorf("artifact clone = %q", got)
	}
}

// A unix socket path is capped by the kernel, and exceeding it fails inside QEMU
// with nothing pointing at the cause.
func TestOverlongSocketPathIsRejectedWhereItIsUsed(t *testing.T) {
	long := filepath.Join("/tmp", strings.Repeat("d", 120), "control.sock")
	if err := checkSocketPath(long); err == nil {
		t.Fatal("accepted a socket path over the limit")
	} else if !strings.Contains(err.Error(), "K0SMOS_STATE_DIR") {
		t.Errorf("error = %v, want it to suggest a way out", err)
	}

	// But a lookup that does not need the socket must not fail on its length:
	// `logs` wants the console, and once complained about socket paths instead.
	t.Setenv("K0SMOS_STATE_DIR", filepath.Join("/tmp", strings.Repeat("d", 120)))
	if _, _, _, err := guestPaths("guest"); err != nil {
		t.Errorf("guestPaths failed on socket length: %v", err)
	}
}

func TestGuestNamesMustBeOneComponent(t *testing.T) {
	stateIn(t)
	for _, bad := range []string{"", ".", "..", "a/b", "../escape"} {
		if _, _, _, err := guestPaths(bad); err == nil {
			t.Errorf("accepted guest name %q", bad)
		}
	}
}

func TestMetaRoundTrip(t *testing.T) {
	stateIn(t)
	if _, err := ensureGuestDir("vm"); err != nil {
		t.Fatal(err)
	}
	_, _, metaPath, err := guestPaths("vm")
	if err != nil {
		t.Fatal(err)
	}
	want := guestMeta{Name: "vm", PID: 4242, Disk: "/x/root.img", APIPort: 7443}
	if err := saveMeta(metaPath, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != want.PID || got.APIPort != want.APIPort || got.Disk != want.Disk {
		t.Errorf("meta = %+v, want %+v", got, want)
	}

	// The API port is what lets `kubeconfig` point at the right guest without
	// being told the port again.
	if port, err := recordedAPIPort("vm"); err != nil || port != 7443 {
		t.Errorf("recordedAPIPort = %d, %v; want 7443", port, err)
	}
}

// A guest directory with no metadata still has to be listed: it is a guest that was
// booted, and hiding it would leave a disk nobody knows about.
func TestListIncludesGuestsWithoutMetadata(t *testing.T) {
	root := stateIn(t)
	if err := os.MkdirAll(filepath.Join(root, "orphan"), 0700); err != nil {
		t.Fatal(err)
	}
	guests, err := listGuests()
	if err != nil {
		t.Fatal(err)
	}
	if len(guests) != 1 || guests[0].Name != "orphan" {
		t.Errorf("list = %v, want the orphan", guests)
	}
}
