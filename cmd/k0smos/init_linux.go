//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/amakhov/k0smos/internal/blkid"
	"github.com/amakhov/k0smos/internal/cgroup"
	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/control"
	"github.com/amakhov/k0smos/internal/datavol"
	"github.com/amakhov/k0smos/internal/dhcp"
	"github.com/amakhov/k0smos/internal/etcd"
	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
	"github.com/amakhov/k0smos/internal/module"
	"github.com/amakhov/k0smos/internal/mount"
	knet "github.com/amakhov/k0smos/internal/net"
	"github.com/amakhov/k0smos/internal/power"
	"github.com/amakhov/k0smos/internal/reaper"
	"github.com/amakhov/k0smos/internal/shutdown"
	"github.com/amakhov/k0smos/internal/status"
	"github.com/amakhov/k0smos/internal/supervise"
	"github.com/amakhov/k0smos/internal/switchroot"
	"github.com/amakhov/k0smos/internal/sys"

	"golang.org/x/sys/unix"
)

const (
	cmdlinePath = "/proc/cmdline"

	// newRootDir is where the real root filesystem is mounted before the switch.
	newRootDir = "/newroot"
	// initPath is k0smos's own location on the real root, re-executed after the
	// switch. It must match where mkrootfs.sh installs the binary.
	initPath = "/sbin/k0smos"
	// switchedFlag marks the post-switch invocation, so it does not switch again.
	switchedFlag = "--switched-root"

	// virtioPortsDir is where the kernel names virtio-serial ports, and
	// controlPortName is the port k0smos takes shutdown commands from. The host
	// side is set up by image/run-qemu.sh.
	virtioPortsDir  = "/sys/class/virtio-ports"
	controlPortName = "k0smos.control"
	// inputDevDir holds the evdev nodes the ACPI power button reports through.
	inputDevDir = "/dev/input"
	// controlRetry is how long to wait before reopening an idle control port.
	controlRetry = time.Second

	// How long to keep looking for the root device before giving up.
	rootProbeAttempts = 30
	rootProbeInterval = 500 * time.Millisecond
	// canonicalRootSpec is the platform-independent contract for a packaged
	// disk root. Platform wrappers only have to label the root filesystem; PID1
	// discovers it without requiring a platform-specific kernel argument.
	canonicalRootSpec = "LABEL=k0smos"
	// noRootSpec explicitly requests an initramfs-only boot. An empty root is
	// reserved for automatic discovery so a normal artifact boots with no root
	// arguments at all.
	noRootSpec = "none"

	// embeddedRoot is where mkinitramfs.sh puts a root filesystem image carried
	// inside the initramfs. Present only for a read-only root (erofs), which is
	// small enough to travel with the kernel rather than as a separate disk.
	embeddedRoot = "/k0smos-root.img"

	// modulesRoot holds one directory per kernel version.
	modulesRoot = "/lib/modules"
	// k0sDataDir is where k0s keeps its own state, including the PKI. Fixed by k0s,
	// and deliberately not cfg.DataDir: that names where k0smos mounts the data
	// volume, which is /var — a volume mounted there covers /var/lib/k0s and
	// /var/lib/kubelet both. Conflating the two put the kubeconfig at /var/pki.
	k0sDataDir = "/var/lib/k0s"

	// metadataMount is where a cloud-init drive is mounted while being read.
	metadataMount = "/run/k0smos/metadata"
	// bootRecordPath holds the boot record, readable over the control port while
	// the node runs and off the disk afterwards.
	bootRecordPath = "/run/k0smos/boot.json"
	// maxServedFile bounds a file served to the host, below the reply framing's
	// own cap so an oversized file is reported rather than truncated.
	maxServedFile = 4 << 20
	// msReadOnly is MS_RDONLY: a metadata drive is never written to.
	msReadOnly = 0x1

	// childGrace is how long the k0s child gets to exit after ctx cancellation
	// before we start detaching filesystems under it.
	childGrace = 5 * time.Second
	// etcdLeaveTimeout bounds the graceful etcd departure. Generous, because a
	// busy cluster can take a while, but finite so a lost quorum cannot stop the
	// machine from shutting down.
	etcdLeaveTimeout = 30 * time.Second
)

// logf writes a progress line to the console. As PID1 there is nowhere else to
// report to, and a silent init is undebuggable when a boot goes wrong.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "k0smos: "+format+"\n", args...)
}

// rec is the boot record. The console shows a boot as it happens and then loses
// it; this is what makes the same information answerable afterwards, over the
// control port or off the disk. Starts sink-less so anything recorded before
// /run exists is still kept in memory.
var rec = status.New(nil)

