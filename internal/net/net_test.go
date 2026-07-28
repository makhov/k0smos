package net

import "testing"

type fakeLinker struct{ up []string }

func (f *fakeLinker) LinkUp(name string) error {
	f.up = append(f.up, name)
	return nil
}

func TestUpBringsLoopbackUp(t *testing.T) {
	f := &fakeLinker{}
	if err := Up(f); err != nil {
		t.Fatal(err)
	}
	if len(f.up) != 1 || f.up[0] != "lo" {
		t.Errorf("brought up %v, want [lo]", f.up)
	}
}
