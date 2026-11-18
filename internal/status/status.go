// Package status records what PID 1 decided during boot, so it can be asked
// afterwards.
//
// A k0smos node reports through its console and nothing else, which means every
// conclusion the init reached is printed once and then scrolls away. That is
// enough to watch a boot and useless for diagnosing one that already happened:
// which root was chosen, which modules failed, whether a configuration drive was
// found, how many times the workload has restarted.
//
// The record is written to a file as it is built, so it is readable over the
// control port while the node runs and off the disk afterwards.
package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Schema is the record's shape version, so a reader can tell what it is looking
// at when this grows fields.
const Schema = 3

// Step is one stage of boot and how it went.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	At     string `json:"at"`
}

// Image is what this machine is running. A node has no shell and no package
// manager, so without this there is no way to ask a booted machine which build
// it is -- which is the first question when deciding whether to rebuild it.
// Every field is best-effort and empty when it could not be determined.
type Image struct {
	K0s    string `json:"k0s,omitempty"`    // the k0s release the image ships
	Build  string `json:"build,omitempty"`  // the k0smos commit it was built from
	Kernel string `json:"kernel,omitempty"` // the running kernel's release
	NVIDIA string `json:"nvidia,omitempty"` // the loaded GPU driver, if any
}

// String is the one line an operator reads, with the fields that are unknown
// left out rather than shown empty.
func (i Image) String() string {
	var parts []string
	for _, p := range [][2]string{
		{"k0s", i.K0s}, {"build", i.Build}, {"kernel", i.Kernel}, {"nvidia", i.NVIDIA},
	} {
		if p[1] != "" {
			parts = append(parts, p[0]+" "+p[1])
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}

// Debug counts what arrived on the debug console. The console is the one
// channel a cloud instance or a machine behind a BMC always has, and answering
// requests on it is root on the node -- but it is reachable by a wider set of
// people than the disk is: anyone with the BMC, the hypervisor, an EC2 serial
// console permission, or a cart in the rack. Whoever owns the cluster cannot
// otherwise tell that a node was opened this way.
//
// Counts and the last request only. The reply is never recorded: it is the
// kubeconfig or a join token. The request is shortened to keep the record and
// its one-line rendering bounded by something a console reader can send -- the
// full line is in the kernel log, which is where an audit trail belongs anyway.
type Debug struct {
	Requests int    `json:"requests"`
	Last     string `json:"last,omitempty"`
	LastAt   string `json:"lastAt,omitempty"`
}

// Child describes the supervised workload.
type Child struct {
	Argv     []string `json:"argv,omitempty"`
	Running  bool     `json:"running"`
	Restarts int      `json:"restarts"`
	LastExit string   `json:"lastExit,omitempty"`
}

// Record is the whole picture.
type Record struct {
	Schema       int    `json:"schema"`
	Boot         string `json:"boot"`
	SwitchedRoot bool   `json:"switchedRoot"`
	Hostname     string `json:"hostname,omitempty"`
	Image        Image  `json:"image"`
	Steps        []Step `json:"steps"`
	Debug        Debug  `json:"debug"`
	Child        Child  `json:"child"`
}

// Recorder accumulates the record and persists it after every change.
type Recorder struct {
	mu   sync.Mutex
	rec  Record
	sink func([]byte) error
	now  func() time.Time
}

// New returns a Recorder. sink receives the serialised record after every
// change and may be nil. A sink error is ignored: the record is a diagnostic,
// and losing it must never fail a boot.
func New(sink func([]byte) error) *Recorder {
	r := &Recorder{sink: sink, now: time.Now}
	r.rec.Schema = Schema
	r.rec.Boot = time.Now().UTC().Format(time.RFC3339Nano)
	return r
}

// NewFrom returns a Recorder carrying over what old already recorded, writing to
// sink from now on.
//
// Boot starts before there is anywhere to write — /run does not exist until the
// pseudo-filesystems are mounted — so the first steps are recorded in memory and
// this hands them to a file once there is one.
func NewFrom(old *Recorder, sink func([]byte) error) *Recorder {
	r := &Recorder{sink: sink, now: time.Now}
	if old != nil {
		r.rec = old.Snapshot()
	} else {
		r.rec.Schema = Schema
		r.rec.Boot = time.Now().UTC().Format(time.RFC3339Nano)
	}
	r.flush()
	return r
}

// Step records a stage. A non-nil err marks it failed.
func (r *Recorder) Step(name string, err error, detail string) {
	r.mu.Lock()
	s := Step{Name: name, OK: err == nil, Detail: detail, At: r.now().UTC().Format(time.RFC3339Nano)}
	if err != nil {
		s.Error = err.Error()
	}
	r.rec.Steps = append(r.rec.Steps, s)
	r.mu.Unlock()
	r.flush()
}

// DebugRequest records that the debug console was asked something. Not a Step:
// steps are boot stages, and appending one per request would both inflate the
// stage count and make Summary report a boot that took as long as the gap until
// someone opened a console.
func (r *Recorder) DebugRequest(request string) {
	r.mu.Lock()
	r.rec.Debug.Requests++
	r.rec.Debug.Last = brief(request)
	r.rec.Debug.LastAt = r.now().UTC().Format(time.RFC3339Nano)
	r.mu.Unlock()
	r.flush()
}

func (r *Recorder) SetSwitchedRoot(v bool) {
	r.mu.Lock()
	r.rec.SwitchedRoot = v
	r.mu.Unlock()
	r.flush()
}

func (r *Recorder) SetHostname(h string) {
	r.mu.Lock()
	r.rec.Hostname = h
	r.mu.Unlock()
	r.flush()
}

func (r *Recorder) SetImage(i Image) {
	r.mu.Lock()
	r.rec.Image = i
	r.mu.Unlock()
	r.flush()
}

// SetChild records the workload being supervised.
func (r *Recorder) SetChild(argv []string) {
	r.mu.Lock()
	r.rec.Child.Argv = append([]string(nil), argv...)
	r.rec.Child.Running = true
	r.mu.Unlock()
	r.flush()
}

// ChildExited records that the workload stopped. err is how it ended, which as
// PID 1 may be ECHILD rather than a real status because the reaper can collect
// the process first.
func (r *Recorder) ChildExited(err error) {
	r.mu.Lock()
	r.rec.Child.Running = false
	r.rec.Child.Restarts++
	if err != nil {
		r.rec.Child.LastExit = err.Error()
	} else {
		r.rec.Child.LastExit = "exited without an error"
	}
	r.mu.Unlock()
	r.flush()
}

// Snapshot returns a copy safe to read while boot continues.
func (r *Recorder) Snapshot() Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.rec
	out.Steps = append([]Step(nil), r.rec.Steps...)
	out.Child.Argv = append([]string(nil), r.rec.Child.Argv...)
	return out
}

// JSON is the record as stored and as served over the control port.
func (r *Recorder) JSON() ([]byte, error) {
	return json.MarshalIndent(r.Snapshot(), "", "  ")
}

func (r *Recorder) flush() {
	if r.sink == nil {
		return
	}
	b, err := r.JSON()
	if err != nil {
		return
	}
	_ = r.sink(b)
}

// Summary is the one line a boot can be judged by: how long it took, how many
// stages ran, and which of them failed. A machine with no shell reports through
// a console that scrolls away, and a serial capture may keep only the tail, so
// init prints this once its own work is done -- and a reader of the record gets
// the same verdict without reading every stage.
func Summary(rec Record) string {
	var failed []string
	for _, s := range rec.Steps {
		if !s.OK {
			failed = append(failed, s.Name+" ("+brief(s.Error)+")")
		}
	}
	verdict := "ok"
	if len(failed) > 0 {
		verdict = "DEGRADED"
	}
	took := ""
	if d, ok := span(rec); ok {
		took = " in " + d.Round(100*time.Millisecond).String()
	}
	out := fmt.Sprintf("boot %s%s, %d stages", verdict, took, len(rec.Steps))
	if len(failed) > 0 {
		out += ": " + strings.Join(failed, ", ")
	}
	return out
}

// span is boot start to last stage, taken from the record itself rather than
// the clock, so reading an old record reports what that boot took.
func span(rec Record) (time.Duration, bool) {
	if len(rec.Steps) == 0 {
		return 0, false
	}
	start, err := time.Parse(time.RFC3339, rec.Boot)
	if err != nil {
		return 0, false
	}
	end, err := time.Parse(time.RFC3339, rec.Steps[len(rec.Steps)-1].At)
	if err != nil || end.Before(start) {
		return 0, false
	}
	return end.Sub(start), true
}

// brief puts an error -- or a console request -- on one line, short enough that
// the verdict stays one line on an 80-column console. The whole error is in the
// record; a whole request is in the kernel log.
func brief(err string) string {
	err = strings.Join(strings.Fields(err), " ")
	if len(err) > 60 {
		err = err[:57] + "..."
	}
	return err
}

// Text renders a record for a person. Failures are called out rather than left
// for the reader to spot among the successes.
func Text(rec Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", Summary(rec))
	fmt.Fprintf(&b, "boot        %s\n", rec.Boot)
	if rec.Hostname != "" {
		fmt.Fprintf(&b, "hostname    %s\n", rec.Hostname)
	}
	fmt.Fprintf(&b, "image       %s\n", rec.Image)
	fmt.Fprintf(&b, "switched    %t\n", rec.SwitchedRoot)

	if len(rec.Child.Argv) > 0 {
		state := "stopped"
		if rec.Child.Running {
			state = "running"
		}
		fmt.Fprintf(&b, "workload    %s (%s, restarts=%d)\n",
			strings.Join(rec.Child.Argv, " "), state, rec.Child.Restarts)
		if rec.Child.LastExit != "" {
			fmt.Fprintf(&b, "last exit   %s\n", rec.Child.LastExit)
		}
	}

	// On its own line rather than among the steps. A step says the channel was
	// opened, which is a boot decision; this says it was used, which is not --
	// and someone reading the record to find out why a node misbehaved should
	// not have to infer that from a stage count.
	if rec.Debug.Requests > 0 {
		fmt.Fprintf(&b, "debug       %d console request(s), last %q at %s\n",
			rec.Debug.Requests, rec.Debug.Last, rec.Debug.LastAt)
	}

	b.WriteString("\nsteps\n")
	for _, s := range rec.Steps {
		mark := "ok"
		if !s.OK {
			mark = "FAILED"
		}
		fmt.Fprintf(&b, "  %-8s %-22s %s\n", mark, s.Name, s.Detail)
		if s.Error != "" {
			fmt.Fprintf(&b, "           %s\n", s.Error)
		}
	}
	return b.String()
}