// step records the outcome of a boot stage. The console message stays where it
// is: this adds a durable record, it does not replace the running commentary.
func step(name string, err error, detail string) {
	rec.Step(name, err, detail)
}

// recordTo points the recorder at a file once there is somewhere to write. The
// path is under /run, so it is per-boot and never stale.
func recordTo(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		logf("warn: boot record dir: %v", err)
		return
	}
	rec = status.NewFrom(rec, func(b []byte) error {
		// Written whole each time rather than appended: a truncated record is
		// worse than a slightly stale one.
		return os.WriteFile(path, b, 0644)
	})
}

// shutdownAdapter lets *sys.Sys satisfy shutdown.Shutdowner, whose Mounts()
// returns []string so that package stays free of any internal/sys import.
type shutdownAdapter struct{ *sys.Sys }

func (a shutdownAdapter) Mounts() ([]string, error) { return a.Sys.MountTargets() }

func run(ctx context.Context) error {
	return boot(ctx, sys.New(), slices.Contains(os.Args, switchedFlag))
}

// findControlPort locates the virtio-serial port named controlPortName and
// returns its device path, or "" if the host did not attach one.
//
// virtio-serial ports are identified by name through sysfs: the directory
// /sys/class/virtio-ports/vportNpM holds a "name" file, and the device is
// /dev/<that directory's basename>.
func findControlPort(name string) string {
	entries, err := os.ReadDir(virtioPortsDir)
	if err != nil {
		return "" // no virtio-serial bus at all
	}
	for _, e := range entries {
		got, err := os.ReadFile(filepath.Join(virtioPortsDir, e.Name(), "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(got)) == name {
			return "/dev/" + e.Name()
		}
	}
	return ""
}

// watchControlPort returns a channel of host shutdown requests, or nil when no
// control port is present.
//
// The port is reopened on EOF: with no host client attached it reads EOF
// immediately, so watching it once would stop listening before anyone could
// send anything.
func watchControlPort(ctx context.Context, s *sys.Sys, cfg config.Config) <-chan control.Command {
	dev := findControlPort(controlPortName)
	if dev == "" {
		logf("no control port; shutdown only via SIGTERM/SIGINT")
		return nil
	}
	logf("listening for host commands on %s", dev)
	return control.WatchReopen(ctx, func() (io.ReadWriteCloser, error) {
		// Read-write, because requests are answered down the same port. Opening
		// it read-only was enough while shutdown was the only thing it carried.
		return os.OpenFile(dev, os.O_RDWR, 0)
	}, controlRetry, func(request string) ([]byte, error) {
		return answerRequest(s, cfg, request)
	})
}

// answerRequest serves a host request for data from the node.
func answerRequest(s *sys.Sys, cfg config.Config, request string) ([]byte, error) {
	verb, arg, _ := strings.Cut(request, " ")
	switch verb {
	case control.RequestKubeconfig:
		return readKubeconfig()
	case control.RequestToken:
		return createJoinToken(cfg, strings.TrimSpace(arg))
	case control.RequestStatus:
		return rec.JSON()
	case control.RequestDmesg:
		return s.KernelLog()
	case control.RequestCat:
		return readForHost(strings.TrimSpace(arg))
	}
	return nil, fmt.Errorf("unknown request %q", request)
}

// readForHost serves one file to the host. This is how pod logs, the rendered
// k0s config and /run state are reached on a machine with no shell.
//
// Refuses a directory rather than returning its raw bytes, and refuses anything
// too large for the reply framing so the failure names the size instead of
// arriving as a truncated file.
func readForHost(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("no path given; use %q", control.RequestCat+" /path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxServedFile {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit for a reply",
			path, info.Size(), int64(maxServedFile))
	}
	return os.ReadFile(path)
}

// readKubeconfig returns the admin kubeconfig off the filesystem.
//
// This exists so that reaching a cluster does not mean shutting the machine down
// and reading its disk with debugfs. Off the filesystem rather than from k0s, so
// it works whether or not k0s is still running — and reports plainly when the
// cluster has not got that far.
func readKubeconfig() ([]byte, error) {
	path := filepath.Join(k0sDataDir, "pki", "admin.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s is not readable yet; the cluster may still be starting: %w", path, err)
	}
	logf("sent %s to the host on request", path)
	return data, nil
}

// joinRoles are the roles a token may be minted for. The request arrives from
// outside, so the role is matched against this list rather than passed through
// into an argument vector.
var joinRoles = []string{"controller", "worker"}

