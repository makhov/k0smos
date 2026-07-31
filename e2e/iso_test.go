//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/iso9660"
)

// nativeCidata writes a cloud-init drive with k0smos's own ISO writer, which is
// what k0smosctl uses. The other tests build theirs with xorriso on purpose — an
// independent implementation on the other side of the reader — so this is the one
// place the writer itself is under test.
func nativeCidata(t *testing.T, userData, metaData string) string {
	t.Helper()
	if metaData == "" {
		metaData = "instance-id: e2e-native\n"
	}
	outDir := filepath.Join(repoRoot(t), "dist", "e2e")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(outDir, sanitise(t.Name())+"-native.iso")
	t.Cleanup(func() { os.Remove(iso) })

	f, err := os.Create(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = iso9660.Write(f, "cidata", []iso9660.File{
		{Name: "user-data", Data: []byte(userData)},
		{Name: "meta-data", Data: []byte(metaData)},
	})
	if err != nil {
		t.Fatalf("write iso: %v", err)
	}
	return iso
}

// A drive we generate must be readable by an implementation that is not ours.
// This is not redundant with the unit tests: those round-trip through k0smos's own
// reader, which ignores anything it does not need — so an image missing the ER
// entry that declares Rock Ridge passed every one of them while xorriso reported
// each filename mangled to USER_DATA.
func TestGeneratedDriveIsReadableByXorriso(t *testing.T) {
	iso := nativeCidata(t, "#cloud-config\nwrite_files:\n  - path: /etc/demo\n    content: hi\n", "")

	out, err := exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(iso)+":/d", "alpine:3.20", "sh", "-c",
		"apk add -q --no-cache xorriso >/dev/null 2>&1 && "+
			"xorriso -indev /d/"+filepath.Base(iso)+" -find / 2>/dev/null",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso: %v\n%s", err, out)
	}
	got := string(out)

	// The Rock Ridge names, not the ISO9660 mangling.
	for _, want := range []string{"'/user-data'", "'/meta-data'"} {
		if !strings.Contains(got, want) {
			t.Errorf("xorriso did not report %s — Rock Ridge is not being honoured:\n%s", want, got)
		}
	}
	if strings.Contains(got, "USER_DATA") {
		t.Errorf("xorriso reported the mangled name, so it ignored our Rock Ridge:\n%s", got)
	}
}

// The whole k0smosctl path, end to end: build the binary, generate a drive from a
// host file, boot it, and check the file arrived with its permissions intact. This
// is the workflow that replaces hand-rolling an ISO with xorriso, so it is the one
// that has to keep working.
func TestCLIGeneratedDriveBoots(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	root := repoRoot(t)
	disk := cloneDisk(t, filepath.Join(root, "dist/k0smos.img"))

	bin := filepath.Join(t.TempDir(), "k0smosctl")
	build := exec.Command("go", "build", "-o", bin, "./cmd/k0smosctl")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build k0smosctl: %v\n%s", err, out)
	}

	// 0600, so the test proves the source file's mode is carried across rather
	// than everything landing world-readable.
	src := filepath.Join(t.TempDir(), "k0s.yaml")
	if err := os.WriteFile(src, []byte("apiVersion: k0s.k0sproject.io/v1beta1\nkind: ClusterConfig\n"), 0600); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(root, "dist", "e2e")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(outDir, sanitise(t.Name())+"-cli.iso")
	t.Cleanup(func() { os.Remove(iso) })

	gen := exec.Command(bin, "gen",
		"-file", src+":/etc/k0s/from-cli.yaml",
		"-hostname", "cli-node",
		"-o", iso)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("k0smosctl gen: %v\n%s", err, out)
	}

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`reading /dev/vd\w+ \(iso9660, LABEL=cidata\) directly, no mount`, bootTimeout)
	v.waitFor(`wrote 1 file\(s\) from user-data`, bootTimeout)
	v.waitFor(`hostname set to "cli-node"`, bootTimeout)
	v.stop()

	if got := debugfsCmd(t, disk, "cat /etc/k0s/from-cli.yaml"); !strings.Contains(got, "ClusterConfig") {
		t.Errorf("file content not on disk: %q", got)
	}
	if ls := debugfsCmd(t, disk, "ls -l /etc/k0s"); !strings.Contains(ls, "100600") {
		t.Errorf("source file mode 0600 was not preserved:\n%s", ls)
	}
}

// And the drive must work where it actually matters: attached to a booting node.
// This closes the loop k0smosctl depends on — our writer, the kernel's view of the
// disk, and our reader on the other side.
func TestNativelyGeneratedDriveBoots(t *testing.T) {
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	disk := cloneDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))

	iso := nativeCidata(t, `#cloud-config
write_files:
  - path: /etc/k0s/native.txt
    permissions: "0600"
    content: |
      written from a drive k0smos generated itself
`, "instance-id: i-native\nlocal-hostname: native-node\n")

	v := boot(t, bootOpts{Disk: disk, Cidata: iso, Exec: execNoop})
	v.waitFor(`reading /dev/vd\w+ \(iso9660, LABEL=cidata\) directly, no mount`, bootTimeout)
	v.waitFor(`wrote 1 file\(s\) from user-data`, bootTimeout)
	// meta-data was read too, which only works if both files are findable by
	// their Rock Ridge names.
	v.waitFor(`hostname set to "native-node"`, bootTimeout)
	v.stop()

	if got := debugfsCmd(t, disk, "cat /etc/k0s/native.txt"); !strings.Contains(got, "generated itself") {
		t.Errorf("file content not on disk: %q", got)
	}
	if ls := debugfsCmd(t, disk, "ls -l /etc/k0s"); !strings.Contains(ls, "100600") {
		t.Errorf("mode not applied:\n%s", ls)
	}
}
