package metadata

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// This is the shape of bootstrap data CAPI providers emit: a cloud-config
// document that writes files and then runs commands.
const k0sUserData = `#cloud-config
write_files:
  - path: /etc/k0s/k0s.yaml
    permissions: "0644"
    content: |
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
  - path: /etc/k0s/join-token
    permissions: "0600"
    content: c2VjcmV0
runcmd:
  - k0s install controller --enable-worker
  - [k0s, start]
`

func TestParseUserDataWriteFiles(t *testing.T) {
	got, err := ParseUserData([]byte(k0sUserData))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WriteFiles) != 2 {
		t.Fatalf("write_files = %d, want 2", len(got.WriteFiles))
	}
	f := got.WriteFiles[0]
	if f.Path != "/etc/k0s/k0s.yaml" {
		t.Errorf("path = %q", f.Path)
	}
	if !strings.Contains(f.Content, "ClusterConfig") {
		t.Errorf("content = %q, want the cluster config", f.Content)
	}
	if f.Mode() != 0644 {
		t.Errorf("mode = %o, want 0644", f.Mode())
	}
	if got.WriteFiles[1].Mode() != 0600 {
		t.Errorf("mode = %o, want 0600", got.WriteFiles[1].Mode())
	}
}

func TestParseUserDataMachineNetwork(t *testing.T) {
	doc := `#cloud-config
k0smos:
  ip: eth0:dhcp,eth1:10.10.0.11/24
  iface: eth0
  gateway: 10.0.2.2
  dns: 1.1.1.1
`
	got, err := ParseUserData([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := MachineConfig{
		IP: "eth0:dhcp,eth1:10.10.0.11/24", Iface: "eth0",
		Gateway: "10.0.2.2", DNS: "1.1.1.1",
	}
	if got.Machine != want {
		t.Errorf("machine config = %#v, want %#v", got.Machine, want)
	}
}

// runcmd entries come as either a bare string or an argv list. Both must yield
// an argv, because k0smos has no shell to hand a string to.
func TestParseUserDataRuncmdBothForms(t *testing.T) {
	got, err := ParseUserData([]byte(k0sUserData))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RunCmd) != 2 {
		t.Fatalf("runcmd = %d entries, want 2", len(got.RunCmd))
	}
	want := []string{"k0s", "install", "controller", "--enable-worker"}
	if !slices.Equal(got.RunCmd[0], want) {
		t.Errorf("runcmd[0] = %v, want %v", got.RunCmd[0], want)
	}
	if !slices.Equal(got.RunCmd[1], []string{"k0s", "start"}) {
		t.Errorf("runcmd[1] = %v, want [k0s start]", got.RunCmd[1])
	}
}

// A string command that needs a shell cannot be honoured faithfully without
// one, so it must be rejected rather than silently mis-executed.
func TestParseUserDataRejectsShellSyntax(t *testing.T) {
	for _, cmd := range []string{
		"k0s status | grep -q Running",
		"echo hi > /tmp/x",
		"a && b",
		"foo $(bar)",
	} {
		doc := "#cloud-config\nruncmd:\n  - " + cmd + "\n"
		got, err := ParseUserData([]byte(doc))
		if err != nil {
			t.Fatalf("%q: %v", cmd, err)
		}
		if len(got.RunCmd) != 0 {
			t.Errorf("%q was accepted as %v; want it skipped", cmd, got.RunCmd)
		}
		if len(got.Warnings) == 0 {
			t.Errorf("%q was skipped without a warning", cmd)
		}
	}
}

func TestParseUserDataBase64Content(t *testing.T) {
	doc := `#cloud-config
write_files:
  - path: /etc/token
    encoding: b64
    content: aGVsbG8=
`
	got, err := ParseUserData([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got.WriteFiles[0].Content != "hello" {
		t.Errorf("content = %q, want hello", got.WriteFiles[0].Content)
	}
}

// Kubernetes manifests shipped through write_files are exactly the content
// people compress, and cloud-init spells the encoding several ways.
func TestParseUserDataGzipEncodings(t *testing.T) {
	// gzip("apiVersion: v1\nkind: Namespace\n") base64-encoded.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte("apiVersion: v1\nkind: Namespace\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	for _, enc := range []string{"gz+base64", "gzip+base64", "gz+b64", "gzip+b64"} {
		doc := "#cloud-config\nwrite_files:\n  - path: /var/lib/k0s/manifests/x/ns.yaml\n" +
			"    encoding: " + enc + "\n    content: " + b64 + "\n"
		got, err := ParseUserData([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", enc, err)
		}
		if len(got.WriteFiles) != 1 {
			t.Fatalf("%s: skipped (%v)", enc, got.Warnings)
		}
		if !strings.Contains(got.WriteFiles[0].Content, "kind: Namespace") {
			t.Errorf("%s: content = %q", enc, got.WriteFiles[0].Content)
		}
	}
}

// Bare "gz"/"gzip" is deliberately not supported: the content field arrives as
// a JSON string (sigs.k8s.io/yaml converts YAML to JSON), which cannot carry raw
// compressed bytes. That is why providers always use the base64 pairing.
func TestParseUserDataBareGzipIsUnsupported(t *testing.T) {
	doc := "#cloud-config\nwrite_files:\n  - path: /x\n    encoding: gzip\n    content: whatever\n"
	got, err := ParseUserData([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WriteFiles) != 0 || len(got.Warnings) == 0 {
		t.Errorf("files = %v, warnings = %v; want it skipped with a warning", got.WriteFiles, got.Warnings)
	}
}

func TestParseUserDataRejectsCorruptGzip(t *testing.T) {
	doc := "#cloud-config\nwrite_files:\n  - path: /x\n    encoding: gzip+base64\n    content: " +
		base64.StdEncoding.EncodeToString([]byte("not gzip at all")) + "\n"
	got, err := ParseUserData([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WriteFiles) != 0 {
		t.Error("accepted corrupt gzip content")
	}
	if len(got.Warnings) == 0 {
		t.Error("skipped without a warning")
	}
}

func TestWriteFileDefaultMode(t *testing.T) {
	doc := "#cloud-config\nwrite_files:\n  - path: /etc/x\n    content: y\n"
	got, err := ParseUserData([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got.WriteFiles[0].Mode() != 0644 {
		t.Errorf("mode = %o, want 0644 by default", got.WriteFiles[0].Mode())
	}
}

// Not every provider sends a cloud-config; empty or non-cloud-config data must
// not fail the boot.
func TestParseUserDataToleratesNonCloudConfig(t *testing.T) {
	for _, doc := range []string{"", "#!/bin/sh\necho hi\n", "just text"} {
		if _, err := ParseUserData([]byte(doc)); err != nil {
			t.Errorf("ParseUserData(%q) = %v, want nil", doc, err)
		}
	}
}

func TestParseUserDataRejectsMalformedYAML(t *testing.T) {
	if _, err := ParseUserData([]byte("#cloud-config\nwrite_files: [oops\n")); err == nil {
		t.Error("accepted malformed YAML")
	}
}

// NoCloud meta-data is YAML; the OpenStack variant is JSON. sigs.k8s.io/yaml
// parses both, since JSON is a subset.
func TestParseMetaDataHostname(t *testing.T) {
	for _, doc := range []string{
		"instance-id: i-123\nlocal-hostname: node-07\n",
		`{"uuid":"i-123","hostname":"node-07"}`,
	} {
		got, err := ParseMetaData([]byte(doc))
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got.Hostname != "node-07" {
			t.Errorf("%q: hostname = %q, want node-07", doc, got.Hostname)
		}
		if got.InstanceID != "i-123" {
			t.Errorf("%q: instance id = %q, want i-123", doc, got.InstanceID)
		}
	}
}

func TestParseMetaDataEmpty(t *testing.T) {
	got, err := ParseMetaData(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "" {
		t.Errorf("hostname = %q, want empty", got.Hostname)
	}
}
