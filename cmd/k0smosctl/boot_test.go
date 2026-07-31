package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// argsFor assembles a command line against real files, since qemuArgs checks that
// the images it is told about exist.
func argsFor(t *testing.T, mutate func(*bootSpec)) []string {
	t.Helper()
	dir := t.TempDir()
	touch := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	s := bootSpec{
		kernel:    touch("vmlinuz"),
		initramfs: touch("initramfs.gz"),
		disk:      touch("k0smos.img"),
		socket:    filepath.Join(dir, "control.sock"),
		mem:       "4096",
		cpus:      "2",
	}
	if mutate != nil {
		mutate(&s)
	}
	g, err := guestFor(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	args, err := qemuArgs(g, s)
	if err != nil {
		t.Fatalf("qemuArgs: %v", err)
	}
	return args
}

// The cmdline is what the node is actually configured by, so its contents matter
// more than any other part of this command.
func TestBootCmdlineCarriesTheEssentials(t *testing.T) {
	args := argsFor(t, nil)
	appendArg := flagValue(t, args, "-append")

	for _, want := range []string{
		"k0smos.root=LABEL=k0smos", // by label: a cloud-init drive shifts device names
		"k0smos.rootfstype=ext4",
		"k0smos.ip=" + guestCIDR,
		"k0smos.gw=" + guestGateway,
		"k0smos.dns=" + guestDNS, // not slirp's resolver, which never answers on macOS
		"panic=10",
	} {
		if !strings.Contains(appendArg, want) {
			t.Errorf("-append is missing %q:\n%s", want, appendArg)
		}
	}
}

// Without a root image the guest stays on the initramfs, and must not be told to
// switch onto a disk that is not there.
func TestBootWithoutDiskDoesNotSetRoot(t *testing.T) {
	args := argsFor(t, func(s *bootSpec) { s.disk = "" })
	if got := flagValue(t, args, "-append"); strings.Contains(got, "k0smos.root=") {
		t.Errorf("-append names a root with no disk attached:\n%s", got)
	}
}

// The control port is what kubeconfig and shutdown depend on, so booting without
// one silently would strand the user.
func TestBootAttachesTheControlPort(t *testing.T) {
	args := argsFor(t, nil)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"virtio-serial-pci",
		"name=k0smos.control",
		"server=on,wait=off",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("qemu args are missing %q", want)
		}
	}
	if !strings.Contains(joined, "hostfwd=tcp::6443-:6443") {
		t.Error("6443 is not forwarded, so kubectl cannot reach the API server")
	}
}

// A stale socket file stops QEMU binding, so it must be cleared rather than left
// for the user to notice.
func TestBootRemovesAStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(sock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	argsFor(t, func(s *bootSpec) { s.socket = sock })
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("stale control socket was left in place")
	}
}

// The cloud-init drive is read-only, matching how an infrastructure provider
// attaches it — a node must not be able to rewrite its own bootstrap data.
func TestBootAttachesCidataReadOnly(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "cidata.iso")
	if err := os.WriteFile(iso, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	args := argsFor(t, func(s *bootSpec) { s.cidata = iso })
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, iso+",if=virtio,format=raw,readonly=on") {
		t.Errorf("cloud-init drive is not attached read-only:\n%s", joined)
	}
}

// A data volume is created on demand, sparsely: k0smos formats it on first boot.
func TestBootCreatesTheDataVolume(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "sub", "data.img")
	argsFor(t, func(s *bootSpec) { s.data, s.dataSize = data, "2G" })

	info, err := os.Stat(data)
	if err != nil {
		t.Fatalf("data volume was not created: %v", err)
	}
	if info.Size() != 2<<30 {
		t.Errorf("size = %d, want %d", info.Size(), int64(2)<<30)
	}
}

// An existing volume must be reused untouched: recreating it would destroy a
// cluster's etcd.
func TestBootReusesAnExistingDataVolume(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data.img")
	if err := os.WriteFile(data, []byte("existing contents"), 0644); err != nil {
		t.Fatal(err)
	}
	argsFor(t, func(s *bootSpec) { s.data, s.dataSize = data, "8G" })

	got, err := os.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing contents" {
		t.Errorf("existing data volume was overwritten: %q", got)
	}
}

func TestBootConsoleModes(t *testing.T) {
	interactive := strings.Join(argsFor(t, nil), " ")
	if !strings.Contains(interactive, "-nographic -serial mon:stdio") {
		t.Errorf("interactive console args missing:\n%s", interactive)
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "console.log")
	headless := strings.Join(argsFor(t, func(s *bootSpec) { s.console = log }), " ")
	if !strings.Contains(headless, "-serial file:"+log) {
		t.Errorf("headless console args missing:\n%s", headless)
	}
	if strings.Contains(headless, "mon:stdio") {
		t.Error("headless boot still attaches stdio")
	}
}

func TestBootRejectsMissingImages(t *testing.T) {
	dir := t.TempDir()
	g, _ := guestFor(runtime.GOARCH)
	_, err := qemuArgs(g, bootSpec{
		kernel:    filepath.Join(dir, "vmlinuz"),
		initramfs: filepath.Join(dir, "initramfs.gz"),
		disk:      filepath.Join(dir, "absent.img"),
	})
	if err == nil {
		t.Error("accepted a root image that does not exist")
	}
}

func TestGuestForUnknownArch(t *testing.T) {
	if _, err := guestFor("riscv64"); err == nil {
		t.Error("accepted an unsupported architecture")
	}
}

// A guest of a different architecture than the host cannot use hardware
// virtualisation, and claiming otherwise makes QEMU fail to start.
func TestAccelFallsBackToEmulationCrossArch(t *testing.T) {
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	if got := strings.Join(accelFor(other), " "); got != "-accel tcg" {
		t.Errorf("accelFor(%s) = %q, want tcg on a %s host", other, got, runtime.GOARCH)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"4G":   4 << 30,
		"512M": 512 << 20,
		"1024": 1024,
	} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-1G", "big", "G"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
}

// flagValue returns the argument following name.
func flagValue(t *testing.T, args []string, name string) string {
	t.Helper()
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("%s not present in %v", name, args)
	return ""
}
