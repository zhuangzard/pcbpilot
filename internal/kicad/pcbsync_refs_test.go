package kicad

import "testing"

// TestRenameReferences: a rename that would orphan a netclass assignment,
// a pattern that matches the old name only, or a custom rule naming the
// old net is reported; a pattern matching both names is not.
func TestRenameReferences(t *testing.T) {
	plan := &SyncPlan{Ops: []SyncOp{{Op: "renameNet", From: "/SIG", To: "/SENSE"}}}
	cases := []struct {
		name, pro, dru string
		want           int
	}{
		{"none", `{"net_settings":{}}`, "", 0},
		{"assignment", `{"net_settings":{"netclass_assignments":{"/SIG":"Fast"}}}`, "", 1},
		{"pattern old only", `{"net_settings":{"netclass_patterns":[{"netclass":"Fast","pattern":"/SIG*"}]}}`, "", 1},
		{"pattern both", `{"net_settings":{"netclass_patterns":[{"netclass":"Any","pattern":"/S*"}]}}`, "", 0},
		{"rule", `{}`, "(rule \"x\" (condition \"A.NetName == '/SIG'\") (constraint clearance (min 0.3mm)))", 1},
		{"not json", `{`, "", 1},
	}
	for _, c := range cases {
		if got := RenameReferences([]byte(c.pro), []byte(c.dru), plan); len(got) != c.want || (c.want > 0 && got[0].Code != ErrRenameReferenced) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := RenameReferences([]byte(`{`), nil, &SyncPlan{}); got != nil {
		t.Errorf("no rename, nothing to check: %+v", got)
	}
}