// tokenTimeout bounds `k0s token create`. It contacts the API server, so asking
// too early blocks rather than failing; the host gets a clear error instead of a
// request that never returns.
const tokenTimeout = 2 * time.Minute

// createJoinToken mints a k0s join token for another machine.
//
// It has to run inside the guest: a token is signed with the cluster CA, which
// only a machine that is already a member holds. The binary is the one being
// supervised, so a node told to run something else — or a smoke-test workload —
// reports that rather than executing an unrelated /usr/local/bin/k0s.
func createJoinToken(cfg config.Config, role string) ([]byte, error) {
	if !slices.Contains(joinRoles, role) {
		return nil, fmt.Errorf("role must be one of %v, got %q", joinRoles, role)
	}
	if len(cfg.Exec) == 0 || filepath.Base(cfg.Exec[0]) != "k0s" {
		return nil, fmt.Errorf("this node is not running k0s (%v), so it cannot mint a join token", cfg.Exec)
	}

	ctx, cancel := context.WithTimeout(context.Background(), tokenTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Exec[0], "token", "create", "--role="+role)
	// PID1 inherits no environment, and k0s looks for its staged binaries on PATH.
	cmd.Env = []string{"PATH=" + cfg.Path, "HOME=/root"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// k0s writes its progress to stderr, so the tail of it says why.
		return nil, fmt.Errorf("k0s token create --role=%s: %w: %s",
			role, err, lastLine(stderr.String()))
	}
	logf("minted a %s join token for the host", role)
	// k0s prints the token with a trailing newline; a token file must not carry
	// one, because k0s reads the file verbatim.
	return bytes.TrimSpace(out), nil
}

// lastLine returns the final non-empty line of s, for a one-line error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "no output"
}

// configureDHCP brings the interface up, acquires a lease, applies it, and keeps
// renewing in the background. It returns the lease's first DNS server, if any.
//
// The link must be up before DHCP runs: with the interface down the kernel has
// nothing to send through, whereas static configuration can set an address
// first and bring the link up afterwards.
func configureDHCP(ctx context.Context, s *sys.Sys, cfg config.Config, iface string) (string, error) {
	if err := s.LinkUp(iface); err != nil {
		return "", fmt.Errorf("link up: %w", err)
	}
	mac, err := s.InterfaceMAC(iface)
	if err != nil {
		return "", fmt.Errorf("read MAC: %w", err)
	}
	conn, err := s.DHCPConn(iface)
	if err != nil {
		return "", fmt.Errorf("open socket: %w", err)
	}

	client := &dhcp.Client{Conn: conn, MAC: mac, Hostname: cfg.Hostname}
	lease, err := client.Acquire(ctx)
	if err != nil {
		conn.Close()
		return "", err
	}

	apply := func(l dhcp.Lease) error {
		gw := ""
		if l.Router != nil {
			gw = l.Router.String()
		}
		if err := knet.Configure(s, iface, l.CIDR(), gw); err != nil {
			return err
		}
		logf("%s configured %s gw %s (lease %s)", iface, l.CIDR(), gw, l.LeaseTime)
		step("network", nil, fmt.Sprintf("%s dhcp %s gw %s", iface, l.CIDR(), gw))
		return nil
	}
	if err := apply(lease); err != nil {
		conn.Close()
		return "", err
	}

	go func() {
		defer conn.Close()
		// Renew returns only when the context ends or the lease is
		// unrecoverable; either way the node keeps running with what it has.
		if err := client.Renew(ctx, lease, apply); err != nil && ctx.Err() == nil {
			logf("warn: dhcp renewal stopped: %v", err)
		}
	}()

	if len(lease.DNS) > 0 {
		return lease.DNS[0].String(), nil
	}
	return "", nil
}

// loadMetadata finds a cloud-init drive, applies its write_files, and returns
// what it said. This is how Cluster API hands a machine its identity: a
// bootstrap provider renders a cloud-config, and the infrastructure provider
// attaches it as a NoCloud ISO or an OpenStack config-drive.
//
// Everything here is best-effort. A machine with no such drive — a manual boot,
// or a platform that supplies nothing — must still come up.
func loadMetadata(s *sys.Sys) (metadata.UserData, metadata.MetaData) {
	var ud metadata.UserData
	var md metadata.MetaData

	dev := ""
	label := ""
	for _, l := range metadata.Labels {
		if d, err := blkid.Resolve(s, "LABEL="+l); err == nil {
			dev, label = d, l
			break
		}
	}
	if dev == "" {
		return ud, md
	}

	files, done, err := openMetadata(s, dev, label)
	if err != nil {
		logf("warn: read metadata drive %s: %v", dev, err)
		return ud, md
	}
	defer done()

	ud, md, err = metadata.Load(files)
	if err != nil {
		logf("warn: read metadata: %v", err)
		return metadata.UserData{}, metadata.MetaData{}
	}
	for _, w := range ud.Warnings {
		logf("metadata: %s", w)
	}
	if len(ud.WriteFiles) > 0 {
		if err := metadata.Apply(ud, s.Mkdir, s.WriteFile); err != nil {
			logf("warn: apply write_files: %v", err)
		} else {
			logf("wrote %d file(s) from user-data", len(ud.WriteFiles))
		}
	}
	return ud, md
}

