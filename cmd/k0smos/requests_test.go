package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListForHostNamesKindsAndTargets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "k0s.yaml"), []byte("spec: {}\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "pki"), 0755); err != nil {
		t.Fatal(err)
	}
	// What /sys and /etc/resolv.conf look like: the target is the answer.
	if err := os.Symlink("/run/k0smos/resolv.conf", filepath.Join(dir, "resolv.conf")); err != nil {
		t.Fatal(err)
	}
	out, err := listForHost(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"pki/", "resolv.conf -> /run/k0smos/resolv.conf", "k0s.yaml", "-rw-r-----"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing lacks %q:\n%s", want, got)
		}
	}
	if n := len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")); n != 3 {
		t.Errorf("got %d lines, want 3:\n%s", n, got)
	}
}

func TestListForHostRejectsRelativeAndFiles(t *testing.T) {
	if _, err := listForHost("var/lib"); err == nil {
		t.Error("a relative path must be refused")
	}
	if _, err := listForHost(""); err == nil {
		t.Error("an empty path must be refused")
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := listForHost(file); err == nil {
		t.Error("a file must be refused by ls")
	}
}

// The two mistakes are symmetric, so each error names the other request.
func TestCatAndLsPointAtEachOther(t *testing.T) {
	dir := t.TempDir()
	_, err := readForHost(dir)
	if err == nil || !strings.Contains(err.Error(), "ls "+dir) {
		t.Errorf("cat of a directory should suggest ls, got %v", err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := listForHost(file); err == nil {
		t.Error("ls of a file must fail")
	}
}

func TestDialForHostReportsBothEnds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	out, err := dialForHost(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.HasPrefix(got, "connected ") || !strings.Contains(got, "-> "+ln.Addr().String()) {
		t.Errorf("got %q, want the local and remote addresses", got)
	}
}

// A refusal is a different answer from a name that does not resolve, and the
// wording is the whole value of the request, so it is passed through unwrapped.
func TestDialForHostReturnsTheStacksWording(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if _, err := dialForHost(addr); err == nil {
		t.Error("dialling a closed port must fail")
	} else if !strings.Contains(err.Error(), addr) {
		t.Errorf("error does not name the address: %v", err)
	}
	if _, err := dialForHost("nocolon"); err == nil {
		t.Error("an address without a port must be refused")
	}
}

// A /dev listing has to be readable by someone who knows ls, and a character
// device must not be a column wider than everything around it.
func TestModeStringMatchesLs(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		want string
	}{
		{0644, "-rw-r--r--"},
		{os.ModeDir | 0755, "drwxr-xr-x"},
		{os.ModeSymlink | 0777, "lrwxrwxrwx"},
		{os.ModeSocket | 0660, "srw-rw----"},
		{os.ModeDevice | os.ModeCharDevice | 0666, "crw-rw-rw-"},
		{os.ModeDevice | 0660, "brw-rw----"},
		{os.ModeNamedPipe | 0644, "prw-r--r--"},
	} {
		if got := modeString(tc.mode); got != tc.want {
			t.Errorf("modeString(%v) = %q, want %q", tc.mode, got, tc.want)
		} else if len(got) != 10 {
			t.Errorf("modeString(%v) = %q, %d columns not 10", tc.mode, got, len(got))
		}
	}
}
