package main

import (
	"bytes"
	"strings"
	"testing"
)

// runToken executes the token command as a user would.
func runToken(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := tokenCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A bad role must fail here rather than after connecting to a guest: the round
// trip is slow (the node waits on the API server) and the error it comes back
// with says nothing about which roles exist.
func TestTokenRejectsAnUnknownRoleWithoutTalkingToTheNode(t *testing.T) {
	// --socket points somewhere that does not exist, so a run that reaches the
	// dial fails with a connection error instead of this one.
	out, err := runToken(t, "--role", "cotroller", "--socket", "/nonexistent/c.sock")
	if err == nil {
		t.Fatalf("accepted an unknown role; output:\n%s", out)
	}
	for _, want := range []string{"controller", "worker", "cotroller"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestTokenAcceptsBothRoles(t *testing.T) {
	for _, role := range []string{"controller", "worker"} {
		// Still fails, but on the socket rather than the role — which is what
		// shows the role was accepted.
		_, err := runToken(t, "--role", role, "--socket", "/nonexistent/c.sock")
		if err == nil {
			t.Fatalf("role %q: expected a failure reaching the socket", role)
		}
		if strings.Contains(err.Error(), "--role") {
			t.Errorf("role %q was rejected: %v", role, err)
		}
	}
}
