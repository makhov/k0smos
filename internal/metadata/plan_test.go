package metadata

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
)

// Bootstrap providers assume systemd: they run `k0s install <role>` to write a
// unit, then `k0s start`. k0smos supervises a single child instead, so the
// install form is interpreted as the equivalent foreground command.
func TestPlanTranslatesK0sInstall(t *testing.T) {
	p := UserData{RunCmd: [][]string{
		{"k0s", "install", "controller", "--enable-worker", "--config", "/etc/k0s/k0s.yaml"},
		{"k0s", "start"},
	}}.Plan()

	want := []string{"k0s", "controller", "--enable-worker", "--config", "/etc/k0s/k0s.yaml"}
	if !slices.Equal(p.Workload, want) {
		t.Errorf("workload = %v, want %v", p.Workload, want)
	}
	if len(p.Unsupported) != 0 {
		t.Errorf("unsupported = %v, want none", p.Unsupported)
	}
}

func TestPlanHandlesWorkerRole(t *testing.T) {
	p := UserData{RunCmd: [][]string{
		{"/usr/local/bin/k0s", "install", "worker", "--token-file", "/etc/k0s/join-token"},
	}}.Plan()
	want := []string{"/usr/local/bin/k0s", "worker", "--token-file", "/etc/k0s/join-token"}
	if !slices.Equal(p.Workload, want) {
		t.Errorf("workload = %v, want %v", p.Workload, want)
	}
}

// Nothing is executed: the recognised file verbs become typed actions carried
// out in Go, because the image ships no coreutils and no shell.
func TestPlanInterpretsFileVerbs(t *testing.T) {
	p := UserData{RunCmd: [][]string{
		{"mkdir", "-p", "/var/lib/foo"},
		{"mkdir", "/var/lib/bar"},
		{"chmod", "0600", "/etc/k0s/token"},
		{"chown", "0:0", "/etc/k0s/token"},
		{"ln", "-s", "/opt/thing", "/usr/local/bin/thing"},
	}}.Plan()

	if len(p.Actions) != 5 {
		t.Fatalf("actions = %d (%v), want 5", len(p.Actions), p.Actions)
	}
	if p.Actions[0].Kind != Mkdir || p.Actions[0].Path != "/var/lib/foo" {
		t.Errorf("actions[0] = %+v", p.Actions[0])
	}
	if p.Actions[2].Kind != Chmod || p.Actions[2].Mode != fs.FileMode(0600) {
		t.Errorf("actions[2] = %+v, want chmod 0600", p.Actions[2])
	}
	if p.Actions[3].Kind != Chown || p.Actions[3].UID != 0 || p.Actions[3].GID != 0 {
		t.Errorf("actions[3] = %+v, want chown 0:0", p.Actions[3])
	}
	if p.Actions[4].Kind != Symlink || p.Actions[4].Target != "/opt/thing" {
		t.Errorf("actions[4] = %+v, want symlink", p.Actions[4])
	}
}

// mkdir/chmod accept several paths at once.
func TestPlanExpandsMultiplePaths(t *testing.T) {
	p := UserData{RunCmd: [][]string{
		{"mkdir", "-p", "/a", "/b", "/c"},
		{"chmod", "0755", "/a", "/b"},
	}}.Plan()
	if len(p.Actions) != 5 {
		t.Fatalf("actions = %v, want 3 mkdirs + 2 chmods", p.Actions)
	}
}

func TestPlanAcceptsNamedOwners(t *testing.T) {
	p := UserData{RunCmd: [][]string{{"chown", "root:root", "/etc/x"}}}.Plan()
	if len(p.Actions) != 1 || p.Actions[0].UID != 0 || p.Actions[0].GID != 0 {
		t.Errorf("actions = %+v, want chown 0:0", p.Actions)
	}
}

// Anything not recognised is reported and skipped. k0smos never execs a binary
// named in user-data, which is what keeps machine state a function of config.
func TestPlanRefusesUnrecognisedCommands(t *testing.T) {
	cmds := [][]string{
		{"curl", "-o", "/tmp/x", "https://example.com"},
		{"sed", "-i", "s/a/b/", "/etc/x"},
		{"/opt/custom/setup.sh"},
		{"chown", "bob:bob", "/etc/x"}, // unknown user
		{"chmod", "notoctal", "/etc/x"},
		{"mkdir"}, // no path
	}
	p := UserData{RunCmd: cmds}.Plan()
	if len(p.Actions) != 0 {
		t.Errorf("actions = %+v, want none", p.Actions)
	}
	if len(p.Unsupported) != len(cmds) {
		t.Errorf("unsupported = %d (%v), want %d", len(p.Unsupported), p.Unsupported, len(cmds))
	}
}

func TestPlanEmptyWhenNoInstall(t *testing.T) {
	p := UserData{RunCmd: [][]string{{"mkdir", "/tmp/x"}}}.Plan()
	if p.Workload != nil {
		t.Errorf("workload = %v, want nil so the caller keeps its default", p.Workload)
	}
}

// --- applying actions ---

type fakeApplier struct {
	calls []string
	err   error
}

func (f *fakeApplier) MkdirAll(p string, m fs.FileMode) error {
	f.calls = append(f.calls, "mkdir:"+p)
	return f.err
}
func (f *fakeApplier) Chmod(p string, m fs.FileMode) error {
	f.calls = append(f.calls, "chmod:"+p)
	return f.err
}
func (f *fakeApplier) Chown(p string, uid, gid int) error {
	f.calls = append(f.calls, "chown:"+p)
	return f.err
}
func (f *fakeApplier) Symlink(target, link string) error {
	f.calls = append(f.calls, "symlink:"+link)
	return f.err
}

func TestRunActionsAppliesInOrder(t *testing.T) {
	actions := []Action{
		{Kind: Mkdir, Path: "/a"},
		{Kind: Chmod, Path: "/b", Mode: 0600},
		{Kind: Chown, Path: "/c"},
		{Kind: Symlink, Target: "/t", Path: "/l"},
	}
	f := &fakeApplier{}
	if errs := RunActions(f, actions); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	want := []string{"mkdir:/a", "chmod:/b", "chown:/c", "symlink:/l"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

// One failing action must not stop the rest: partial setup with a reported
// failure beats abandoning the boot.
func TestRunActionsReportsAllFailures(t *testing.T) {
	f := &fakeApplier{err: errors.New("EACCES")}
	errs := RunActions(f, []Action{{Kind: Mkdir, Path: "/a"}, {Kind: Mkdir, Path: "/b"}})
	if len(errs) != 2 {
		t.Errorf("errors = %v, want 2", errs)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls = %v, want both attempted", f.calls)
	}
}