// openMetadata returns a reader for the files on a cloud-init drive, plus a
// cleanup to call when done.
//
// An ISO is parsed in userspace, which is the case that matters: every CAPI
// infrastructure provider attaches its bootstrap data as one, and KubeVirt has a
// single code path that always writes an ISO. Doing it here rather than asking
// the kernel to mount it means CONFIG_ISO9660_FS is not required, which is what
// keeps otherwise-good monolithic kernels usable — Kata's builds in no ISO9660
// at all.
//
// In practice that covers every config-drive k0smos will meet. Both formats the
// spec permits are ISO9660 or vfat, and the tooling only produces the first:
// nova defaults to iso9660, openstacksdk builds Ironic's with
// genisoimage/mkisofs/xorrisofs, and KubeVirt uses xorrisofs. vfat is allowed and
// unused.
//
// A vfat drive would still fall back to mount, which needs the kernel to support
// it: internal/module ships no "vfat", so that would be one line to add there
// (Alpine has vfat and the nls_* codepages as modules — no kernel work).
func openMetadata(s *sys.Sys, dev, label string) (metadata.Files, func(), error) {
	name := strings.TrimPrefix(dev, "/dev/")
	info, _ := blkid.Identify(s, name)
	if info.FSType == "iso9660" {
		r, err := iso9660.Open(iso9660.OnDevice(s, name))
		if err != nil {
			return nil, nil, err
		}
		logf("reading %s (iso9660, LABEL=%s) directly, no mount", dev, label)
		return r, func() {}, nil
	}

	if err := s.Mkdir(metadataMount, 0755); err != nil {
		return nil, nil, fmt.Errorf("mkdir %s: %w", metadataMount, err)
	}
	// The label alone does not say which filesystem this is, and probing may
	// have recognised nothing, so try the plausible ones. iso9660 is not among
	// them: probing identifies it by the same primary volume descriptor the
	// reader parses, so a drive that got here is not one.
	for _, fstype := range []string{"vfat", "ext4"} {
		if err := s.Mount(dev, metadataMount, fstype, msReadOnly, ""); err == nil {
			logf("mounted %s (%s, LABEL=%s) at %s", dev, fstype, label, metadataMount)
			step("metadata", nil, fmt.Sprintf("%s (%s, LABEL=%s)", dev, fstype, label))
			return metadata.Dir(metadataMount), func() {
				if err := s.Unmount(metadataMount, 0); err != nil {
					logf("warn: unmount %s: %v", metadataMount, err)
				}
			}, nil
		}
	}
	return nil, nil, errors.New("no filesystem could be read or mounted")
}

// leaveEtcd asks k0s to give up this node's etcd membership, bounded by a
// timeout so a stalled cluster cannot keep the machine alive indefinitely.
//
// This runs a command, which everything else in k0smos deliberately avoids —
// but the binary is the workload already being supervised and the subcommand is
// fixed here, not taken from user-data.
func leaveEtcd(workload []string) {
	argv := etcd.LeaveCmd(workload)
	if argv == nil {
		return // worker, or kine-backed: no membership to give up
	}
	logf("leaving etcd cluster: %v", argv)

	// Deliberately not derived from the boot context: that may already be
	// cancelled, and the leave needs its own window regardless of why we stop.
	ctx, cancel := context.WithTimeout(context.Background(), etcdLeaveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Not fatal. A single-node or kine cluster refuses, and a lost quorum
		// cannot process the removal — in both cases stopping is still correct.
		logf("warn: etcd leave: %v", err)
		return
	}
	logf("left etcd cluster")
}

