//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"github.com/amakhov/k0smos/internal/config"
	"github.com/amakhov/k0smos/internal/status"
)

// Every console request has to leave a trace, because the console is root on
// the node and is handed to more people than the disk is.
//
// "status" is answered from the record alone and never touches sys, which is
// what lets this run anywhere. It also makes the reply self-checking: the JSON
// handed back was serialised during the call, so if it already says one request
// was made, the request was recorded before it was answered -- which is what
// keeps a request that hangs or kills init from vanishing.
func TestAuditedRequestRecordsBeforeAnswering(t *testing.T) {
	rec = status.New(nil)
	t.Cleanup(func() { rec = status.New(nil) })

	data, err := auditedRequest(nil, config.Config{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	var answered status.Record
	if err := json.Unmarshal(data, &answered); err != nil {
		t.Fatal(err)
	}
	if answered.Debug.Requests != 1 || answered.Debug.Last != "status" {
		t.Errorf("the reply says %d request(s), last %q; want the request recorded before it was answered",
			answered.Debug.Requests, answered.Debug.Last)
	}

	// A refused request is recorded too: "was a token issued or denied" is the
	// question an audit trail exists to answer.
	if _, err := auditedRequest(nil, config.Config{}, "nonsense"); err == nil {
		t.Fatal("an unknown request should fail")
	}
	got := rec.Snapshot().Debug
	if got.Requests != 2 || got.Last != "nonsense" {
		t.Errorf("Debug = %+v, want both requests counted", got)
	}
}
