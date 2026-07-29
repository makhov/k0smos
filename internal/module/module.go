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

// Load loads each named module along with its dependencies, dependencies first.
//
// base is a kernel module directory such as /lib/modules/6.6.142-0-virt. If it
// has no modules.dep the kernel is monolithic and Load does nothing. Names not
// present in modules.dep are skipped: a module compiled into the kernel simply
// is not listed there.
//
// A module that fails to load does not stop the others — an init that gives up
// on the first bad module silently costs you storage and networking. Every
// failure is collected and returned together.
func Load(l Loader, base string, names []string) error {
	deps, err := readDeps(l, base)
	if err != nil {
		return err
	}
	if deps == nil {
		return nil // monolithic kernel
	}
	r := &resolver{
		l:     l,
		base:  base,
		deps:  deps,
		soft:  readSoftdeps(l, base),
		alias: readAliases(l, base),
		done:  map[string]bool{},
	}
	for _, name := range names {
		r.load(name)
	}
	return errors.Join(r.errs...)
}

type modInfo struct {
	path string   // relative to base, e.g. kernel/fs/ext4/ext4.ko.gz
	deps []string // module names, which must be loaded first
}

type resolver struct {
	l     Loader
	base  string
	deps  map[string]modInfo
	soft  map[string][]string // module -> modules to load before it
	alias map[string]string   // alias -> module
	done  map[string]bool
	errs  []error
}

// load loads name after everything it depends on, hard and soft.
func (r *resolver) load(name string) {
	key := r.resolve(name)
	if key == "" {
		return // built into the kernel, or not shipped
	}
	if r.done[key] {
		return
	}
	r.done[key] = true // set before recursing, so a dependency cycle terminates

	// Soft dependencies first: they are not in modules.dep, but without them
	// init_module fails with ENOENT for an unresolved symbol.
	for _, dep := range r.soft[key] {
		r.load(dep)
	}
	for _, dep := range r.deps[key].deps {
		r.load(dep)
	}

	image, err := r.l.ReadFile(path.Join(r.base, r.deps[key].path))
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("read module %s: %w", key, err))
		return
	}
	if image, err = decompress(image); err != nil {
		r.errs = append(r.errs, fmt.Errorf("decompress module %s: %w", key, err))
		return
	}
	// EEXIST means the kernel already has it — the desired end state either way.
	if err := r.l.InitModule(image, ""); err != nil && !errors.Is(err, syscall.EEXIST) {
		r.errs = append(r.errs, fmt.Errorf("load module %s: %w", key, err))
	}
}

// resolve maps a name to a loadable module, following modules.alias when the
// name is an alias rather than a module (softdeps name algorithms, e.g. the
// "crc32c" algorithm is provided by the crc32c_generic module). It returns ""
// when nothing matches.
func (r *resolver) resolve(name string) string {
	if _, ok := r.deps[name]; ok {
		return name
	}
	if target, ok := r.alias[name]; ok {
		if _, ok := r.deps[target]; ok {
			return target
		}
	}
	return ""
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
		for dep := range strings.FieldsSeq(rest) {
			info.deps = append(info.deps, nameOf(dep))
		}
		deps[nameOf(target)] = info
	}
	return deps, nil
}

// readSoftdeps parses modules.softdep lines of the form
//
//	softdep libcrc32c pre: crc32c
//
// keeping only the "pre" entries, which must be loaded first. "post" entries
// are advisory and irrelevant to getting a module loaded. An absent or
// unreadable file yields an empty map: soft dependencies are best-effort.
func readSoftdeps(l Loader, base string) map[string][]string {
	out := map[string][]string{}
	data, err := l.ReadFile(path.Join(base, "modules.softdep"))
	if err != nil {
		return out
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "softdep" {
			continue
		}
		module, rest := fields[1], fields[2:]
		pre := false
		for _, f := range rest {
			switch f {
			case "pre:":
				pre = true
			case "post:":
				pre = false
			default:
				if pre {
					out[module] = append(out[module], f)
				}
			}
		}
	}
	return out
}

// readAliases parses modules.alias lines of the form
//
//	alias crc32c crc32c_generic
func readAliases(l Loader, base string) map[string]string {
	out := map[string]string{}
	data, err := l.ReadFile(path.Join(base, "modules.alias"))
	if err != nil {
		return out
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "alias" {
			continue
		}
		out[fields[1]] = fields[2]
	}
	return out
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
