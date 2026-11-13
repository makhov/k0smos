package grubenv

import (
	"strings"
	"testing"
)

func TestEncodeIsWhatGrubEditenvWrites(t *testing.T) {
	b, err := Encode("k0smos_kernel_args", `hugepages=512 spec_rstack_overflow=safe-ret x=a\b`)
	if err != nil {
		t.Fatal(err)
	}
	want := "# GRUB Environment Block\nk0smos_kernel_args=hugepages=512 spec_rstack_overflow=safe-ret x=a\\\\b\n"
	if len(b) != Size || !strings.HasPrefix(string(b), want) || strings.Trim(string(b[len(want):]), "#") != "" {
		t.Errorf("block = %q", b)
	}
	if v, ok := Get(b, "k0smos_kernel_args"); !ok || v != `hugepages=512 spec_rstack_overflow=safe-ret x=a\b` {
		t.Errorf("Get = %q, %v", v, ok)
	}
	if _, ok := Get(b, "other"); ok {
		t.Error("Get found a key that is not there")
	}
}

func TestEncodeRefusesWhatGrubCannotHold(t *testing.T) {
	if _, err := Encode("k", "a\nb"); err == nil {
		t.Error("a newline was accepted")
	}
	if _, err := Encode("k", strings.Repeat("x", Size)); err == nil {
		t.Error("an oversized value was accepted")
	}
}

func TestGetIgnoresWhatIsNotABlock(t *testing.T) {
	if _, ok := Get([]byte("k=v\n"), "k"); ok {
		t.Error("read a key from a file with no header")
	}
}
