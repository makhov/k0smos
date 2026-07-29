# k0smos PID1 Init — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A minimal Go PID1 (`k0smos`) that boots a single-node k0s inside a VM — mounts pseudo-filesystems, sets up cgroup2, reaps zombies, supervises the k0s binary, and shuts down cleanly.

**Architecture:** k0smos runs as PID1. It performs OS init (mounts, cgroups, networking, `/etc` seed), installs a zombie reaper and signal handlers, then execs `k0s controller --single` as a supervised child. All real syscalls live in `internal/sys`; every other package depends only on a narrow local interface it defines, so logic is unit-testable with a fake and no root/VM. A QEMU boot script is the end-to-end acceptance test.

**Tech Stack:** Go (static, `CGO_ENABLED=0`), `golang.org/x/sys/unix`, QEMU for acceptance.

## Global Constraints

- Module path: `github.com/amakhov/k0smos`.
- Go >= 1.25 (the `go` directive is driven by `golang.org/x/sys`, which requires 1.25.0; originally specced as 1.24 but that is infeasible with the pinned dependency). `CGO_ENABLED=0`, build fully static (`-ldflags '-extldflags "-static"'`).
- Linux-only. Non-linux files must not break `go build`/`go vet` on the dev Mac — guard OS-specific code with `//go:build linux` and provide the interface types in OS-neutral files.
- **No shell, no busybox** in the produced image.
- PID1 must never return from a running state; fatal init errors panic (kernel shows it on console), shutdown ends in `reboot(2)`.
- Only dependency beyond stdlib: `golang.org/x/sys`. No other third-party libs.
- TDD: failing test first, minimal impl, frequent commits.

### Shared interface contract (referenced by multiple tasks)

`internal/sys` exports one concrete real implementation. Its method set (the union every consumer draws from):

```go
// package sys
type Sys struct{} // real, //go:build linux

func New() *Sys

func (s *Sys) Getpid() int
func (s *Sys) Mount(source, target, fstype string, flags uintptr, data string) error
func (s *Sys) Unmount(target string, flags int) error
func (s *Sys) Mkdir(path string, perm os.FileMode) error
func (s *Sys) WriteFile(path string, data []byte, perm os.FileMode) error
func (s *Sys) Mounts() ([]MountPoint, error) // parses /proc/self/mountinfo
func (s *Sys) Sethostname(name string) error
func (s *Sys) LinkUp(name string) error      // bring a network iface up
func (s *Sys) Sync()
func (s *Sys) Reboot(cmd int) error
func (s *Sys) Reap() (pid int, ok bool, err error) // wait4(-1,WNOHANG); ok=false when no child ready

type MountPoint struct{ Source, Target, FSType string }
```

Each consumer package defines its **own** minimal interface (subset of the above) and a fake in its `_test.go`. `*sys.Sys` satisfies all of them.

---

### Task 1: Repo scaffold + PID1 gate

**Files:**
- Create: `/Users/amakhov/work/k0smos/go.mod`
- Create: `/Users/amakhov/work/k0smos/.gitignore`
- Create: `/Users/amakhov/work/k0smos/Makefile`
- Create: `/Users/amakhov/work/k0smos/cmd/k0smos/main.go`
- Test: `/Users/amakhov/work/k0smos/cmd/k0smos/main_test.go`

**Interfaces:**
- Produces: `func gate(pid int) error` — returns error when `pid != 1`.

- [ ] **Step 1: Write the failing test**

`cmd/k0smos/main_test.go`:
```go
package main

import "testing"

func TestGateRejectsNonPID1(t *testing.T) {
	if err := gate(4242); err == nil {
		t.Fatal("expected error when pid != 1, got nil")
	}
}

func TestGateAcceptsPID1(t *testing.T) {
	if err := gate(1); err != nil {
		t.Fatalf("expected nil for pid 1, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./cmd/k0smos/`
Expected: FAIL — `undefined: gate` (and no go.mod yet → create it in step 3).

- [ ] **Step 3: Create scaffold + minimal implementation**

`go.mod`:
```
module github.com/amakhov/k0smos

go 1.24

require golang.org/x/sys v0.30.0
```

`.gitignore`:
```
/dist/
/build/
*.img
*.qcow2
vmlinuz*
```

`cmd/k0smos/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

func gate(pid int) error {
	if pid != 1 {
		return fmt.Errorf("k0smos is an init (PID1), not a CLI; got pid %d", pid)
	}
	return nil
}

func main() {
	if err := gate(os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// init sequence wired in Task 10
}
```

`Makefile`:
```makefile
BIN := dist/k0smos
GO_BUILD := CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build test vet
build:
	$(GO_BUILD) -o $(BIN) ./cmd/k0smos

test:
	go test ./...

vet:
	go vet ./...
```

- [ ] **Step 4: Fetch dep and run tests**

