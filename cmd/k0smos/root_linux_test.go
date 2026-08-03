//go:build linux

package main

import (
	"testing"

	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/metadata"
)

func TestSelectRootPrecedence(t *testing.T) {
	tests := []struct {
		name                       string
		explicit                   string
		embedded                   bool
		wantSpec                   string
		wantEmbedded, wantDisabled bool
	}{
		{
			name:     "explicit override wins over embedded",
			explicit: "UUID=01234567-89ab-cdef-0123-456789abcdef",
			embedded: true,
			wantSpec: "UUID=01234567-89ab-cdef-0123-456789abcdef",
		},
		{name: "embedded root", embedded: true, wantEmbedded: true},
		{name: "canonical disk fallback", wantSpec: canonicalRootSpec},
		{name: "initramfs only", explicit: noRootSpec, embedded: true, wantDisabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSpec, gotEmbedded, gotDisabled := selectRoot(tt.explicit, tt.embedded)
			if gotSpec != tt.wantSpec || gotEmbedded != tt.wantEmbedded || gotDisabled != tt.wantDisabled {
				t.Errorf("selectRoot(%q, %t) = (%q, %t, %t), want (%q, %t, %t)",
					tt.explicit, tt.embedded, gotSpec, gotEmbedded, gotDisabled,
					tt.wantSpec, tt.wantEmbedded, tt.wantDisabled)
			}
		})
	}
}

func TestApplyMachineConfigOverridesOnlyExplicitFields(t *testing.T) {
	cfg := config.Parse("k0smos.ip=dhcp k0smos.gw=10.0.2.2 k0smos.dns=9.9.9.9")
	applyMachineConfig(&cfg, metadata.MachineConfig{
		IP:  "eth0:dhcp,eth1:10.10.0.11/24",
		DNS: "1.1.1.1",
	})
	if cfg.IP != "eth0:dhcp,eth1:10.10.0.11/24" || cfg.DNS != "1.1.1.1" {
		t.Fatalf("machine network was not applied: %#v", cfg)
	}
	if cfg.Gateway != "10.0.2.2" || cfg.Iface != "eth0" {
		t.Errorf("unspecified artifact defaults were lost: %#v", cfg)
	}
}
