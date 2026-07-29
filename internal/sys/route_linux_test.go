//go:build linux

package sys

import (
	"testing"
	"unsafe"
)

// rtentry is hand-declared to match <linux/route.h>. If the layout drifts the
// kernel reads garbage from a syscall argument, so pin size and offsets.
func TestRtentryLayoutMatchesKernel(t *testing.T) {
	var rt rtentry
	if got, want := unsafe.Sizeof(rt), uintptr(120); got != want {
		t.Errorf("sizeof(rtentry) = %d, want %d", got, want)
	}
	for _, tc := range []struct {
		name   string
		offset uintptr
		want   uintptr
	}{
		{"dst", unsafe.Offsetof(rt.dst), 8},
		{"gateway", unsafe.Offsetof(rt.gateway), 24},
		{"genmask", unsafe.Offsetof(rt.genmask), 40},
		{"flags", unsafe.Offsetof(rt.flags), 56},
		{"metric", unsafe.Offsetof(rt.metric), 80},
		{"dev", unsafe.Offsetof(rt.dev), 88},
	} {
		if tc.offset != tc.want {
			t.Errorf("offsetof(%s) = %d, want %d", tc.name, tc.offset, tc.want)
		}
	}
}
