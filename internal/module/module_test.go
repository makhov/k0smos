package module

import (
	"bytes"
	"compress/gzip"
	"errors"
	"slices"
	"syscall"
	"testing"
)

// gz returns name's payload gzipped, mimicking Alpine's .ko.gz modules.
func gz(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeLoader struct {
	files  map[string][]byte
	loaded []string         // payloads, in load order
	errs   map[string]error // payload -> error to return
}

func (f *fakeLoader) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, syscall.ENOENT
	}
	return data, nil
}

func (f *fakeLoader) InitModule(image []byte, _ string) error {
	if err, ok := f.errs[string(image)]; ok {
		return err
	}
	f.loaded = append(f.loaded, string(image))
	return nil
}

// modules.dep lists a module's dependencies AFTER the colon, and those must be
// loaded before the module itself.
const depFile = "kernel/drivers/block/virtio_blk.ko.gz:\n" +
	"kernel/drivers/net/virtio_net.ko.gz: kernel/drivers/net/net_failover.ko.gz kernel/net/core/failover.ko.gz\n" +
	"kernel/drivers/net/net_failover.ko.gz: kernel/net/core/failover.ko.gz\n" +
	"kernel/net/core/failover.ko.gz:\n" +
	"kernel/fs/ext4/ext4.ko.gz:\n"

func newFake(t *testing.T) *fakeLoader {
	t.Helper()
	return &fakeLoader{files: map[string][]byte{
		"/lib/modules/test/modules.dep":                           []byte(depFile),
		"/lib/modules/test/kernel/drivers/block/virtio_blk.ko.gz": gz(t, "virtio_blk"),
		"/lib/modules/test/kernel/drivers/net/virtio_net.ko.gz":   gz(t, "virtio_net"),
		"/lib/modules/test/kernel/drivers/net/net_failover.ko.gz": gz(t, "net_failover"),
		"/lib/modules/test/kernel/net/core/failover.ko.gz":        gz(t, "failover"),
		"/lib/modules/test/kernel/fs/ext4/ext4.ko.gz":             gz(t, "ext4"),
	}}
}

// modules.softdep records dependencies that modules.dep does not. Missing them
// makes init_module fail with ENOENT ("unknown symbol") — this is exactly how
// libcrc32c fails without crc32c on a real Alpine kernel.
func TestLoadHonoursSoftDependencies(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.softdep"] = []byte(
		"# comment\nsoftdep ext4 pre: crc32c\n")
	f.files["/lib/modules/test/modules.alias"] = []byte("alias crc32c crc32c_generic\n")
	f.files["/lib/modules/test/modules.dep"] = []byte(
		depFile + "kernel/crypto/crc32c_generic.ko.gz:\n")
	f.files["/lib/modules/test/kernel/crypto/crc32c_generic.ko.gz"] = gz(t, "crc32c_generic")

	if _, err := Load(f, "/lib/modules/test", []string{"ext4"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"crc32c_generic", "ext4"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v", f.loaded, want)
	}
}

// An init must not let one bad module block the rest: without this, a failure
// early in the list silently costs you ext4, overlayfs and netfilter.
func TestLoadContinuesAfterAFailureAndReportsAll(t *testing.T) {
	f := newFake(t)
	f.errs = map[string]error{"virtio_blk": syscall.ENOENT}
	_, err := Load(f, "/lib/modules/test", []string{"virtio_blk", "ext4"})
	if err == nil {
		t.Fatal("Load = nil, want the failure reported")
	}
	if !slices.Contains(f.loaded, "ext4") {
		t.Errorf("loaded = %v, want ext4 loaded despite virtio_blk failing", f.loaded)
	}
}

