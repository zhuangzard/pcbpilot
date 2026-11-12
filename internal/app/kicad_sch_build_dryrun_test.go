package app

import (
	"bytes"
	"strings"
	"testing"
)

// A dry run is a plan: ok, but never complete, and it says so.
func TestSbDryRunIsNeverComplete(t *testing.T) {
	res := &sbReport{OK: true, Plan: &sbPlan{}}
	var out, errb bytes.Buffer
	if err := finishSbRun(res, nil, "", &out, &errb); err != nil {
		t.Fatal(err)
	}
	if res.Complete || len(res.Incomplete) != 1 || !strings.Contains(res.Incomplete[0], "dry-run") {
		t.Fatalf("complete=%v incomplete=%v", res.Complete, res.Incomplete)
	}
	if !strings.Contains(errb.String(), "nothing written") {
		t.Fatalf("stderr = %q", errb.String())
	}
	// a real build with every gate pass stays complete
	res = &sbReport{OK: true, Gates: []sbGate{{Name: "erc", Status: "pass"}}}
	if err := finishSbRun(res, nil, "", &out, &errb); err != nil || !res.Complete {
		t.Fatalf("complete=%v err=%v", res.Complete, err)
	}
}
