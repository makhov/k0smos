package status

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The record exists so that "what did PID 1 decide" survives the console
// scrolling away, and can be answered while the node is still running.
func TestRecordsStepsInOrderWithOutcomes(t *testing.T) {
	r := New(nil)
	r.Step("mounts", nil, "")
	r.Step("modules", nil, "48 loaded, 2 skipped")
	r.Step("cgroup", errors.New("no cgroup2"), "")

	rec := r.Snapshot()
	if len(rec.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(rec.Steps))
	}
	if rec.Steps[0].Name != "mounts" || !rec.Steps[0].OK {
		t.Errorf("steps[0] = %+v", rec.Steps[0])
	}
	if rec.Steps[1].Detail != "48 loaded, 2 skipped" {
		t.Errorf("detail = %q", rec.Steps[1].Detail)
	}
	if rec.Steps[2].OK || rec.Steps[2].Error != "no cgroup2" {
		t.Errorf("steps[2] = %+v, want failed with the error text", rec.Steps[2])
	}
}

// Every mutation must reach the sink: a node that hangs later should still leave
// a record of how far it got.
func TestPersistsAfterEveryChange(t *testing.T) {
	var writes int
	var last []byte
	r := New(func(b []byte) error { writes++; last = b; return nil })

	r.Step("mounts", nil, "")
	r.SetChild([]string{"k0s", "controller"})
	r.ChildExited(errors.New("boom"))

	if writes != 3 {
		t.Errorf("sink called %d times, want 3", writes)
	}
	var rec Record
	if err := json.Unmarshal(last, &rec); err != nil {
		t.Fatalf("last write is not valid JSON: %v", err)
	}
	if rec.Child.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", rec.Child.Restarts)
	}
}

// A failing sink must not stop the boot; the record is a diagnostic, not a
// dependency.
func TestSinkFailureIsNotFatal(t *testing.T) {
	r := New(func([]byte) error { return errors.New("read-only fs") })
	r.Step("mounts", nil, "") // must not panic
	if len(r.Snapshot().Steps) != 1 {
		t.Error("step was not recorded when the sink failed")
	}
}

func TestChildRestartsAreCounted(t *testing.T) {
	r := New(nil)
	r.SetChild([]string{"k0s", "controller", "--single"})
	if !r.Snapshot().Child.Running {
		t.Error("child should be running after SetChild")
	}
	r.ChildExited(errors.New("exit status 1"))
	r.ChildExited(nil)
	rec := r.Snapshot()
	if rec.Child.Restarts != 2 {
		t.Errorf("restarts = %d, want 2", rec.Child.Restarts)
	}
	if rec.Child.Running {
		t.Error("child should not read as running after it exited")
	}
	if rec.Child.LastExit != "signal: killed or clean exit" && rec.Child.LastExit == "" {
		t.Error("last exit should be recorded even for a nil error")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	r := New(nil)
	r.Step("mounts", nil, "")
	snap := r.Snapshot()
	snap.Steps[0].Name = "mutated"
	if r.Snapshot().Steps[0].Name != "mounts" {
		t.Error("Snapshot must not alias the recorder's own slice")
	}
}

// The rendered form is what an operator reads, so it must name the failure
// rather than bury it.
func TestTextHighlightsFailures(t *testing.T) {
	r := New(nil)
	r.SetSwitchedRoot(true)
	r.SetHostname("node-7")
	r.Step("mounts", nil, "")
	r.Step("network", errors.New("no lease"), "dhcp on eth0")
	r.SetChild([]string{"k0s", "worker"})

	out := Text(r.Snapshot())
	for _, want := range []string{"node-7", "mounts", "ok", "network", "FAILED", "no lease", "k0s worker"} {
		if !strings.Contains(out, want) {
			t.Errorf("Text() missing %q:\n%s", want, out)
		}
	}
}

func TestJSONRoundTrips(t *testing.T) {
	r := New(nil)
	r.Step("mounts", nil, "")
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Schema == 0 {
		t.Error("schema version should be recorded so a reader can tell the shape")
	}
}