Run: `cd /Users/amakhov/work/k0smos && go mod tidy && go test ./cmd/k0smos/`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add go.mod go.sum .gitignore Makefile cmd/k0smos/
git commit -m "feat: scaffold k0smos repo and PID1 gate"
```

---

### Task 2: internal/sys — real syscaller + mountinfo parser

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/sys/sys.go` (OS-neutral types)
- Create: `/Users/amakhov/work/k0smos/internal/sys/sys_linux.go` (real impl)
- Create: `/Users/amakhov/work/k0smos/internal/sys/mountinfo.go` (pure parser)
- Test: `/Users/amakhov/work/k0smos/internal/sys/mountinfo_test.go`

**Interfaces:**
- Produces: `sys.MountPoint`, `sys.New()`, `*sys.Sys` (contract above), `sys.parseMountInfo([]byte) ([]MountPoint, error)`.

- [ ] **Step 1: Write the failing test**

`internal/sys/mountinfo_test.go`:
```go
package sys

import "testing"

func TestParseMountInfo(t *testing.T) {
	// fields: id parent maj:min root mountpoint opts... - fstype source super
	data := []byte(
		"22 28 0:21 / /proc rw,nosuid - proc proc rw\n" +
			"24 28 0:6 / /dev rw - devtmpfs devtmpfs rw\n")
	mps, err := parseMountInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(mps) != 2 {
		t.Fatalf("want 2 mounts, got %d", len(mps))
	}
	if mps[0].Target != "/proc" || mps[0].FSType != "proc" {
		t.Errorf("mount0 = %+v", mps[0])
	}
	if mps[1].Target != "/dev" || mps[1].FSType != "devtmpfs" {
		t.Errorf("mount1 = %+v", mps[1])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/sys/`
Expected: FAIL — `undefined: parseMountInfo`.

- [ ] **Step 3: Implement**

`internal/sys/sys.go`:
```go
package sys

// MountPoint is one entry from /proc/self/mountinfo.
type MountPoint struct {
	Source string
	Target string
	FSType string
}
```

`internal/sys/mountinfo.go`:
```go
package sys

import (
	"bufio"
	"bytes"
	"fmt"
)

// parseMountInfo parses /proc/self/mountinfo content. The optional fields
// between the mountpoint and the " - " separator are variable-length, so we
// split on the separator to locate fstype/source reliably.
func parseMountInfo(data []byte) ([]MountPoint, error) {
	var out []MountPoint
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		sep := bytes.Index([]byte(line), []byte(" - "))
		if sep < 0 {
			return nil, fmt.Errorf("mountinfo: no separator in %q", line)
		}
		left := line[:sep]
		right := line[sep+3:]
		lf := splitFields(left)
		rf := splitFields(right)
		if len(lf) < 5 || len(rf) < 2 {
			return nil, fmt.Errorf("mountinfo: short line %q", line)
		}
		out = append(out, MountPoint{Target: lf[4], FSType: rf[0], Source: rf[1]})
	}
	return out, sc.Err()
}

func splitFields(s string) []string {
	var f []string
	for _, tok := range bytes.Fields([]byte(s)) {
		f = append(f, string(tok))
	}
	return f
}
```

`internal/sys/sys_linux.go`:
```go
//go:build linux

package sys

import (
	"os"

	"golang.org/x/sys/unix"
)

type Sys struct{}

func New() *Sys { return &Sys{} }

func (s *Sys) Getpid() int { return os.Getpid() }

func (s *Sys) Mount(source, target, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

func (s *Sys) Unmount(target string, flags int) error { return unix.Unmount(target, flags) }

func (s *Sys) Mkdir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (s *Sys) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (s *Sys) Mounts() ([]MountPoint, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountInfo(data)
}

func (s *Sys) Sethostname(name string) error { return unix.Sethostname([]byte(name)) }

func (s *Sys) Sync() { unix.Sync() }

func (s *Sys) Reboot(cmd int) error { return unix.Reboot(cmd) }

// Reap collects one exited child. ok=false means "no child ready right now".
func (s *Sys) Reap() (int, bool, error) {
	var ws unix.WaitStatus
	pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
	if err == unix.ECHILD {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if pid <= 0 {
		return 0, false, nil
	}
	return pid, true, nil
}

// LinkUp brings a network interface up via ioctl SIOCSIFFLAGS.
func (s *Sys) LinkUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/sys/ && GOOS=linux go build ./...`
Expected: PASS; linux build succeeds.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/sys/
git commit -m "feat: sys syscaller and mountinfo parser"
```

---

### Task 3: internal/mount — pseudo-filesystem mounts

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/mount/mount.go`
- Test: `/Users/amakhov/work/k0smos/internal/mount/mount_test.go`

**Interfaces:**
- Consumes: `sys.MountPoint` and a mounter interface (subset of `*sys.Sys`).
- Produces: `mount.Mounter` interface, `mount.Ensure(m Mounter) error`, `mount.Default []Spec`, `mount.Spec`.

- [ ] **Step 1: Write the failing test**

