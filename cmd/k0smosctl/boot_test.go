package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	// Attached by default here, so most cases exercise the foreground path; the
	// detached one is covered explicitly below.
	s := bootSpec{
		kernel:    touch("vmlinuz"),
		initramfs: touch("initramfs.gz"),
		disk:      touch("k0smos.img"),
		socket:    filepath.Join(dir, "control.sock"),
		mem:       "4096",
		cpus:      "2",
		apiPort:   6443,
		attach:    true,
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

func artifactArgsFor(t *testing.T, mutate func(*bootSpec)) []string {
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
		artifact: true,
		firmware: touch("uefi.fd"),
		disk:     touch("machine.qcow2"),
		socket:   filepath.Join(dir, "control.sock"),
		mem:      "4096",
		cpus:     "2",
		apiPort:  6443,
		attach:   true,
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

func TestArtifactBootUsesFirmwareAndOneMachineDisk(t *testing.T) {
	args := artifactArgsFor(t, nil)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"if=pflash,format=raw,readonly=on,file=",
		"machine.qcow2,if=virtio,format=qcow2",
		"name=k0smos.control",
		"hostfwd=tcp::6443-:6443",
		"dns=" + guestDNS,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("artifact boot args are missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"-kernel", "-initrd", "-append"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("artifact boot unexpectedly passes %s:\n%s", forbidden, joined)
		}
	}
}

func TestArtifactBootCanJoinSharedClusterNetwork(t *testing.T) {
	args := artifactArgsFor(t, func(s *bootSpec) {
		s.clusterNet = "127.0.0.1:4321"
		s.clusterMAC = "52:54:00:c0:5e:0b"
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"socket,id=n1,connect=127.0.0.1:4321",
		"virtio-net-pci,netdev=n1,mac=52:54:00:c0:5e:0b",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("shared-network args are missing %q:\n%s", want, joined)
		}
	}
}

func TestArtifactBootAttachesCidataWithoutSeparateDataDisk(t *testing.T) {
	dir := t.TempDir()
	cidata := filepath.Join(dir, "cidata.iso")
	if err := os.WriteFile(cidata, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(artifactArgsFor(t, func(s *bootSpec) { s.cidata = cidata }), " ")
	if !strings.Contains(joined, cidata+",if=virtio,format=raw,readonly=on") {
		t.Errorf("artifact boot did not attach cidata read-only:\n%s", joined)
	}
}

func TestArtifactBootRejectsExternalDataVolume(t *testing.T) {
	dir := t.TempDir()
	g, _ := guestFor(runtime.GOARCH)
	_, err := qemuArgs(g, bootSpec{
		artifact: true,
		firmware: filepath.Join(dir, "uefi.fd"),
		disk:     filepath.Join(dir, "machine.qcow2"),
		data:     filepath.Join(dir, "data.img"),
		attach:   true,
	})
	// Missing boot files are checked first, so create them and retry the semantic
	// validation rather than weakening qemuArgs's input checks.
	if err == nil || !strings.Contains(err.Error(), "firmware") {
		t.Fatalf("first validation = %v, want missing firmware", err)
	}
	for _, name := range []string{"uefi.fd", "machine.qcow2"} {
		if writeErr := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	_, err = qemuArgs(g, bootSpec{
		artifact: true,
		firmware: filepath.Join(dir, "uefi.fd"),
		disk:     filepath.Join(dir, "machine.qcow2"),
		data:     filepath.Join(dir, "data.img"),
		attach:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "already contains") {
		t.Errorf("artifact with --data = %v, want contained-volume error", err)
	}
}

func TestFirmwareCandidatesCoverUbuntu24OVMF(t *testing.T) {
	g, err := guestFor("amd64")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/usr/share/OVMF/OVMF_CODE_4M.fd"; !slices.Contains(firmwareCandidates(g), want) {
		t.Errorf("amd64 firmware candidates do not include Ubuntu 24.04 path %s", want)
	}
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

// Without a root image the guest stays on the initramfs explicitly; an empty
// root setting is reserved for automatic discovery by PID1.
func TestBootWithoutDiskDisablesRootDiscovery(t *testing.T) {
	args := argsFor(t, func(s *bootSpec) { s.disk = "" })
	if got := flagValue(t, args, "-append"); !strings.Contains(got, "k0smos.root=none") {
		t.Errorf("-append does not disable root discovery with no disk attached:\n%s", got)
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

func TestBootDryRunDoesNotRemoveAControlSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(sock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	argsFor(t, func(s *bootSpec) { s.socket, s.dryRun = sock, true })
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("dry-run removed the control socket: %v", err)
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

// Three console modes, and the distinction matters for whether ctrl-c works.
func TestBootConsoleModes(t *testing.T) {
	// Attached: stdio with signal=on, so ctrl-c reaches this process as a signal
	// instead of being swallowed by QEMU's raw mode and sent to the guest.
	attached := strings.Join(argsFor(t, nil), " ")
	if !strings.Contains(attached, "-chardev stdio,id=con,signal=on") {
		t.Errorf("attached console does not keep ctrl-c on the host:\n%s", attached)
	}
	if strings.Contains(attached, "mon:stdio") {
		t.Error("attached console hands the terminal to QEMU, which swallows ctrl-c")
	}

	// Interactive: QEMU owns the terminal, and ctrl-a x is the escape.
	interactive := strings.Join(argsFor(t, func(s *bootSpec) { s.interactive = true }), " ")
	if !strings.Contains(interactive, "-nographic -serial mon:stdio") {
		t.Errorf("interactive console args missing:\n%s", interactive)
	}

	// Detached: a file, because there will be no terminal to write to.
	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "console.log")
	detached := strings.Join(argsFor(t, func(s *bootSpec) {
		s.attach, s.console = false, log
	}), " ")
	if !strings.Contains(detached, "-serial file:"+log) {
		t.Errorf("detached console args missing:\n%s", detached)
	}
	if strings.Contains(detached, "stdio") {
		t.Error("detached boot still attaches stdio")
	}
}

// A detached guest with nowhere to log would produce "-serial file:", which QEMU
// accepts and then writes nowhere findable.
func TestBootDetachedRequiresAConsolePath(t *testing.T) {
	dir := t.TempDir()
	touch := func(n string) string {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0644)
		return p
	}
	g, _ := guestFor(runtime.GOARCH)
	_, err := qemuArgs(g, bootSpec{
		kernel: touch("vmlinuz"), initramfs: touch("initramfs.gz"),
		attach: false, console: "",
	})
	if err == nil {
		t.Error("accepted a detached boot with no console path")
	}
}

// --interactive and --console cannot both have the terminal.
func TestBootInteractiveRejectsAConsoleFile(t *testing.T) {
	dir := t.TempDir()
	touch := func(n string) string {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("x"), 0644)
		return p
	}
	g, _ := guestFor(runtime.GOARCH)
	_, err := qemuArgs(g, bootSpec{
		kernel: touch("vmlinuz"), initramfs: touch("initramfs.gz"),
		interactive: true, console: filepath.Join(dir, "c.log"),
	})
	if err == nil {
		t.Error("accepted --interactive together with --console")
	}
}

// A second guest needs its own forwarded port, and 0 must forward nothing.
func TestBootApiPortIsConfigurable(t *testing.T) {
	got := strings.Join(argsFor(t, func(s *bootSpec) { s.apiPort = 7443 }), " ")
	if !strings.Contains(got, "hostfwd=tcp::7443-:6443") {
		t.Errorf("api port not forwarded as asked:\n%s", got)
	}
	none := strings.Join(argsFor(t, func(s *bootSpec) { s.apiPort = 0 }), " ")
	if strings.Contains(none, "hostfwd") {
		t.Errorf("--api-port 0 still forwarded a port:\n%s", none)
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
