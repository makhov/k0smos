package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/iso9660"
)

// runGen executes the gen command as a user would, returning its output and error.
func runGen(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := genCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A drive that would do nothing must be refused. cloud-config without its
// "#cloud-config" first line is ignored by the node, so writing one produces a
// machine that boots unconfigured with only a console warning to say why — the
// failure this check exists to prevent.
func TestGenRefusesUserDataThatWouldBeIgnored(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(src, []byte("write_files:\n  - path: /etc/x\n    content: y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(dir, "out.iso")

	out, err := runGen(t, "--user-data", src, "-o", iso)
	if err == nil {
		t.Fatal("accepted user-data the node will ignore")
	}
	if !strings.Contains(err.Error(), "no effect") {
		t.Errorf("error = %v, want it to say the drive would have no effect", err)
	}
	if !strings.Contains(out, "not a #cloud-config document") {
		t.Errorf("the underlying warning was not shown:\n%s", out)
	}
	// And nothing may be left behind: a rejected drive that exists anyway is
	// worse than none, because it will be booted.
	if _, err := os.Stat(iso); !os.IsNotExist(err) {
		t.Error("wrote an image it had rejected")
	}
}

func TestGenRejectsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(src, []byte("#cloud-config\nwrite_files: [oh dear\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGen(t, "--user-data", src, "-o", filepath.Join(dir, "out.iso")); err == nil {
		t.Fatal("accepted malformed YAML")
	}
}

// --file is the path that needs no cloud-config written by hand, so what it
// renders has to be right: the destination path, the source's permissions, and
// content that survives being carried through YAML.
func TestGenFileRendersWriteFiles(t *testing.T) {
	dir := t.TempDir()
	// Content chosen to break naive inlining: a tab, a colon, a leading dash.
	content := "a:\tb\n- item: value\n"
	src := filepath.Join(dir, "k0s.yaml")
	if err := os.WriteFile(src, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(dir, "out.iso")

	if _, err := runGen(t, "--file", src+":/etc/k0s/k0s.yaml", "--hostname", "node-1", "-o", iso); err != nil {
		t.Fatal(err)
	}

	img, err := os.ReadFile(iso)
	if err != nil {
		t.Fatal(err)
	}
	r, err := iso9660.Open(fileAt(img))
	if err != nil {
		t.Fatal(err)
	}

	got, err := r.ReadFile("user-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"path: /etc/k0s/k0s.yaml",
		`permissions: "0600"`, // the source file's mode, not a default
		"encoding: b64",
		base64.StdEncoding.EncodeToString([]byte(content)),
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("user-data missing %q:\n%s", want, got)
		}
	}

	meta, err := r.ReadFile("meta-data")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), "local-hostname: node-1") {
		t.Errorf("meta-data = %q, want the hostname", meta)
	}
}

func TestGenRejectsBadArguments(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(dir, "out.iso")

	for name, args := range map[string][]string{
		"nothing to do":         {"-o", iso},
		"both sources":          {"--user-data", src, "--file", src + ":/etc/x", "-o", iso},
		"file without a colon":  {"--file", src, "-o", iso},
		"relative destination":  {"--file", src + ":etc/x", "-o", iso},
		"source does not exist": {"--file", "/nonexistent:/etc/x", "-o", iso},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runGen(t, args...); err == nil {
				t.Error("accepted bad arguments")
			}
		})
	}
}

// fileAt adapts a byte slice to what iso9660.Open reads from.
type fileAt []byte

func (f fileAt) ReadAt(p []byte, off int64) error {
	if off < 0 || off+int64(len(p)) > int64(len(f)) {
		return os.ErrInvalid
	}
	copy(p, f[off:])
	return nil
}
