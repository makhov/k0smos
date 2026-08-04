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
const Schema = 1

// Step is one stage of boot and how it went.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	At     string `json:"at"`
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
	Steps        []Step `json:"steps"`
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
	r.rec.Boot = time.Now().UTC().Format(time.RFC3339)
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
		r.rec.Boot = time.Now().UTC().Format(time.RFC3339)
	}
	r.flush()
	return r
}

// Step records a stage. A non-nil err marks it failed.
func (r *Recorder) Step(name string, err error, detail string) {
	r.mu.Lock()
	s := Step{Name: name, OK: err == nil, Detail: detail, At: r.now().UTC().Format(time.RFC3339)}
	if err != nil {
		s.Error = err.Error()
	}
	r.rec.Steps = append(r.rec.Steps, s)
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

// Text renders a record for a person. Failures are called out rather than left
// for the reader to spot among the successes.
func Text(rec Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "boot        %s\n", rec.Boot)
	if rec.Hostname != "" {
		fmt.Fprintf(&b, "hostname    %s\n", rec.Hostname)
	}
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
