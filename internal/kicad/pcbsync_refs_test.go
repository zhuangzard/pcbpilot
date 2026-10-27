package kicad

import "testing"

// TestRenameReferences: a rename is refused when any assignment or
// pattern involves either name (wildcard or regex reading), when the rules
// name either net, or when a rule selects nets other than by a literal
// comparison; unrelated literal rules and classes do not block it.
func TestRenameReferences(t *testing.T) {
	plan := &SyncPlan{Ops: []SyncOp{{Op: "renameNet", From: "/SIG", To: "/SENSE"}}}
	cases := []struct {
		name, pro, dru string
		want           int
	}{
		{"none", `{"net_settings":{}}`, "", 0},
		{"unrelated assignment and pattern", `{"net_settings":{"netclass_assignments":{"GND":"Pwr"},"netclass_patterns":[{"netclass":"Pwr","pattern":"+5V"}]}}`, "", 0},
		{"assignment old name", `{"net_settings":{"netclass_assignments":{"/SIG":"Fast"}}}`, "", 1},
		{"assignment new name only", `{"net_settings":{"netclass_assignments":{"/SENSE":"Fast"}}}`, "", 1},
		{"pattern old only", `{"net_settings":{"netclass_patterns":[{"netclass":"Fast","pattern":"/SIG*"}]}}`, "", 1},
		{"pattern new only", `{"net_settings":{"netclass_patterns":[{"netclass":"Fast","pattern":"/SEN*"}]}}`, "", 1},
		{"regex pattern", `{"net_settings":{"netclass_patterns":[{"netclass":"Fast","pattern":"^/S[A-Z]+$"}]}}`, "", 1},
		{"rule names old", `{}`, "(rule \"x\" (condition \"A.NetName == '/SIG'\") (constraint clearance (min 0.3mm)))", 1},
		{"rule literal other net", `{}`, "(rule \"x\" (condition \"A.NetName == '/OTHER'\") (constraint clearance (min 0.3mm)))", 0},
		{"rule matches regex", `{}`, "(rule \"x\" (condition \"A.NetName matches '^/S.*$'\") (constraint clearance (min 0.3mm)))", 1},
		{"rule matches literal", `{}`, "(rule \"x\" (condition \"A.NetName matches '^/SIG$'\") (constraint clearance (min 0.3mm)))", 1},
		{"rule wildcard literal", `{}`, "(rule \"x\" (condition \"A.NetName == '/S*'\") (constraint clearance (min 0.3mm)))", 1},
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
