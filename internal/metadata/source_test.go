package metadata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNoCloudLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "user-data", "#cloud-config\nruncmd:\n  - [k0s, install, controller]\n")
	write(t, dir, "meta-data", "instance-id: i-1\nlocal-hostname: node-1\n")

	ud, md, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if md.Hostname != "node-1" {
		t.Errorf("hostname = %q, want node-1", md.Hostname)
	}
	if len(ud.RunCmd) != 1 {
		t.Errorf("runcmd = %v, want one entry", ud.RunCmd)
	}
}

// The OpenStack config-drive puts the same information at different paths.
func TestLoadConfigDriveLayout(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "openstack", "latest")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	write(t, sub, "user_data", "#cloud-config\nruncmd:\n  - [k0s, install, worker]\n")
	write(t, sub, "meta_data.json", `{"uuid":"i-2","hostname":"node-2"}`)

	ud, md, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if md.Hostname != "node-2" {
		t.Errorf("hostname = %q, want node-2", md.Hostname)
	}
	argv, _ := ud.Workload()
	if len(argv) != 2 || argv[1] != "worker" {
		t.Errorf("workload = %v, want [k0s worker]", argv)
	}
}

// A drive with neither layout is not an error: it may be some other disk.
func TestLoadEmptyDrive(t *testing.T) {
	ud, md, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ud.RunCmd) != 0 || md.Hostname != "" {
		t.Errorf("expected nothing from an empty drive, got %v / %v", ud, md)
	}
}

func TestApplyWritesFilesWithModes(t *testing.T) {
	dir := t.TempDir()
	ud := UserData{WriteFiles: []WriteFile{
		{Path: filepath.Join(dir, "etc", "k0s", "k0s.yaml"), Content: "config", Permissions: "0644"},
		{Path: filepath.Join(dir, "etc", "k0s", "token"), Content: "secret", Permissions: "0600"},
	}}
	if err := Apply(ud, os.MkdirAll, os.WriteFile); err != nil {
		t.Fatal(err)
	}
	for _, f := range ud.WriteFiles {
		info, err := os.Stat(f.Path)
		if err != nil {
			t.Fatalf("%s: %v", f.Path, err)
		}
		if info.Mode().Perm() != f.Mode() {
			t.Errorf("%s mode = %o, want %o", f.Path, info.Mode().Perm(), f.Mode())
		}
		got, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != f.Content {
			t.Errorf("%s content = %q, want %q", f.Path, got, f.Content)
		}
	}
}

// A file whose parent does not exist must still be written: bootstrap data
// routinely targets directories the image does not ship.
func TestApplyCreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "file")
	err := Apply(UserData{WriteFiles: []WriteFile{{Path: deep, Content: "x"}}}, os.MkdirAll, os.WriteFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deep); err != nil {
		t.Error(err)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