`internal/mount/mount_test.go`:
```go
package mount

import (
	"os"
	"testing"

	"github.com/amakhov/k0smos/internal/sys"
)

type fakeMounter struct {
	existing []sys.MountPoint
	mounted  []string
	mkdirs   []string
}

func (f *fakeMounter) Mounts() ([]sys.MountPoint, error) { return f.existing, nil }
func (f *fakeMounter) Mkdir(p string, _ os.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeMounter) Mount(_, target, _ string, _ uintptr, _ string) error {
	f.mounted = append(f.mounted, target)
	return nil
}

func TestEnsureMountsMissingSkipsExisting(t *testing.T) {
	f := &fakeMounter{existing: []sys.MountPoint{{Target: "/proc"}}}
	if err := Ensure(f); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range f.mounted {
		if tgt == "/proc" {
			t.Error("/proc already mounted, should have been skipped")
		}
	}
	want := map[string]bool{"/sys": false, "/dev": false, "/run": false}
	for _, tgt := range f.mounted {
		if _, ok := want[tgt]; ok {
			want[tgt] = true
		}
	}
	for tgt, seen := range want {
		if !seen {
			t.Errorf("expected %s to be mounted", tgt)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/mount/`
Expected: FAIL — `undefined: Ensure`.

- [ ] **Step 3: Implement**

`internal/mount/mount.go`:
```go
package mount

import (
	"fmt"
	"os"

	"github.com/amakhov/k0smos/internal/sys"
)

// Mounter is the subset of *sys.Sys that mounting needs.
type Mounter interface {
	Mounts() ([]sys.MountPoint, error)
	Mkdir(path string, perm os.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
}

// Spec is one pseudo-filesystem to mount.
type Spec struct {
	Source string
	Target string
	FSType string
	Flags  uintptr
	Data   string
	Perm   os.FileMode
}

// Default is the base set every PID1 boot needs. cgroup2 is handled separately
// (internal/cgroup) because it needs post-mount controller setup.
var Default = []Spec{
	{Source: "proc", Target: "/proc", FSType: "proc", Perm: 0555},
	{Source: "sysfs", Target: "/sys", FSType: "sysfs", Perm: 0555},
	{Source: "devtmpfs", Target: "/dev", FSType: "devtmpfs", Perm: 0755},
	{Source: "devpts", Target: "/dev/pts", FSType: "devpts", Perm: 0755},
	{Source: "tmpfs", Target: "/dev/shm", FSType: "tmpfs", Perm: 0755},
	{Source: "tmpfs", Target: "/run", FSType: "tmpfs", Perm: 0755},
	{Source: "tmpfs", Target: "/tmp", FSType: "tmpfs", Perm: 01777},
}

// Ensure mounts every Default spec not already present.
func Ensure(m Mounter) error {
	existing, err := m.Mounts()
	if err != nil {
		return fmt.Errorf("read mounts: %w", err)
	}
	have := map[string]bool{}
	for _, mp := range existing {
		have[mp.Target] = true
	}
	for _, s := range Default {
		if have[s.Target] {
			continue
		}
		if err := m.Mkdir(s.Target, s.Perm); err != nil {
			return fmt.Errorf("mkdir %s: %w", s.Target, err)
		}
		if err := m.Mount(s.Source, s.Target, s.FSType, s.Flags, s.Data); err != nil {
			return fmt.Errorf("mount %s: %w", s.Target, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/mount/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/mount/
git commit -m "feat: pseudo-filesystem mount setup"
```

---

### Task 4: internal/cgroup — cgroup2 unified + controllers

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/cgroup/cgroup.go`
- Test: `/Users/amakhov/work/k0smos/internal/cgroup/cgroup_test.go`

**Interfaces:**
- Consumes: a cgroup mounter interface (subset of `*sys.Sys`).
- Produces: `cgroup.Setup(c Controller) error`, `cgroup.Controller` interface.

- [ ] **Step 1: Write the failing test**

`internal/cgroup/cgroup_test.go`:
```go
package cgroup

import (
	"os"
	"strings"
	"testing"
)

type fakeCgroup struct {
	mountedTarget string
	writes        map[string]string
}

func (f *fakeCgroup) Mkdir(string, os.FileMode) error { return nil }
func (f *fakeCgroup) Mount(_, target, fstype string, _ uintptr, _ string) error {
	f.mountedTarget = target
	return nil
}
func (f *fakeCgroup) WriteFile(path string, data []byte, _ os.FileMode) error {
	f.writes[path] = string(data)
	return nil
}