func TestLoadResolvesDependenciesFirst(t *testing.T) {
	f := newFake(t)
	if _, err := Load(f, "/lib/modules/test", []string{"virtio_net"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"failover", "net_failover", "virtio_net"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v", f.loaded, want)
	}
}

func TestLoadDecompressesAndLoadsEachModuleOnce(t *testing.T) {
	f := newFake(t)
	// virtio_net and net_failover share the failover dependency.
	if _, err := Load(f, "/lib/modules/test", []string{"virtio_net", "net_failover", "ext4"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"failover", "net_failover", "virtio_net", "ext4"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v", f.loaded, want)
	}
}

// A module compiled into the kernel is not in modules.dep. That is normal, not
// an error — the functionality is already present.
func TestLoadSkipsUnknownModules(t *testing.T) {
	f := newFake(t)
	if _, err := Load(f, "/lib/modules/test", []string{"builtin_thing", "ext4"}); err != nil {
		t.Fatalf("Load = %v, want nil for a module absent from modules.dep", err)
	}
	if !slices.Equal(f.loaded, []string{"ext4"}) {
		t.Errorf("loaded = %v, want [ext4]", f.loaded)
	}
}

// EEXIST means the kernel already has it loaded, which is success.
func TestLoadTreatsAlreadyLoadedAsSuccess(t *testing.T) {
	f := newFake(t)
	f.errs = map[string]error{"ext4": syscall.EEXIST}
	if _, err := Load(f, "/lib/modules/test", []string{"ext4"}); err != nil {
		t.Fatalf("Load = %v, want nil when module is already loaded", err)
	}
}

func TestLoadReportsRealFailures(t *testing.T) {
	f := newFake(t)
	f.errs = map[string]error{"ext4": syscall.EINVAL}
	_, err := Load(f, "/lib/modules/test", []string{"ext4"})
	if err == nil {
		t.Fatal("Load = nil, want error when init_module fails")
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("Load = %v, want it to wrap EINVAL", err)
	}
}

// No modules directory at all means a monolithic kernel: nothing to do.
func TestLoadWithNoModulesDepIsNoOp(t *testing.T) {
	f := &fakeLoader{files: map[string][]byte{}}
	if _, err := Load(f, "/lib/modules/none", []string{"virtio_net"}); err != nil {
		t.Fatalf("Load = %v, want nil when modules.dep is absent", err)
	}
	if len(f.loaded) != 0 {
		t.Errorf("loaded = %v, want none", f.loaded)
	}
}

// Uncompressed .ko must work too — not every distro gzips modules.
func TestLoadHandlesUncompressedModules(t *testing.T) {
	f := &fakeLoader{files: map[string][]byte{
		"/lib/modules/test/modules.dep":            []byte("kernel/fs/ext4/ext4.ko:\n"),
		"/lib/modules/test/kernel/fs/ext4/ext4.ko": []byte("ext4-raw"),
	}}
	if _, err := Load(f, "/lib/modules/test", []string{"ext4"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.loaded, []string{"ext4-raw"}) {
		t.Errorf("loaded = %v, want [ext4-raw]", f.loaded)
	}
}

// Real modules.alias lines. The device entries are globs, which is why an exact
// map lookup cannot serve them, and the reason autoloading needed adding rather
// than already working.
const aliasFile = "" +
	"alias crc32c crc32c_generic\n" +
	"alias virtio:d00000002v* virtio_blk\n" +
	"alias virtio:d00000001v* virtio_net\n" +
	"alias pci:v00008086d000010D3sv*sd*bc*sc*i* e1000e\n" +
	"alias usb:v*p*d*dc*dsc*dp*ic03isc*ip*in* usbhid\n"

// A driver must be found for hardware nothing named in advance. This is the
// point of the whole mechanism: Default cannot enumerate the NICs and HBAs of an
// arbitrary machine, but the kernel's own index can.
func TestLoadForDevicesMatchesModaliasPatterns(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte(aliasFile)

	// As sysfs reports them: a virtio disk and a virtio NIC on a QEMU guest.
	_, err := LoadForDevices(f, "/lib/modules/test", fixedDevices(
		"virtio:d00000002v00001AF4",
		"virtio:d00000001v00001AF4",
	))
	if err != nil {
		t.Fatal(err)
	}
	// virtio_net's dependencies come first, as for any other load.
	want := []string{"virtio_blk", "failover", "net_failover", "virtio_net"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v", f.loaded, want)
	}
}

// A device with no driver in this tree must be ignored, not fail the boot: a
// machine has plenty of devices k0s does not care about.
func TestLoadForDevicesIgnoresUnmatchedDevices(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte(aliasFile)

	res, err := LoadForDevices(f, "/lib/modules/test", fixedDevices(
		"pci:v00008086d00009D2Fsv00001028sd000007A5bc0Csc03i30", // xHCI, no entry
		"acpi:PNP0C0C:",
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Loaded != 0 || len(f.loaded) != 0 {
		t.Errorf("loaded %v, want nothing", f.loaded)
	}
}

// e1000e is in the alias file but not in this tree's modules.dep, which is what a
// built-in driver looks like. Skipped, not an error.
func TestLoadForDevicesSkipsModulesNotInTheTree(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte(aliasFile)

	if _, err := LoadForDevices(f, "/lib/modules/test", fixedDevices(
		"pci:v00008086d000010D3sv00008086sd00000000bc02sc00i00",
	)); err != nil {
		t.Fatal(err)
	}
	if len(f.loaded) != 0 {
		t.Errorf("loaded %v, want nothing", f.loaded)
	}
}

// A monolithic kernel has no modules.dep, and autoloading must be as much of a
// no-op there as Load is.
func TestLoadForDevicesWithNoModulesDepIsNoOp(t *testing.T) {
	f := &fakeLoader{files: map[string][]byte{}}
	res, err := LoadForDevices(f, "/lib/modules/test", fixedDevices("virtio:d00000002v00001AF4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.TreeFound || res.Loaded != 0 {
		t.Errorf("res = %+v, want zero", res)
	}
}

// One device can match several patterns and one module can drive many devices;
// neither may cause a module to be loaded twice.
func TestMatchDevicesDeduplicates(t *testing.T) {
	entries := []aliasEntry{
		{pattern: "pci:v00001AF4d00001000sv*sd*bc*sc*i*", module: "virtio_net"},
		{pattern: "pci:v00001AF4d*sv*sd*bc*sc*i*", module: "virtio_net"},
	}
	got := matchDevices(entries, []string{
		"pci:v00001AF4d00001000sv00001AF4sd00000001bc02sc00i00",
		"pci:v00001AF4d00001000sv00001AF4sd00000002bc02sc00i00",
	})
	if !slices.Equal(got, []string{"virtio_net"}) {
		t.Errorf("matchDevices = %v, want [virtio_net]", got)
	}
}

// A malformed pattern must not cost the machine every other driver.
func TestMatchDevicesSkipsMalformedPatterns(t *testing.T) {
	entries := []aliasEntry{
		{pattern: "pci:v[", module: "broken"},
		{pattern: "pci:v00001AF4d00001001sv*", module: "virtio_blk"},
	}
	got := matchDevices(entries, []string{"pci:v00001AF4d00001001sv00001AF4sd00000002bc01sc00i00"})
	if !slices.Equal(got, []string{"virtio_blk"}) {
		t.Errorf("matchDevices = %v, want [virtio_blk]", got)
	}
}

// Exact and device aliases live in the same file and must not be confused: the
// exact ones resolve a requested name, the glob ones match hardware.
func TestReadAliasesSeparatesExactFromDevicePatterns(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte(aliasFile)

	exact, device := readAliases(f, "/lib/modules/test")
	if exact["crc32c"] != "crc32c_generic" {
		t.Errorf("exact = %v, want crc32c -> crc32c_generic", exact)
	}
	if len(exact) != 1 {
		t.Errorf("exact has %d entries, want 1: %v", len(exact), exact)
	}
	if len(device) != 4 {
		t.Errorf("device has %d entries, want 4: %v", len(device), device)
	}
}

// fixedDevices is an enumerator that always reports the same devices.
func fixedDevices(modaliases ...string) func() ([]string, error) {
	return func() ([]string, error) { return modaliases, nil }
}

// Discovery is not one-shot: loading a bus driver makes its children appear, and
// only then can they be matched. This is the case a single pass gets wrong, and
// it is not hypothetical — it is how virtio works when the transport is a module.
func TestLoadForDevicesRescansAsDevicesAppear(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.dep"] = []byte(depFile +
		"kernel/drivers/virtio/virtio_mmio.ko.gz:\n")
	f.files["/lib/modules/test/kernel/drivers/virtio/virtio_mmio.ko.gz"] = gz(t, "virtio_mmio")
	f.files["/lib/modules/test/modules.alias"] = []byte(
		"alias of:N*T*Cvirtio,mmioC* virtio_mmio\n" +
			"alias virtio:d00000002v* virtio_blk\n")

	// Round 1 sees only the MMIO transport. The disk behind it exists only once
	// that driver is loaded, which is what the second round is for.
	round := 0
	devices := func() ([]string, error) {
		round++
		if round == 1 {
			return []string{"of:NfooTbarCvirtio,mmioC"}, nil
		}
		return []string{"of:NfooTbarCvirtio,mmioC", "virtio:d00000002v00001AF4"}, nil
	}

	res, err := LoadForDevices(f, "/lib/modules/test", devices)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"virtio_mmio", "virtio_blk"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v — the disk driver needs a rescan to be found", f.loaded, want)
	}
	if res.Loaded != 2 {
		t.Errorf("Loaded = %d, want 2", res.Loaded)
	}
}

// Rescanning must stop once a round adds nothing, or a machine whose devices are
// all driven would re-enumerate sysfs forever.
func TestLoadForDevicesStopsWhenNothingNewLoads(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte("alias virtio:d00000002v* virtio_blk\n")

	calls := 0
	devices := func() ([]string, error) {
		calls++
		return []string{"virtio:d00000002v00001AF4"}, nil
	}
	if _, err := LoadForDevices(f, "/lib/modules/test", devices); err != nil {
		t.Fatal(err)
	}
	// One round to load the driver, one to find nothing new. Never the cap.
	if calls != 2 {
		t.Errorf("enumerated %d times, want 2", calls)
	}
	if !slices.Equal(f.loaded, []string{"virtio_blk"}) {
		t.Errorf("loaded = %v, want [virtio_blk]", f.loaded)
	}
}

// A sysfs that cannot be read is reported, not silently treated as "no devices".
func TestLoadForDevicesReportsEnumerationFailure(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte("alias virtio:d00000002v* virtio_blk\n")

	_, err := LoadForDevices(f, "/lib/modules/test", func() ([]string, error) {
		return nil, errors.New("sysfs unreadable")
	})
	if err == nil {
		t.Error("no error when devices could not be enumerated")
	}
}

// A driver loaded by name and then matched by modalias must be counted once, as
// named. Two independent passes get this wrong: the second init_module returns
// EEXIST, which counts as success, and the driver is reported as autoloaded.
func TestLoadAllDoesNotCountNamedModulesAsAutoloaded(t *testing.T) {
	f := newFake(t)
	f.files["/lib/modules/test/modules.alias"] = []byte(
		"alias virtio:d00000002v* virtio_blk\n" +
			"alias virtio:d00000001v* virtio_net\n")

	// virtio_blk is both named and discoverable; virtio_net only discoverable.
	res, err := LoadAll(f, "/lib/modules/test", []string{"virtio_blk"}, fixedDevices(
		"virtio:d00000002v00001AF4",
		"virtio:d00000001v00001AF4",
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Loaded != 1 {
		t.Errorf("Loaded = %d, want 1 (virtio_blk, by name)", res.Loaded)
	}
	// failover and net_failover are virtio_net's dependencies, so three.
	if res.Autoloaded != 3 {
		t.Errorf("Autoloaded = %d, want 3 (virtio_net and its two deps)", res.Autoloaded)
	}
	if res.Devices != 2 {
		t.Errorf("Devices = %d, want 2", res.Devices)
	}
	// And nothing loaded twice, whichever way it was found.
	want := []string{"virtio_blk", "failover", "net_failover", "virtio_net"}
	if !slices.Equal(f.loaded, want) {
		t.Errorf("loaded = %v, want %v", f.loaded, want)
	}
}

// With no modules.dep, LoadAll must be as much of a no-op as its halves.
func TestLoadAllWithNoModulesDepIsNoOp(t *testing.T) {
	f := &fakeLoader{files: map[string][]byte{}}
	res, err := LoadAll(f, "/lib/modules/test", []string{"ext4"}, fixedDevices("virtio:d00000002v0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.TreeFound || res.Loaded != 0 || res.Autoloaded != 0 {
		t.Errorf("res = %+v, want zero", res)
	}
}
