package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -n must show the last N lines without reading the whole console: a k0s boot log
// reaches hundreds of kilobytes in a minute.
func TestLogsTailsLastLines(t *testing.T) {
	dir := stateIn(t)
	if err := os.MkdirAll(filepath.Join(dir, "vm"), 0700); err != nil {
		t.Fatal(err)
	}
	console := filepath.Join(dir, "vm", consoleFile)

	var body strings.Builder
	for i := 1; i <= 500; i++ {
		body.WriteString("line ")
		body.WriteString(strings.Repeat("x", 60))
		body.WriteString(" ")
		body.WriteString(itoa(i))
		body.WriteString("\n")
	}
	if err := os.WriteFile(console, []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := logsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--name", "vm", "-n", "3"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(got), out.String())
	}
	if !strings.HasSuffix(got[0], " 498") || !strings.HasSuffix(got[2], " 500") {
		t.Errorf("tail = %q…%q, want lines 498 to 500", got[0], got[2])
	}
}

// Asking for more lines than exist must show everything rather than fail.
func TestLogsTailBeyondStart(t *testing.T) {
	dir := stateIn(t)
	if err := os.MkdirAll(filepath.Join(dir, "vm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vm", consoleFile), []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := logsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--name", "vm", "-n", "50"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a\nb\n" {
		t.Errorf("output = %q, want the whole file", out.String())
	}
}

// A guest that has never booted should say so, naming where it looked.
func TestLogsForUnknownGuest(t *testing.T) {
	stateIn(t)
	cmd := logsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--name", "ghost"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("no error for a guest that was never booted")
	}
	if !strings.Contains(err.Error(), "has it been booted") {
		t.Errorf("error = %v, want a hint about booting", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