func TestSetupMountsAndEnablesControllers(t *testing.T) {
	f := &fakeCgroup{writes: map[string]string{}}
	if err := Setup(f); err != nil {
		t.Fatal(err)
	}
	if f.mountedTarget != "/sys/fs/cgroup" {
		t.Errorf("mounted %q, want /sys/fs/cgroup", f.mountedTarget)
	}
	got := f.writes["/sys/fs/cgroup/cgroup.subtree_control"]
	for _, ctrl := range []string{"+cpu", "+memory", "+pids", "+io"} {
		if !strings.Contains(got, ctrl) {
			t.Errorf("subtree_control %q missing %s", got, ctrl)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/cgroup/`
Expected: FAIL — `undefined: Setup`.

- [ ] **Step 3: Implement**

`internal/cgroup/cgroup.go`:
```go
package cgroup

import (
	"fmt"
	"os"
)

const (
	root    = "/sys/fs/cgroup"
	subtree = root + "/cgroup.subtree_control"
)

// Controller is the subset of *sys.Sys that cgroup setup needs.
type Controller interface {
	Mkdir(path string, perm os.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// Setup mounts the cgroup2 unified hierarchy and delegates the core
// controllers to child cgroups so containerd/kubelet/runc can use them.
func Setup(c Controller) error {
	if err := c.Mkdir(root, 0755); err != nil {
		return fmt.Errorf("mkdir cgroup root: %w", err)
	}
	if err := c.Mount("cgroup2", root, "cgroup2", 0, "nsdelegate"); err != nil {
		return fmt.Errorf("mount cgroup2: %w", err)
	}
	if err := c.WriteFile(subtree, []byte("+cpu +memory +pids +io"), 0644); err != nil {
		return fmt.Errorf("enable controllers: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/cgroup/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/cgroup/
git commit -m "feat: cgroup2 unified hierarchy setup"
```

---

### Task 5: internal/net — loopback up

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/net/net.go`
- Test: `/Users/amakhov/work/k0smos/internal/net/net_test.go`

**Interfaces:**
- Consumes: a linker interface (subset of `*sys.Sys`).
- Produces: `net.Up(l Linker) error`, `net.Linker` interface.

- [ ] **Step 1: Write the failing test**

`internal/net/net_test.go`:
```go
package net

import "testing"

type fakeLinker struct{ up []string }

func (f *fakeLinker) LinkUp(name string) error {
	f.up = append(f.up, name)
	return nil
}

func TestUpBringsLoopbackUp(t *testing.T) {
	f := &fakeLinker{}
	if err := Up(f); err != nil {
		t.Fatal(err)
	}
	if len(f.up) != 1 || f.up[0] != "lo" {
		t.Errorf("brought up %v, want [lo]", f.up)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/net/`
Expected: FAIL — `undefined: Up`.

- [ ] **Step 3: Implement**

`internal/net/net.go`:
```go
package net

import "fmt"

// Linker is the subset of *sys.Sys that network setup needs.
type Linker interface {
	LinkUp(name string) error
}

// Up brings the loopback interface up. The primary NIC is configured by the
// kernel `ip=` cmdline parameter at boot in the MVP.
func Up(l Linker) error {
	if err := l.LinkUp("lo"); err != nil {
		return fmt.Errorf("lo up: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/net/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/net/
git commit -m "feat: bring loopback up"
```

---

### Task 6: internal/config — kernel cmdline parsing

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/config/config.go`
- Test: `/Users/amakhov/work/k0smos/internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config` struct, `config.Parse(cmdline string) Config`.

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:
```go
package config

import "testing"

func TestParseReadsHostnameAndDefaults(t *testing.T) {
	c := Parse("root=/dev/vda ip=dhcp k0smos.hostname=node1 quiet")
	if c.Hostname != "node1" {
		t.Errorf("hostname = %q, want node1", c.Hostname)
	}
}

func TestParseDefaultsHostname(t *testing.T) {
	c := Parse("root=/dev/vda")
	if c.Hostname != "k0smos" {
		t.Errorf("hostname = %q, want default k0smos", c.Hostname)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/config/`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 3: Implement**

`internal/config/config.go`:
```go
package config

import "strings"

// Config holds k0smos knobs parsed from the kernel cmdline (k0smos.* keys).
type Config struct {
	Hostname string
}

// Parse extracts k0smos.* parameters from a kernel cmdline string.
func Parse(cmdline string) Config {
	c := Config{Hostname: "k0smos"}
	for _, tok := range strings.Fields(cmdline) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || !strings.HasPrefix(k, "k0smos.") {
			continue
		}
		switch strings.TrimPrefix(k, "k0smos.") {
		case "hostname":
			c.Hostname = v
		}
	}
	return c
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/config/
git commit -m "feat: parse k0smos kernel cmdline params"
```

---

### Task 7: internal/reaper — zombie reaper loop

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/reaper/reaper.go`
- Test: `/Users/amakhov/work/k0smos/internal/reaper/reaper_test.go`

**Interfaces:**
- Consumes: a reaper interface (subset of `*sys.Sys`).
- Produces: `reaper.Reaper` interface, `reaper.Run(ctx context.Context, r Reaper, trigger <-chan struct{})`.

The reaper loop is triggered by a channel (fed from a SIGCHLD handler in Task 10) and drains all ready children each time.

- [ ] **Step 1: Write the failing test**

`internal/reaper/reaper_test.go`:
```go
package reaper

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeReaper struct {
	mu     sync.Mutex
	queue  []int // pids to hand out, one per Reap until empty
	reaped []int
}

func (f *fakeReaper) Reap() (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return 0, false, nil
	}
	pid := f.queue[0]
	f.queue = f.queue[1:]
	f.reaped = append(f.reaped, pid)
	return pid, true, nil
}

func TestRunDrainsAllReadyChildren(t *testing.T) {
	f := &fakeReaper{queue: []int{10, 11, 12}}
	trigger := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { Run(ctx, f, trigger); close(done) }()

	trigger <- struct{}{}
	// give the loop time to drain
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		n := len(f.reaped)
		f.mu.Unlock()
		if n == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("reaped %d, want 3", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/reaper/`
Expected: FAIL — `undefined: Run`.

- [ ] **Step 3: Implement**

`internal/reaper/reaper.go`:
```go
package reaper

import "context"

// Reaper is the subset of *sys.Sys that reaping needs.
type Reaper interface {
	Reap() (pid int, ok bool, err error)
}

// Run drains exited children whenever trigger fires (or ctx is done). As PID1
// we inherit all orphans; each SIGCHLD may cover several exits, so we loop
// Reap() until it reports no child ready.
func Run(ctx context.Context, r Reaper, trigger <-chan struct{}) {
	drain := func() {
		for {
			_, ok, err := r.Reap()
			if err != nil || !ok {
				return
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			drain()
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/reaper/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/reaper/
git commit -m "feat: zombie reaper loop"
```

---

### Task 8: internal/supervise — supervise k0s child with restart

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/supervise/supervise.go`
- Test: `/Users/amakhov/work/k0smos/internal/supervise/supervise_test.go`

**Interfaces:**
- Produces: `supervise.Options`, `supervise.Run(ctx context.Context, opts Options) error`. `Options.start` is an injectable child-runner: `func(ctx context.Context) error`; the exported `Command`/`Args` build the real runner. Restart uses capped backoff via an injectable `sleep func(time.Duration)`.

- [ ] **Step 1: Write the failing test**

`internal/supervise/supervise_test.go`:
```go
package supervise

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunRestartsUntilContextCancelled(t *testing.T) {
	calls := 0
	opts := Options{
		start: func(ctx context.Context) error {
			calls++
			return errors.New("child died")
		},
		sleep:      func(time.Duration) {},
		MaxBackoff: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// let it restart a few times, then cancel
		for calls < 3 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	err := Run(ctx, opts)
	if err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}
	if calls < 3 {
		t.Fatalf("child started %d times, want >=3", calls)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/supervise/`
Expected: FAIL — `undefined: Options`.

- [ ] **Step 3: Implement**

`internal/supervise/supervise.go`:
```go
package supervise

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// Options configures the supervised child. Command/Args describe the real
// child; start/sleep are injectable seams for testing.
type Options struct {
	Command    string
	Args       []string
	MaxBackoff time.Duration

	start func(ctx context.Context) error
	sleep func(time.Duration)
}

// Run supervises the child, restarting it with capped exponential backoff
// until ctx is cancelled. Returns nil on clean context cancellation.
func Run(ctx context.Context, opts Options) error {
	if opts.start == nil {
		opts.start = func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	if opts.sleep == nil {
		opts.sleep = time.Sleep
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 10 * time.Second
	}

	backoff := 200 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = opts.start(ctx)
		if ctx.Err() != nil {
			return nil
		}
		opts.sleep(backoff)
		if backoff *= 2; backoff > opts.MaxBackoff {
			backoff = opts.MaxBackoff
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/supervise/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/supervise/
git commit -m "feat: supervise k0s child with restart backoff"
```

---

### Task 9: internal/shutdown — graceful poweroff/reboot

**Files:**
- Create: `/Users/amakhov/work/k0smos/internal/shutdown/shutdown.go`
- Test: `/Users/amakhov/work/k0smos/internal/shutdown/shutdown_test.go`

**Interfaces:**
- Consumes: a shutdowner interface (subset of `*sys.Sys`).
- Produces: `shutdown.Shutdowner` interface, `shutdown.Do(s Shutdowner, cmd int) error`, constants `shutdown.PowerOff`, `shutdown.Reboot`.

Design note: `Shutdowner.Mounts()` returns `[]string` (mount targets only) so this package imports nothing from `internal/sys` and stays cycle-free. `*sys.Sys` satisfies it via the `MountTargets()` adapter added in Task 10.

- [ ] **Step 1: Write the failing test**

`internal/shutdown/shutdown_test.go`:
```go
package shutdown

import (
	"testing"

	"golang.org/x/sys/unix"
)

type fakeShutdowner struct {
	order      []string
	unmounted  []string
	rebootWith int
}

func (f *fakeShutdowner) Mounts() ([]string, error) {
	return []string{"/var/lib/k0s", "/proc"}, nil
}
func (f *fakeShutdowner) Sync() { f.order = append(f.order, "sync") }
func (f *fakeShutdowner) Unmount(target string, _ int) error {
	f.order = append(f.order, "unmount:"+target)
	f.unmounted = append(f.unmounted, target)
	return nil
}
func (f *fakeShutdowner) Reboot(cmd int) error {
	f.order = append(f.order, "reboot")
	f.rebootWith = cmd
	return nil
}

func TestDoSyncsUnmountsThenReboots(t *testing.T) {
	f := &fakeShutdowner{}
	if err := Do(f, PowerOff); err != nil {
		t.Fatal(err)
	}
	if f.order[0] != "sync" {
		t.Errorf("first op %q, want sync", f.order[0])
	}
	if f.order[len(f.order)-1] != "reboot" {
		t.Errorf("last op %q, want reboot", f.order[len(f.order)-1])
	}
	// /proc is pseudo → must be skipped; /var/lib/k0s must be unmounted.
	if len(f.unmounted) != 1 || f.unmounted[0] != "/var/lib/k0s" {
		t.Errorf("unmounted = %v, want [/var/lib/k0s]", f.unmounted)
	}
	if f.rebootWith != unix.LINUX_REBOOT_CMD_POWER_OFF {
		t.Errorf("reboot cmd = %d, want POWER_OFF", f.rebootWith)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/shutdown/`
Expected: FAIL — `undefined: Do`.

- [ ] **Step 3: Implement**

`internal/shutdown/shutdown.go`:
```go
package shutdown

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	PowerOff = unix.LINUX_REBOOT_CMD_POWER_OFF
	Reboot   = unix.LINUX_REBOOT_CMD_RESTART
)

// Shutdowner is the subset of *sys.Sys that shutdown needs. Mounts returns
// mount targets as strings so this package imports nothing from internal/sys.
type Shutdowner interface {
	Mounts() ([]string, error)
	Sync()
	Unmount(target string, flags int) error
	Reboot(cmd int) error
}

// Do flushes disks, unmounts writable filesystems (best-effort, pseudo-fs
// skipped), then issues reboot(2) with cmd. reboot(2) does not return on
// success in production; Do returns only so fakes can assert the sequence.
func Do(s Shutdowner, cmd int) error {
	s.Sync()
	targets, err := s.Mounts()
	if err != nil {
		return fmt.Errorf("read mounts: %w", err)
	}
	for _, target := range targets {
		if isPseudo(target) {
			continue
		}
		_ = s.Unmount(target, unix.MNT_DETACH) // best-effort
	}
	s.Sync()
	return s.Reboot(cmd)
}

func isPseudo(target string) bool {
	for _, p := range []string{"/proc", "/sys", "/dev", "/run", "/tmp"} {
		if target == p || strings.HasPrefix(target, p+"/") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/shutdown/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add internal/shutdown/
git commit -m "feat: graceful shutdown sync+unmount+reboot"
```

---

### Task 10: cmd/k0smos — wire the init sequence

**Files:**
- Modify: `/Users/amakhov/work/k0smos/cmd/k0smos/main.go`
- Create: `/Users/amakhov/work/k0smos/cmd/k0smos/init_linux.go`
- Create: `/Users/amakhov/work/k0smos/internal/sys/targets.go` (add `MountTargets()` helper on `*sys.Sys` returning `[]string`)
- Test: `/Users/amakhov/work/k0smos/internal/sys/targets_test.go`

**Interfaces:**
- Consumes: everything from Tasks 2–9.
- Produces: `func boot(ctx context.Context, s *sys.Sys, cmdline string) error` orchestrating the sequence; `(*sys.Sys).MountTargets() ([]string, error)` so `*sys.Sys` satisfies `shutdown.Shutdowner`.

Because `shutdown.Shutdowner.Mounts()` returns `[]string`, add an adapter method `MountTargets` on `*sys.Sys` and pass a small wrapper into `shutdown.Do`. The wrapper is defined in `init_linux.go`.

- [ ] **Step 1: Write the failing test (targets helper)**

`internal/sys/targets_test.go`:
```go
package sys

import "testing"

func TestMountTargetsExtractsTargets(t *testing.T) {
	// exercise the pure extraction the helper delegates to
	mps := []MountPoint{{Target: "/proc"}, {Target: "/var/lib/k0s"}}
	got := targetsOf(mps)
	if len(got) != 2 || got[0] != "/proc" || got[1] != "/var/lib/k0s" {
		t.Errorf("targetsOf = %v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/amakhov/work/k0smos && go test ./internal/sys/`
Expected: FAIL — `undefined: targetsOf`.

- [ ] **Step 3: Implement helper + wiring**

`internal/sys/targets.go`:
```go
package sys

func targetsOf(mps []MountPoint) []string {
	out := make([]string, 0, len(mps))
	for _, mp := range mps {
		out = append(out, mp.Target)
	}
	return out
}
```

Add to `internal/sys/sys_linux.go`:
```go
// MountTargets returns just the mount targets, for shutdown unmounting.
func (s *Sys) MountTargets() ([]string, error) {
	mps, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	return targetsOf(mps), nil
}
```

`cmd/k0smos/init_linux.go`:
```go
//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/amakhov/k0smos/internal/cgroup"
	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/mount"
	knet "github.com/amakhov/k0smos/internal/net"
	"github.com/amakhov/k0smos/internal/reaper"
	"github.com/amakhov/k0smos/internal/shutdown"
	"github.com/amakhov/k0smos/internal/supervise"
	"github.com/amakhov/k0smos/internal/sys"

	"golang.org/x/sys/unix"
)

// shutdownAdapter lets *sys.Sys satisfy shutdown.Shutdowner (Mounts()[]string).
type shutdownAdapter struct{ *sys.Sys }

func (a shutdownAdapter) Mounts() ([]string, error) { return a.Sys.MountTargets() }

func boot(ctx context.Context, s *sys.Sys, cmdline string) error {
	if err := mount.Ensure(s); err != nil {
		return fmt.Errorf("mounts: %w", err)
	}
	if err := cgroup.Setup(s); err != nil {
		return fmt.Errorf("cgroup: %w", err)
	}
	if err := knet.Up(s); err != nil {
		return fmt.Errorf("net: %w", err)
	}
	cfg := config.Parse(cmdline)
	if err := s.Sethostname(cfg.Hostname); err != nil {
		fmt.Fprintf(os.Stderr, "warn: sethostname: %v\n", err)
	}

	// reaper: SIGCHLD -> trigger channel
	chld := make(chan os.Signal, 1)
	signal.Notify(chld, unix.SIGCHLD)
	trigger := make(chan struct{}, 1)
	go func() {
		for range chld {
			select {
			case trigger <- struct{}{}:
			default:
			}
		}
	}()
	go reaper.Run(ctx, s, trigger)

	// supervise k0s
	go func() {
		_ = supervise.Run(ctx, supervise.Options{
			Command:    "/usr/local/bin/k0s",
			Args:       []string{"controller", "--single"},
			MaxBackoff: 10 * time.Second,
		})
	}()

	// wait for shutdown signal
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	<-term
	return shutdown.Do(shutdownAdapter{s}, shutdown.PowerOff)
}
```

Rewrite `cmd/k0smos/main.go` main() to call boot on linux. Keep `gate` as-is; add:
```go
func main() {
	if err := gate(os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	if err := run(context.Background(), string(cmdline)); err != nil {
		panic(err) // PID1: surface on console; kernel handles the rest
	}
}
```
Add `run` in `init_linux.go`:
```go
func run(ctx context.Context, cmdline string) error {
	return boot(ctx, sys.New(), cmdline)
}
```
And a non-linux stub `cmd/k0smos/init_other.go`:
```go
//go:build !linux

package main

import (
	"context"
	"errors"
)

func run(context.Context, string) error { return errors.New("k0smos runs on linux only") }
```
Update `main.go` imports (`context`) and remove the old trailing comment.

- [ ] **Step 4: Build + test everything**

Run: `cd /Users/amakhov/work/k0smos && go test ./... && GOOS=linux go vet ./... && GOOS=linux CGO_ENABLED=0 go build ./...`
Expected: all tests PASS; linux vet + build succeed.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add cmd/k0smos/ internal/sys/
git commit -m "feat: wire k0smos PID1 init sequence"
```

---

### Task 11: image/ — rootfs assembly + QEMU acceptance

**Files:**
- Create: `/Users/amakhov/work/k0smos/image/mkrootfs.sh`
- Create: `/Users/amakhov/work/k0smos/image/k0s.yaml`
- Create: `/Users/amakhov/work/k0smos/image/run-qemu.sh`
- Create: `/Users/amakhov/work/k0smos/image/acceptance.sh`
- Modify: `/Users/amakhov/work/k0smos/Makefile` (add `image`, `accept` targets)

**Interfaces:**
- Consumes: `dist/k0smos` (Task 1 build), a `k0s` linux binary (downloaded), a distro kernel + virtio (host-provided).

This task's "test" is the end-to-end acceptance run; there is no Go unit test.

- [ ] **Step 1: Write the rootfs assembler**

`image/mkrootfs.sh`:
```bash
#!/usr/bin/env bash
# Assemble a minimal ext4 rootfs image containing only k0smos + k0s.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(mktemp -d)
img=${1:-dist/k0smos.img}
k0s_bin=${K0S_BIN:?set K0S_BIN to a linux k0s binary path}

mkdir -p "$root"/{sbin,usr/local/bin,etc/k0s,proc,sys,dev,run,tmp,var/lib/k0s}
cp dist/k0smos "$root/sbin/k0smos"
cp "$k0s_bin" "$root/usr/local/bin/k0s"
cp "$here/k0s.yaml" "$root/etc/k0s/k0s.yaml"
printf 'k0smos\n' > "$root/etc/hostname"
printf '127.0.0.1 localhost\n' > "$root/etc/hosts"
printf 'nameserver 1.1.1.1\n' > "$root/etc/resolv.conf"
printf 'NAME=k0smos\nID=k0smos\n' > "$root/etc/os-release"

# size: k0s binary is large (embedded bins); pad generously
size_mb=$(( $(du -sm "$root" | cut -f1) + 512 ))
mkdir -p "$(dirname "$img")"
rm -f "$img"
truncate -s "${size_mb}M" "$img"
mkfs.ext4 -q -d "$root" "$img"
rm -rf "$root"
echo "wrote $img (${size_mb}M)"
```

`image/k0s.yaml` (minimal single-node; k0s defaults are fine, keep explicit):
```yaml
apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
metadata:
  name: k0smos
spec:
  storage:
    type: kine
```

- [ ] **Step 2: Write the QEMU runner**

`image/run-qemu.sh`:
```bash
#!/usr/bin/env bash
# Boot the k0smos image under QEMU with direct kernel boot.
# Requires: KERNEL=/path/to/vmlinuz (with virtio-blk/virtio-net built in).
set -euo pipefail
img=${1:-dist/k0smos.img}
kernel=${KERNEL:?set KERNEL to a bzImage/vmlinuz with virtio built in}

exec qemu-system-x86_64 \
  -m 4096 -smp 2 \
  -enable-kvm 2>/dev/null || true
qemu-system-x86_64 \
  -m 4096 -smp 2 \
  -kernel "$kernel" \
  -append "root=/dev/vda rw init=/sbin/k0smos ip=dhcp console=ttyS0" \
  -drive file="$img",if=virtio,format=raw \
  -netdev user,id=n0,hostfwd=tcp::6443-:6443 -device virtio-net,netdev=n0 \
  -nographic -serial mon:stdio
```

- [ ] **Step 3: Write the acceptance check**

`image/acceptance.sh`:
```bash
#!/usr/bin/env bash
# Boot k0smos in QEMU, wait until the node is Ready, then power off.
# Captures serial console to dist/console.log and greps for readiness.
set -euo pipefail
log=dist/console.log
: > "$log"

# Run QEMU in background, tee serial to log.
KERNEL="${KERNEL:?}" ./image/run-qemu.sh dist/k0smos.img | tee "$log" &
qpid=$!

ok=0
for _ in $(seq 1 120); do   # up to ~10 min
  if grep -q "Reached Ready state\|node.*Ready\|k0s controller.*started" "$log"; then
    ok=1; break
  fi
  sleep 5
done

kill "$qpid" 2>/dev/null || true
wait "$qpid" 2>/dev/null || true

if [ "$ok" -ne 1 ]; then
  echo "ACCEPTANCE FAIL: readiness marker not found in $log" >&2
  tail -50 "$log" >&2
  exit 1
fi
echo "ACCEPTANCE PASS"
```

Note: the exact readiness string depends on k0s log output. During first run, inspect `dist/console.log` and tighten the grep to a stable line (e.g. from `k0s status` or the kubelet "Node became ready" log). Adjust the pattern before declaring the task done.

- [ ] **Step 4: Wire Makefile + run acceptance**

Add to `Makefile`:
```makefile
.PHONY: image accept
image: build
	chmod +x image/*.sh
	./image/mkrootfs.sh dist/k0smos.img

accept: image
	./image/acceptance.sh
```

Run (on a Linux host with QEMU + a virtio kernel + a k0s linux binary):
```bash
cd /Users/amakhov/work/k0smos
K0S_BIN=/path/to/k0s KERNEL=/path/to/vmlinuz make accept
```
Expected: `ACCEPTANCE PASS`. If the marker is wrong, tighten the grep per the Step 3 note and re-run.

- [ ] **Step 5: Commit**

```bash
cd /Users/amakhov/work/k0smos
git add image/ Makefile
git commit -m "feat: rootfs image assembly and QEMU acceptance test"
```

---

## Self-Review

**Spec coverage:**
- PID1 gate → Task 1. Mounts → Task 3. cgroup2 → Task 4. Net (lo) → Task 5. `/etc` seed (hostname) → Task 10 (hosts/resolv/os-release baked by Task 11 mkrootfs). Reaper → Task 7. Signals → Task 10. Supervise k0s child → Task 8/10. Shutdown → Task 9. Config (cmdline) → Task 6. Image + boot → Task 11. Testing (fast fakes + QEMU acceptance) → every Go task + Task 11. All spec M1 sections covered.
- Deferred-by-spec items (config-drive, immutable rootfs, monolithic kernel, in-init DHCP, multi-node) correctly absent — they are M2.

**Placeholder scan:** No TBD/TODO. One "adjust after first run" note (readiness grep in Task 11) is a concrete instruction — the marker string depends on real k0s log output, so the executor must tighten the grep against `dist/console.log` before declaring the task done.

**Type consistency:** `*sys.Sys` method set (contract section) is the superset; each package interface (`mount.Mounter`, `cgroup.Controller`, `net.Linker`, `reaper.Reaper`, `shutdown.Shutdowner`) is a verified subset. `shutdown.Shutdowner.Mounts()` returns `[]string`, satisfied by `shutdownAdapter.Mounts()` → `(*sys.Sys).MountTargets()` (Task 10). `reaper.Reap()` signature `(int, bool, error)` matches `(*sys.Sys).Reap()`. `supervise.Options.start`/`sleep` seams match test usage.
