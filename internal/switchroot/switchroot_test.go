package switchroot

import (
	"errors"
	"os"
	"slices"
	"testing"
)

type fakeSwitcher struct {
	ops     []string
	execArg []string
	failOn  string
	err     error
}

func (f *fakeSwitcher) record(op string) error {
	f.ops = append(f.ops, op)
	if f.failOn == op {
		return f.err
	}
	return nil
}

func (f *fakeSwitcher) Mount(source, target, _ string, flags uintptr, _ string) error {
	if flags&msMove != 0 {
		return f.record("move:" + source + "->" + target)
	}
	return f.record("mount:" + source + "->" + target)
}
func (f *fakeSwitcher) Mkdir(p string, _ os.FileMode) error { return f.record("mkdir:" + p) }
func (f *fakeSwitcher) Chdir(dir string) error              { return f.record("chdir:" + dir) }
func (f *fakeSwitcher) Chroot(dir string) error             { return f.record("chroot:" + dir) }
func (f *fakeSwitcher) Exec(argv0 string, argv, _ []string) error {
	f.execArg = argv
	return f.record("exec:" + argv0)
}

// The sequence is what makes switch_root work; any reordering leaves the new
// root un-pivoted or the kernel filesystems unreachable.
func TestDoMovesKernelMountsThenPivotsAndExecs(t *testing.T) {
	f := &fakeSwitcher{}
	err := Do(f, "/newroot", "/sbin/k0smos", []string{"k0smos", "--switched-root"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		// Kernel filesystems must follow us across, or the new init has no
		// /proc, /sys or /dev.
		"move:/dev->/newroot/dev",
		"move:/proc->/newroot/proc",
		"move:/sys->/newroot/sys",
		// Then make the new root the actual root and enter it.
		"chdir:/newroot",
		"move:.->/",
		"chroot:.",
		"chdir:/",
		"exec:/sbin/k0smos",
	}
	if !slices.Equal(f.ops, want) {
		t.Errorf("ops =\n  %v\nwant\n  %v", f.ops, want)
	}
	if !slices.Equal(f.execArg, []string{"k0smos", "--switched-root"}) {
		t.Errorf("exec argv = %v", f.execArg)
	}
}

// Exec replaces the process, so returning at all means it failed.
func TestDoReportsExecFailure(t *testing.T) {
	f := &fakeSwitcher{failOn: "exec:/sbin/k0smos", err: errors.New("no such file")}
	err := Do(f, "/newroot", "/sbin/k0smos", []string{"k0smos"})
	if err == nil {
		t.Fatal("Do = nil, want error when exec fails")
	}
}

func TestDoFailsWhenRootCannotBePivoted(t *testing.T) {
	f := &fakeSwitcher{failOn: "move:.->/", err: errors.New("EINVAL")}
	if err := Do(f, "/newroot", "/sbin/k0smos", []string{"k0smos"}); err == nil {
		t.Fatal("Do = nil, want error when the root move fails")
	}
}

// A missing /dev or /proc in the new root should not abort the switch: the
// mount points are created rather than treated as fatal.
func TestDoToleratesUnmovableKernelMount(t *testing.T) {
	f := &fakeSwitcher{failOn: "move:/dev->/newroot/dev", err: errors.New("ENOENT")}
	if err := Do(f, "/newroot", "/sbin/k0smos", []string{"k0smos"}); err != nil {
		t.Fatalf("Do = %v, want nil (a kernel mount move is best-effort)", err)
	}
	if !slices.Contains(f.ops, "exec:/sbin/k0smos") {
		t.Errorf("did not reach exec: %v", f.ops)
	}
}
