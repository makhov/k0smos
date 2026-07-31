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

// Default is the module set a k0s node needs on a stock kernel. Modules absent
// from the kernel are skipped, so this list is safe on a monolithic kernel too.
var Default = []string{
	// Disk and network devices, and the filesystems k0s stores data on.
	"virtio_net", "virtio_blk", "virtio_pci",
	"ext4", "overlay",

	// No filesystem module for cloud-init drives. Cluster API bootstrap data
	// arrives on an ISO — KubeVirt builds both its NoCloud and its config-drive
	// volumes with `xorrisofs -joliet -rock`, a single code path in
	// pkg/cloud-init — and internal/iso9660 reads that in userspace instead of
	// mounting it. So no isofs, and no vfat with the NLS codepages it drags in.
	//
	// The point is not the ~30 KB saved: it is that a kernel needs no filesystem
	// support at all to take CAPI bootstrap data, which is what keeps monolithic
	// guest kernels like Kata's usable unmodified.
	//
	// Nothing real is given up. A config-drive may be ISO9660 or vfat, and the
	// tooling only writes the first: nova defaults to iso9660, openstacksdk
	// builds Ironic's with genisoimage/mkisofs/xorrisofs. If a vfat one ever
	// turns up, add "vfat" plus nls_codepage_437 and nls_iso8859_1 here —
	// Alpine ships all three as modules, so it needs no kernel work.

	// nftables. k0s selects iptables-nft mode, and without these kube-proxy
	// dies with `iptables: Failed to initialize nft: Protocol not supported`,
	// which crashloops kube-proxy, starves kube-router of the API, and leaves
	// the node NotReady. nft_compat is what makes the iptables-nft shim work.
	"nfnetlink", "nf_tables", "nft_compat", "nft_chain_nat",

	// Legacy iptables path, in case k0s picks it instead. IPv6 too: kube-proxy
	// programs both families and reports failure for either.
	"ip_tables", "iptable_nat", "iptable_filter",
	"ip6_tables", "ip6table_nat", "ip6table_filter",
	"nf_reject_ipv6", "nft_reject_ipv6",

	// Connection tracking and the xtables matches/targets kube-proxy emits.
	// REJECT in particular is not optional: kube-proxy's KUBE-SERVICES chain
	// rejects traffic to services with no endpoints, and without it
	// iptables-restore fails the whole batch with `RULE_APPEND failed (No such
	// file or directory)`, so no service rules are programmed at all.
	"nf_conntrack", "nf_conntrack_netlink", "nf_nat",
	"nf_reject_ipv4", "nft_reject", "nft_reject_ipv4", "nft_reject_inet",
	"ipt_REJECT", "nft_ct", "nft_nat", "nft_masq",
	"xt_conntrack", "xt_MASQUERADE", "xt_comment", "xt_mark", "xt_tcpudp",
	"xt_multiport", "xt_addrtype", "xt_statistic", "xt_nat", "xt_recent",
	// nfacct: kube-proxy's KUBE-FORWARD counts invalid-conntrack drops with
	// `-m nfacct`, so without the match module that chain fails to load.
	"nfnetlink_acct", "xt_nfacct",

	// ipsets, used by kube-router for its firewall rules.
	"ip_set", "ip_set_hash_ip", "ip_set_hash_net", "xt_set",

	// Pod networking: veth pairs into a bridge, with netfilter on the bridge.
	"veth", "bridge", "br_netfilter", "ipip",

	// The root carried inside the initramfs is a file, so it needs a loop device,
	// and the filesystem on it needs its driver. Both are built into the default
	// (Kata) kernel and so are skipped there; they are here for a modular kernel
	// that has them. Alpine's linux-virt has neither — CONFIG_BLK_DEV_LOOP=m but
	// CONFIG_EROFS_FS unset entirely — so an embedded root cannot work there at
	// all, and such a kernel must boot from a disk instead.
	"loop", "erofs",

	// Graceful power-off on real hardware and hypervisors: the ACPI button
	// driver raises the press and evdev exposes it as /dev/input/eventN. Absent
	// these, a power button or `virsh shutdown` does nothing at all.
	"button", "evdev",
}

