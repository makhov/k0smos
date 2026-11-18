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

// The verdict a serial capture may be the only record of: how long, how many
// stages, and nothing else when they all passed.
func TestSummaryIsTheOneLineVerdict(t *testing.T) {
	rec := Record{Boot: "2026-11-14T10:00:00Z", Steps: []Step{
		{Name: "mounts", OK: true, At: "2026-11-14T10:00:01.5Z"},
		{Name: "root", OK: true, At: "2026-11-14T10:00:04.2Z"},
	}}
	if got, want := Summary(rec), "boot ok in 4.2s, 2 stages"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// A failed stage has to be named in that line, with its error on it, and the
// line has to stay one line however long the error is.
func TestSummaryNamesWhatFailed(t *testing.T) {
	rec := Record{Boot: "2026-11-14T10:00:00Z", Steps: []Step{
		{Name: "mounts", OK: true, At: "2026-11-14T10:00:01Z"},
		{Name: "network", At: "2026-11-14T10:00:02Z", Error: "dhcp on eth0:\n  no lease"},
		{Name: "metadata", At: "2026-11-14T10:00:03Z", Error: strings.Repeat("x", 80)},
	}}
	got := Summary(rec)
	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("the verdict must be one line: %q", got)
	}
	for _, want := range []string{"DEGRADED", " in 3s,", "network (dhcp on eth0: no lease)",
		"metadata (" + strings.Repeat("x", 57) + "...)"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, missing %q", got, want)
		}
	}
}

// Nothing recorded yet means nothing to time; the verdict still renders, since
// Text leads with it.
func TestSummaryWithoutStepsOmitsTheTime(t *testing.T) {
	if got, want := Summary(Record{Boot: "2026-11-14T10:00:00Z"}), "boot ok, 0 stages"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// A GPU image reports all four; a plain node has no driver and must not show
// an empty "nvidia" with nothing after it.
func TestImageStringLeavesOutWhatIsUnknown(t *testing.T) {
	full := Image{K0s: "v1.37.2+k0s.1", Build: "ff7354c2e5f1", Kernel: "7.0.0-43-generic", NVIDIA: "620.70.06"}
	want := "k0s v1.37.2+k0s.1, build ff7354c2e5f1, kernel 7.0.0-43-generic, nvidia 620.70.06"
	if got := full.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := (Image{Kernel: "7.0.0-43-generic"}).String(); got != "kernel 7.0.0-43-generic" {
		t.Errorf("String() = %q", got)
	}
	if got := (Image{}).String(); got != "unknown" {
		t.Errorf("String() = %q, want unknown", got)
	}
}

// Console requests are counted, not recorded as boot stages. Appending a step
// per request would inflate the stage count and make Summary report a boot that
// lasted until someone opened a console hours later, which is how an audit trail
// ends up discrediting the record it lives in.
func TestDebugRequestsAreCountedWithoutBecomingSteps(t *testing.T) {
	r := New(nil)
	r.Step("mounts", nil, "")
	r.DebugRequest("dmesg")
	r.DebugRequest("token worker")

	rec := r.Snapshot()
	if rec.Debug.Requests != 2 {
		t.Errorf("Debug.Requests = %d, want 2", rec.Debug.Requests)
	}
	if rec.Debug.Last != "token worker" {
		t.Errorf("Debug.Last = %q, want the most recent request", rec.Debug.Last)
	}
	if rec.Debug.LastAt == "" {
		t.Error("Debug.LastAt should say when, or the count cannot be placed in time")
	}
	if len(rec.Steps) != 1 {
		t.Errorf("Steps = %d, want the one real stage", len(rec.Steps))
	}
	if got := Summary(rec); !strings.Contains(got, "1 stages") {
		t.Errorf("Summary = %q, want the stage count unaffected by console requests", got)
	}
}

// The record is rewritten whole on every change, and a console is reachable by
// whoever holds the BMC, so what it stores from a request has to be bounded.
func TestDebugRequestIsBounded(t *testing.T) {
	r := New(nil)
	r.DebugRequest("cat /" + strings.Repeat("a", 4096))
	if n := len(r.Snapshot().Debug.Last); n > 80 {
		t.Errorf("Debug.Last is %d bytes; a console reader should not be able to grow the record", n)
	}
}

func TestTextReportsConsoleUse(t *testing.T) {
	r := New(nil)
	r.Step("mounts", nil, "")
	if strings.Contains(Text(r.Snapshot()), "console request") {
		t.Error("a node nobody opened a console on should not report one")
	}
	r.DebugRequest("kubeconfig")
	out := Text(r.Snapshot())
	for _, want := range []string{"1 console request(s)", "kubeconfig"} {
		if !strings.Contains(out, want) {
			t.Errorf("Text() missing %q:\n%s", want, out)
		}
	}
}