// watchPowerButton returns a channel that fires when the hardware power button
// is pressed, or nil if there are no input devices to watch.
//
// Every /dev/input/event* is watched because which one carries the power key
// depends on the platform, and without udev there are no by-path symlinks to
// pick from. Non-power events are filtered out by internal/power.
func watchPowerButton(ctx context.Context) <-chan struct{} {
	entries, err := os.ReadDir(inputDevDir)
	if err != nil {
		return nil // no evdev: no ACPI button, or the module is not loaded
	}
	merged := make(chan struct{})
	watched := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		f, err := os.Open(filepath.Join(inputDevDir, e.Name()))
		if err != nil {
			continue
		}
		watched++
		go func() {
			for range power.Watch(ctx, f) {
				select {
				case merged <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if watched == 0 {
		return nil
	}
	logf("watching %d input device(s) for the power button", watched)
	return merged
}

// pivot mounts the real root filesystem and switches into it, re-executing
// k0smos there as PID1. It does not return on success.
// rootDevice finds the block device holding the root filesystem.
//
// An image carried inside the initramfs is attached to a loop device: that is what
// lets the root travel with the kernel instead of as a second artifact. Otherwise
// the root is a real disk. With no explicit override, the canonical LABEL=k0smos
// filesystem is discovered automatically.
func selectRoot(explicit string, hasEmbedded bool) (spec string, useEmbedded, disabled bool) {
	switch {
	case explicit == noRootSpec:
		return "", false, true
	case explicit != "":
		return explicit, false, false
	case hasEmbedded:
		return "", true, false
	default:
		return canonicalRootSpec, false, false
	}
}

func rootDevice(s *sys.Sys, cfg config.Config) (string, error) {
	// An explicit k0smos.root= wins over an embedded image. Both can be present —
	// the initramfs carries a root by default, and a boot may still be told to
	// switch onto a disk — and silently ignoring what the cmdline named would be
	// the wrong way round.
	hasEmbedded := false
	if cfg.Root == "" {
		_, err := os.Stat(embeddedRoot)
		hasEmbedded = err == nil
	}
	rootSpec, useEmbedded, disabled := selectRoot(cfg.Root, hasEmbedded)
	if disabled {
		return "", fmt.Errorf("root discovery is disabled by k0smos.root=%s", noRootSpec)
	}
	if useEmbedded {
		// Read-only: the image is erofs, and attaching it writable makes the
		// mount fail with EACCES.
		dev, err := s.LoopAttach(embeddedRoot, true)
		if err != nil {
			return "", fmt.Errorf("attach %s to a loop device: %w", embeddedRoot, err)
		}
		logf("attached %s at %s", embeddedRoot, dev)
		return dev, nil
	}
	if cfg.Root == "" {
		logf("no explicit or embedded root; discovering canonical %s", rootSpec)
	}

	// Retried because virtio_blk and friends probe asynchronously, so the device
	// can appear just after its module loads.
	dev, err := blkid.ResolveWait(s, rootSpec, rootProbeAttempts, func() {
		time.Sleep(rootProbeInterval)
	})
	if err != nil {
		return "", fmt.Errorf("find root %s: %w", rootSpec, err)
	}
	if dev != rootSpec {
		logf("resolved %s to %s", rootSpec, dev)
		step("root", nil, fmt.Sprintf("%s -> %s", rootSpec, dev))
	}
	return dev, nil
}

func pivot(s *sys.Sys, cfg config.Config) error {
	// UUID=/LABEL= are resolved here rather than passed to mount(2): on real
	// hardware disks enumerate as /dev/sda or /dev/nvme0n1 and can reorder
	// between boots. Retried because virtio_blk and friends probe
	// asynchronously, so the device can appear just after its module loads.
	dev, err := rootDevice(s, cfg)
	if err != nil {
		return err
	}

	// Prefer what the device actually holds over what the cmdline said. An image
	// carried in the initramfs needs no k0smos.rootfstype=, and getting it wrong
	// fails as an unhelpful EINVAL from mount.
	fstype := cfg.RootFSType
	if info, ok := blkid.Identify(s, strings.TrimPrefix(dev, "/dev/")); ok && info.FSType != fstype {
		logf("root %s holds %s, not %s — using %s", dev, info.FSType, fstype, info.FSType)
		fstype = info.FSType
	}

	if err := s.Mkdir(newRootDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", newRootDir, err)
	}
	// Read-write first, then read-only. A writable root is what ext4 wants; but a
	// read-only filesystem (erofs) or a read-only device refuses the first attempt
	// with EACCES, and the boot has nothing useful to say about why.
	err = s.Mount(dev, newRootDir, fstype, 0, cfg.RootFlags)
	readOnly := false
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EROFS) {
		err = s.Mount(dev, newRootDir, fstype, msReadOnly, cfg.RootFlags)
		readOnly = err == nil
	}
	if err != nil {
		return fmt.Errorf("mount %s (%s): %w", dev, fstype, err)
	}
	// A read-only filesystem such as EROFS can accept a mount without
	// MS_RDONLY and still report the resulting mount as read-only. This is the
	// normal metal-root path, so report the effective state rather than merely
	// which mount(2) call succeeded.
	if !readOnly {
		if ro, statErr := s.IsReadOnly(newRootDir); statErr == nil {
			readOnly = ro
		}
	}
	how := "read-write"
	if readOnly {
		how = "read-only"
	}
	logf("mounted %s at %s %s, switching root", dev, newRootDir, how)
	// The marker tells the next k0smos it is already on the real root, so it
	// proceeds with the rest of init instead of trying to switch again.
	return switchroot.Do(s, newRootDir, initPath, []string{initPath, switchedFlag})
}

// loadModules loads the configured kernel modules from the running kernel's
// /lib/modules directory.
func loadModules(s *sys.Sys, cfg config.Config) error {
	names := cfg.Modules
	if names == nil {
		names = module.Default
	}
	if len(names) == 0 {
		logf("module loading disabled")
		step("modules", nil, "disabled by k0smos.modules=none")
		return nil
	}
	release, err := s.Release()
	if err != nil {
		return fmt.Errorf("uname: %w", err)
	}
	base := "/lib/modules/" + release
	// The named set plus whatever the hardware asks for. Both in one call so a
	// driver found twice is counted once — see module.LoadAll.
	res, err := module.LoadAll(s, base, names, s.Modaliases)
	if err != nil {
		// Individual failures are collected rather than fatal: one undriveable
		// device or one bad module must not cost the machine the others.
		logf("warn: modules: %v", err)
		step("modules", err, "")
	}
	if res.TreeFound {
		logf("loaded %d kernel module(s) from %s, autoloaded %d driver(s) for %d device(s)",
			res.Loaded, base, res.Autoloaded, res.Devices)
		step("modules", nil, fmt.Sprintf("%d loaded, %d autoloaded for %d devices (%s)",
			res.Loaded, res.Autoloaded, res.Devices, release))
		return nil
	}

	// No modules.dep under the running kernel's directory. That is expected on a
	// monolithic kernel and a serious problem otherwise, and the two used to be
	// indistinguishable — k0smos reported success either way, so a kernel/module
	// version skew showed up later as a baffling "cannot find root".
	if entries, derr := os.ReadDir(modulesRoot); derr == nil && len(entries) > 0 {
		have := make([]string, 0, len(entries))
		for _, e := range entries {
			have = append(have, e.Name())
		}
		logf("warn: %s has module trees %v but none for the running kernel %q — "+
			"kernel and modules are out of step, so NO modules were loaded",
			modulesRoot, have, release)
		step("modules", fmt.Errorf("kernel %s has no module tree; found %v", release, have), "")
		return nil
	}
	logf("no module tree; assuming a monolithic kernel")
	step("modules", nil, "none needed; monolithic kernel")
	return nil
}

// boot performs OS init, starts the reaper and the supervised k0s child, then
// blocks until a termination signal arrives and shuts the machine down.
//
// switched reports whether this is the post-switch_root invocation, which must
// not attempt the switch a second time.
func boot(ctx context.Context, s *sys.Sys, switched bool) error {
	logf("starting as PID1 (switched-root=%t)", switched)
	rec.SetSwitchedRoot(switched)
	if err := mount.Ensure(s); err != nil {
		step("mounts", err, "")
		return fmt.Errorf("mounts: %w", err)
	}
	logf("pseudo-filesystems mounted")
	step("mounts", nil, "")
	// /run exists now, so the record can start persisting. Anything recorded
	// above was held in memory and is carried over.
	recordTo(bootRecordPath)

	// Only readable now that /proc is mounted.
	cfg := config.Parse(readCmdline(cmdlinePath))

	// Exported here rather than just before the workload: PID1 inherits no
	// environment, and k0smos itself execs bundled binaries (mkfs for the data
	// volume, k0s for the etcd leave) well before the child starts.
	if err := os.Setenv("PATH", cfg.Path); err != nil {
		logf("warn: set PATH: %v", err)
	}

	// Modules come before cgroup and net: a stock distro kernel ships virtio,
	// ext4 and overlayfs as modules, so without this there is no NIC and no
	// container storage. Failures are not fatal — the kernel may have the
	// functionality built in, in which case there is nothing to load.
	if err := loadModules(s, cfg); err != nil {
		logf("warn: modules: %v", err)
	}

	// Leave the initramfs for the real root. kubelet cannot
	// run on a ramfs root — cadvisor finds no filesystem info for it. Modules
	// had to be loaded first: the kernel needs virtio_blk and ext4 before the
	// root device is even visible. With no override, rootDevice first uses an
	// embedded image and then the canonical LABEL=k0smos disk. root=none is the
	// explicit initramfs-only escape hatch used by smoke tests.
	if cfg.Root != noRootSpec && !switched {
		if err := pivot(s, cfg); err != nil {
			return fmt.Errorf("switch root: %w", err)
		}
	}

	// A read-only root — erofs — cannot serve the paths k0s and cloud-init write
	// to, so those get a tmpfs overlay before anything tries. Done here because it
	// must precede the resolv.conf write, cloud-init's write_files and the
	// workload, and because /run has to be a tmpfs already (mount.Ensure).
	if ro, err := s.IsReadOnly("/"); err != nil {
		logf("warn: could not tell whether the root is read-only: %v", err)
	} else if ro {
		if err := mount.MakeWritable(s, mount.WritablePaths); err != nil {
			logf("warn: %v", err)
		} else {
			logf("root is read-only; overlaid %v with tmpfs", mount.WritablePaths)
		}
	}

	// The mutable data volume, mounted before k0s can touch its data directory.
	// Not fatal: without one, k0s falls back to the root filesystem, which is how
	// a machine with no data volume already behaves.
	if res, err := datavol.Prepare(s, datavol.Options{
		Spec:       cfg.Data,
		Label:      cfg.DataLabel,
		FSType:     cfg.DataFSType,
		MountPoint: cfg.DataDir,
	}); err != nil {
		logf("warn: data volume: %v", err)
	} else if res.Device != "" {
		verb := "mounted"
		if res.Formatted {
			verb = "formatted and mounted"
		}
		logf("%s data volume %s at %s", verb, res.Device, cfg.DataDir)
		// /opt is a symlink to /var/opt on a read-only root, so the target has to
		// exist before containerd stages plugins into it.
		if err := s.MkdirAll("/var/opt", 0755); err != nil {
			logf("warn: mkdir /var/opt: %v", err)
		}
	}

	if err := cgroup.Setup(s); err != nil {
		step("cgroup2", err, "")
		return fmt.Errorf("cgroup: %w", err)
	}
	logf("cgroup2 hierarchy ready")
	step("cgroup2", nil, "")

	// Metadata is local block-device input, so it does not need networking. Read
	// it before configuring interfaces: a platform artifact has one immutable
	// GRUB command line, while its metadata drive supplies the distinct address
	// each machine needs on a cluster segment. write_files still happens after
	// the writable overlays and /var are ready.
	userData, metaData := loadMetadata(s)
	applyMachineConfig(&cfg, userData.Machine)

	if err := knet.Up(s); err != nil {
		step("loopback", err, "")
		return fmt.Errorf("net: %w", err)
	}
	logf("loopback up")
	step("loopback", nil, "")

	// Created here rather than just before the child starts, because the DHCP
	// renewal goroutine below needs to be cancelled on shutdown too.
	runCtx, stopChild := context.WithCancel(ctx)
	defer stopChild()

	// Not fatal: a node with only loopback still boots, it just cannot pull
	// images. Better a degraded node with a console message than a panic.
	dns := cfg.DNS
	for _, nic := range cfg.NICs() {
		if nic.DHCP() {
			leaseDNS, err := configureDHCP(runCtx, s, cfg, nic.Name)
			if err != nil {
				logf("warn: dhcp on %s: %v", nic.Name, err)
			} else if dns == "" {
				// An explicit k0smos.dns= wins: the lease's resolver is not always
				// usable (QEMU's slirp hands out one that never answers).
				dns = leaseDNS
			}
			continue
		}
		if err := knet.Configure(s, nic.Name, nic.Addr, nic.Gateway); err != nil {
			logf("warn: configure %s: %v", nic.Name, err)
		} else {
			logf("%s configured %s gw %s", nic.Name, nic.Addr, nic.Gateway)
			step("network", nil, fmt.Sprintf("%s %s gw %s", nic.Name, nic.Addr, nic.Gateway))
		}
	}
	if dns != "" {
		resolv := fmt.Appendf(nil, "nameserver %s\n", dns)
		if err := s.WriteFile("/etc/resolv.conf", resolv, 0644); err != nil {
			logf("warn: write resolv.conf: %v", err)
		}
	}

	// CAPI names machines, so metadata wins over the cmdline default.
	hostname := cfg.Hostname
	if metaData.Hostname != "" {
		hostname = metaData.Hostname
	}
	if err := s.Sethostname(hostname); err != nil {
		logf("warn: sethostname %q: %v", hostname, err)
	} else {
		logf("hostname set to %q", hostname)
		rec.SetHostname(hostname)
	}

	// Reaper: SIGCHLD -> coalescing trigger -> drain wait4.
	chld := make(chan os.Signal, 1)
	signal.Notify(chld, unix.SIGCHLD)
	defer signal.Stop(chld)
	trigger := make(chan struct{}, 1)
	go pump(chld, trigger)
	go reaper.Run(runCtx, s, trigger)

	// Started before the child so a host shutdown request is never missed while
	// k0s is coming up, which can take minutes.
	hostCmds := watchControlPort(runCtx, s, cfg)
	powerBtn := watchPowerButton(runCtx)

	// runcmd is interpreted, never executed: k0smos does not exec binaries named
	// in user-data, so machine state stays a function of its configuration and
	// the image needs neither a shell nor coreutils.
	plan := userData.Plan()
	for _, cmd := range plan.Unsupported {
		logf("user-data: UNSUPPORTED runcmd %v, skipped", cmd)
	}
	for _, err := range metadata.RunActions(s, plan.Actions) {
		logf("warn: user-data action: %v", err)
	}
	if n := len(plan.Actions); n > 0 {
		logf("applied %d file action(s) from runcmd", n)
	}
	// A workload described by user-data wins: that is CAPI telling this machine
	// whether it is a control plane or a worker, and with which join token.
	workloadCmd := cfg.Exec
	if len(plan.Workload) > 0 {
		workloadCmd = plan.Workload
		logf("workload from user-data")
	}
	if len(plan.Env) > 0 {
		logf("workload env from user-data: %v", plan.Env)
	}

	logf("supervising %v", workloadCmd)
	rec.SetChild(workloadCmd)
	childDone := make(chan struct{})
	go func() {
		defer close(childDone)
		_ = supervise.Run(runCtx, supervise.Options{
			Command:    workloadCmd[0],
			Args:       workloadCmd[1:],
			Env:        plan.Env,
			MaxBackoff: 10 * time.Second,
			OnExit: func(err error) {
				logf("child exited: %v", err)
				rec.ChildExited(err)
			},
		})
	}()

	// Wait for a shutdown request. SIGINT is what the kernel delivers to PID1
	// on ctrl-alt-del; SIGTERM is the conventional request; the control port is
	// how the host asks, since this guest has no working power button.
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	how := shutdown.PowerOff
wait:
	for {
		select {
		case sig := <-term:
			logf("got %v, shutting down", sig)
			break wait
		case cmd, ok := <-hostCmds:
			// A closed channel yields the zero Command, which must not be
			// mistaken for a request: that alone once powered the machine off
			// seconds into boot with nothing connected to the port.
			if !ok {
				hostCmds = nil // nil channel blocks, disabling this case
				continue
			}
			logf("host requested %v", cmd)
			if cmd == control.Reboot {
				how = shutdown.Reboot
			}
			break wait
		case _, ok := <-powerBtn:
			// Same closed-channel hazard as above: a closed channel yields a
			// zero value that must not be read as a button press.
			if !ok {
				powerBtn = nil
				continue
			}
			logf("power button pressed, shutting down")
			break wait
		case <-ctx.Done():
			logf("context cancelled, shutting down")
			break wait
		}
	}

	// Give up etcd membership while the controller is still running — it cannot
	// leave once it has been stopped. Nothing on this machine persists, so a
	// member that vanishes without leaving would sit in the member list counting
	// against quorum forever.
	leaveEtcd(workloadCmd)

	stopChild()
	select {
	case <-childDone:
	case <-time.After(childGrace):
		logf("warn: child did not exit within %s", childGrace)
	}

	logf("syncing and unmounting")
	return shutdown.Do(shutdownAdapter{s}, how)
}

// applyMachineConfig overlays fields explicitly supplied by cloud-config. An
// empty field means "keep the artifact default", matching cmdline parsing and
// letting a drive set only the second NIC without restating DNS or the gateway.
func applyMachineConfig(cfg *config.Config, machine metadata.MachineConfig) {
	if machine.IP != "" {
		cfg.IP = machine.IP
	}
	if machine.Iface != "" {
		cfg.Iface = machine.Iface
	}
	if machine.Gateway != "" {
		cfg.Gateway = machine.Gateway
	}
	if machine.DNS != "" {
		cfg.DNS = machine.DNS
	}
}
