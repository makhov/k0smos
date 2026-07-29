// Package module loads kernel modules, so k0smos can boot on a stock distro
// kernel that ships virtio, ext4 and overlayfs as modules rather than built in.
package module

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"syscall"
)

// Loader is the subset of *sys.Sys that module loading needs.
type Loader interface {
	ReadFile(path string) ([]byte, error)
	InitModule(image []byte, params string) error
}

// Default is the module set a k0s node needs on a stock kernel: virtio for
// disk and network, ext4 and overlayfs for storage, and the netfilter pieces
// kube-proxy and CNI rely on. Modules absent from the kernel are skipped, so
// this list is safe on a monolithic kernel too.
var Default = []string{
	"virtio_net", "virtio_blk", "virtio_pci",
	"ext4", "overlay",
	"br_netfilter", "ip_tables", "iptable_nat", "iptable_filter",
	"nf_conntrack", "nf_nat", "xt_conntrack", "xt_MASQUERADE",
	"veth", "bridge",
}

// Load loads each named module and its dependencies, dependencies first.
//
// base is a kernel module directory such as /lib/modules/6.6.142-0-virt. If it
// has no modules.dep the kernel is monolithic and Load does nothing. Names not
// present in modules.dep are skipped: a module compiled into the kernel simply
// is not listed there.
func Load(l Loader, base string, names []string) error {
	deps, err := readDeps(l, base)
	if err != nil {
		return err
	}
	if deps == nil {
		return nil // monolithic kernel
	}
	done := map[string]bool{}
	for _, name := range names {
		if err := load(l, base, deps, name, done); err != nil {
			return err
		}
	}
	return nil
}

// modules.dep maps a module's relative path to the paths it depends on:
//
//	kernel/drivers/net/virtio_net.ko.gz: kernel/drivers/net/net_failover.ko.gz ...
//
// readDeps returns nil (not an error) when the file is absent.
func readDeps(l Loader, base string) (map[string]modInfo, error) {
	data, err := l.ReadFile(path.Join(base, "modules.dep"))
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}
		return nil, fmt.Errorf("read modules.dep: %w", err)
	}
	deps := map[string]modInfo{}
	for line := range strings.Lines(string(data)) {
		target, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || target == "" {
			continue
		}
		info := modInfo{path: target}
		for _, dep := range strings.Fields(rest) {
			info.deps = append(info.deps, nameOf(dep))
		}
		deps[nameOf(target)] = info
	}
	return deps, nil
}

type modInfo struct {
	path string   // relative to base, e.g. kernel/fs/ext4/ext4.ko.gz
	deps []string // module names, which must be loaded first
}

// nameOf turns a module path into its module name: kernel/fs/ext4/ext4.ko.gz
// becomes ext4.
func nameOf(p string) string {
	name := path.Base(p)
	name = strings.TrimSuffix(name, ".gz")
	name = strings.TrimSuffix(name, ".xz")
	name = strings.TrimSuffix(name, ".zst")
	return strings.TrimSuffix(name, ".ko")
}

func load(l Loader, base string, deps map[string]modInfo, name string, done map[string]bool) error {
	if done[name] {
		return nil
	}
	done[name] = true // set before recursing, so a dependency cycle terminates

	info, ok := deps[name]
	if !ok {
		return nil // built into the kernel, or not shipped
	}
	for _, dep := range info.deps {
		if err := load(l, base, deps, dep, done); err != nil {
			return err
		}
	}

	image, err := l.ReadFile(path.Join(base, info.path))
	if err != nil {
		return fmt.Errorf("read module %s: %w", name, err)
	}
	if image, err = decompress(image); err != nil {
		return fmt.Errorf("decompress module %s: %w", name, err)
	}
	// EEXIST means the kernel already has it — the desired end state either way.
	if err := l.InitModule(image, ""); err != nil && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("load module %s: %w", name, err)
	}
	return nil
}

// decompress unwraps a gzipped module. init_module(2) needs the raw ELF, and
// in-kernel decompression cannot be relied on. Uncompressed input passes
// through unchanged.
func decompress(image []byte) ([]byte, error) {
	if len(image) < 2 || image[0] != 0x1f || image[1] != 0x8b {
		return image, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(image))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
