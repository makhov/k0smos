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
