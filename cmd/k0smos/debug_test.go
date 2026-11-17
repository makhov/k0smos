package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestServeDebugAnswersAndReportsErrorsInOneLine(t *testing.T) {
	var w strings.Builder
	in := strings.NewReader("status\nbogus\ncat /x\n")
	serveDebug(in, &w, func(request string) ([]byte, error) {
		switch request {
		case "status":
			return []byte(`{"ok":true}`), nil
		case "cat /x":
			return []byte("one\ntwo\n"), nil
		}
		// Multi-line, as a wrapped path error can be: it must not arrive looking
		// like payload.
		return nil, errors.New("unknown request\nsecond line")
	})
	got := w.String()
	want := "{\"ok\":true}\nk0smos debug: unknown request second line\none\ntwo\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// Someone who opens a console long after boot sees no banner, so enter has to
// answer. Derived from control.Requests rather than restated, so a new request
// does not fail a test that is about discoverability.
func TestServeDebugEmptyLineAndHelpPrintUsage(t *testing.T) {
	var w strings.Builder
	serveDebug(strings.NewReader("\n   \nhelp\n"), &w, func(string) ([]byte, error) {
		t.Error("usage must not reach the responder")
		return nil, nil
	})
	lines := strings.Split(strings.TrimSuffix(w.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d usage lines, want 3: %q", len(lines), w.String())
	}
	for _, l := range lines {
		if !strings.Contains(l, "cat") || !strings.Contains(l, "dmesg") {
			t.Errorf("usage does not name the requests: %q", l)
		}
	}
}

// A megabyte of log down a serial line takes minutes nobody can interrupt.
func TestServeDebugKeepsTheTailOfALongReply(t *testing.T) {
	body := strings.Repeat("a", debugMaxReply) + "THE-END\n"
	var w strings.Builder
	serveDebug(strings.NewReader("dmesg\n"), &w, func(string) ([]byte, error) {
		return []byte(body), nil
	})
	got := w.String()
	if !strings.HasSuffix(got, "THE-END\n") {
		t.Error("the end of the reply was cut instead of the start")
	}
	if !strings.Contains(got, fmt.Sprintf("last %d of %d bytes", debugMaxReply, len(body))) {
		t.Errorf("truncation was not announced: %q", got[:min(len(got), 120)])
	}
	if len(got) > debugMaxReply+200 {
		t.Errorf("reply is %d bytes, cap is %d", len(got), debugMaxReply)
	}
}
