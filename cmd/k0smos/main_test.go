package main

import "testing"

func TestGateRejectsNonPID1(t *testing.T) {
	if err := gate(4242); err == nil {
		t.Fatal("expected error when pid != 1, got nil")
	}
}

func TestGateAcceptsPID1(t *testing.T) {
	if err := gate(1); err != nil {
		t.Fatalf("expected nil for pid 1, got %v", err)
	}
}