// Result reports what Load did, so the caller can tell a monolithic kernel from
// a module tree that does not match the running one.
type Result struct {
	// TreeFound is true when base held a modules.dep.
	TreeFound bool
	// Loaded counts the modules actually handed to init_module.
	Loaded int
	// Autoloaded counts those found by matching device modaliases rather than
	// being named. Counted separately and never double-counted: a driver already
	// loaded by name is not autoloaded, and reporting it as such would overstate
	// what discovery contributes.
	Autoloaded int
	// Devices is how many device modaliases were considered.
	Devices int
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
func Load(l Loader, base string, names []string) (Result, error) {
	r, err := newResolver(l, base)
	if r == nil {
		return Result{}, err
	}
	for _, name := range names {
		r.load(name)
	}
	return Result{TreeFound: true, Loaded: r.loaded}, errors.Join(r.errs...)
}

// maxDeviceRounds bounds device discovery. Two or three rounds is normal; the cap
// only stops a pathological alias file from looping.
const maxDeviceRounds = 8

// LoadForDevices loads a driver for each device the kernel reports, by matching
// its modalias against the patterns in modules.alias. devices enumerates the
// modaliases currently present; see sys.Modaliases.
//
// This is what makes a kernel's full hardware support reachable. A hand-written
// list cannot: Default names 50 modules, which is workable for virtio but cannot
// enumerate the NICs and HBAs of arbitrary machines. Matching by modalias is how
// udev does it, and it needs to know nothing about the hardware in advance.
//
// devices is called repeatedly, because discovery is not one-shot: loading a bus
// driver makes its children appear, and only then can they be matched. A PCI
// virtio controller yields devices whose modaliases look like
// "virtio:d00000002v00001AF4", which is what virtio_blk actually binds to — and
// on a kernel where the transport is a module (virtio_mmio) those do not exist
// until it is loaded. udev sees this as a stream of events; with no udev, the
// equivalent is to re-enumerate until a round loads nothing new.
func LoadForDevices(l Loader, base string, devices func() ([]string, error)) (Result, error) {
	r, err := newResolver(l, base)
	if r == nil {
		return Result{}, err
	}
	seen := r.loadDevices(devices)
	return Result{TreeFound: true, Loaded: r.loaded, Autoloaded: r.loaded, Devices: seen}, errors.Join(r.errs...)
}

// LoadAll loads the named set and then autoloads drivers for whatever hardware is
// present, on shared bookkeeping.
//
// Shared matters: with two independent passes, a driver already loaded by name is
// handed to init_module again, comes back EEXIST — which counts as success — and
// is then reported as autoloaded. That overstates what discovery contributes,
// which is the one thing this number exists to measure.
func LoadAll(l Loader, base string, names []string, devices func() ([]string, error)) (Result, error) {
	r, err := newResolver(l, base)
	if r == nil {
		return Result{}, err
	}
	for _, name := range names {
		r.load(name)
	}
	named := r.loaded
	seen := r.loadDevices(devices)
	return Result{
		TreeFound:  true,
		Loaded:     named,
		Autoloaded: r.loaded - named,
		Devices:    seen,
	}, errors.Join(r.errs...)
}

// loadDevices matches devices to drivers, re-enumerating until a round loads
// nothing new. It returns how many modaliases the last round saw.
func (r *resolver) loadDevices(devices func() ([]string, error)) int {
	seen := 0
	for range maxDeviceRounds {
		modaliases, err := devices()
		if err != nil && len(modaliases) == 0 {
			r.errs = append(r.errs, fmt.Errorf("enumerate devices: %w", err))
			return seen
		}
		seen = len(modaliases)
		before := r.loaded
		for _, name := range matchDevices(r.device, modaliases) {
			// Repeating is cheap: the resolver skips what it has already loaded,
			// so only genuinely new devices cost anything.
			r.load(name)
		}
		if r.loaded == before {
			break
		}
	}
	return seen
}

// newResolver reads the index files. It returns a nil resolver when there is no
// modules.dep: either the kernel is monolithic or the tree belongs to a different
// kernel, and only the caller can tell which, since only it can see whether
// /lib/modules exists at all.
func newResolver(l Loader, base string) (*resolver, error) {
	deps, err := readDeps(l, base)
	if err != nil || deps == nil {
		return nil, err
	}
	exact, device := readAliases(l, base)
	return &resolver{
		l:      l,
		base:   base,
		deps:   deps,
		soft:   readSoftdeps(l, base),
		alias:  exact,
		device: device,
		done:   map[string]bool{},
	}, nil
}

type modInfo struct {
	path string   // relative to base, e.g. kernel/fs/ext4/ext4.ko.gz
	deps []string // module names, which must be loaded first
}

type resolver struct {
	l      Loader
	base   string
	deps   map[string]modInfo
	soft   map[string][]string // module -> modules to load before it
	alias  map[string]string   // exact alias -> module
	device []aliasEntry        // modalias glob -> module
	done   map[string]bool
	loaded int
	errs   []error
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
		return
	}
	r.loaded++
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

// aliasEntry is a device alias: a glob to match a device's modalias against,
// and the module that drives it.
type aliasEntry struct {
	pattern string
	module  string
}

// readAliases parses modules.alias, which holds two kinds of line.
//
// Plain names are exact aliases, used to resolve a requested module to the one
// that actually provides it:
//
//	alias crc32c crc32c_generic
//
// Patterns are device aliases, matched against a device's modalias to discover
// which driver it needs. This is how a kernel's hardware support is indexed, and
// what makes loading drivers for unknown hardware possible without naming any:
//
//	alias pci:v00001AF4d00001000sv*sd*bc*sc*i* virtio_net
//	alias pci:v00008086d000010D3sv*sd*bc*sc*i* e1000e
func readAliases(l Loader, base string) (map[string]string, []aliasEntry) {
	exact := map[string]string{}
	var device []aliasEntry

	data, err := l.ReadFile(path.Join(base, "modules.alias"))
	if err != nil {
		return exact, nil
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "alias" {
			continue
		}
		if strings.ContainsAny(fields[1], "*?[") {
			device = append(device, aliasEntry{pattern: fields[1], module: fields[2]})
			continue
		}
		exact[fields[1]] = fields[2]
	}
	return exact, device
}

// matchDevices returns the modules driving the given device modaliases, in the
// order first seen and without repeats.
//
// A device may match several patterns and a module may drive many devices, hence
// the deduplication. Malformed patterns are skipped rather than failing the lot:
// one bad line in a distro's modules.alias must not cost the machine its
// storage driver.
func matchDevices(entries []aliasEntry, modaliases []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, modalias := range modaliases {
		for _, e := range entries {
			if ok, err := path.Match(e.pattern, modalias); err != nil || !ok {
				continue
			}
			if !seen[e.module] {
				seen[e.module] = true
				out = append(out, e.module)
			}
		}
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
