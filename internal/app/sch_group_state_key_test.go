package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// F3 (2026-09-25 E2E): an `sch apply` queue filed the page's groups under the
// project uuid; `sch gate --project ceshi` read the empty name-keyed record and
// lost every ownership exemption. These tests pin the alias rules offline.

const (
	f3Name = "ceshi"
	f3UUID = "1cdaf015a8ae4a68ba26327e6b6c31f0"
	f3Doc  = "950ae6609e91d753"
)

func f3SaveGroups(t *testing.T, key string, members ...string) {
	t.Helper()
	st, err := loadPcbStageState(key)
	if err != nil {
		t.Fatal(err)
	}
	st.SetGroupsForPage(f3Doc, []*schGroup{{ID: "g1", Name: "USB", Members: members}})
	if err := savePcbStageState(st); err != nil {
		t.Fatal(err)
	}
}

func TestStageKeyAliasFallsBackToLiveIdentity(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	if got := stageKeyAlias(f3Name, f3Name, f3UUID, workflow.Exists); got != f3Name {
		t.Fatalf("no records: want typed key, got %q", got)
	}
	f3SaveGroups(t, f3UUID, "J2", "U2")
	if got := stageKeyAlias(f3Name, f3Name, f3UUID, workflow.Exists); got != f3UUID {
		t.Fatalf("name missing, uuid recorded: want uuid, got %q", got)
	}
	f3SaveGroups(t, f3Name, "J2", "U2")
	if got := stageKeyAlias(f3Name, f3Name, f3UUID, workflow.Exists); got != f3Name {
		t.Fatalf("typed key recorded: must stay authoritative, got %q", got)
	}
}

func TestPickSchGroupStateKey(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	pick := func(key, uuid string) string {
		return pickSchGroupStateKey(key, uuid, f3Doc, loadPcbStageState, workflow.Exists)
	}
	if got := pick(f3Name, ""); got != f3Name {
		t.Fatalf("offline (no uuid): want name, got %q", got)
	}
	if got := pick(f3Name, f3UUID); got != f3Name {
		t.Fatalf("fresh project: keep the resolved key (no second file), got %q", got)
	}
	// Legacy groups recorded by name only: keep reading them.
	f3SaveGroups(t, f3Name, "R1")
	if got := pick(f3Name, f3UUID); got != f3Name {
		t.Fatalf("legacy name-keyed groups: want name, got %q", got)
	}
	// Once the uuid record holds this page's groups it wins for both routings.
	f3SaveGroups(t, f3UUID, "J2", "U2")
	if got := pick(f3Name, f3UUID); got != f3UUID {
		t.Fatalf("name routing: want uuid, got %q", got)
	}
	if got := pick(f3UUID, f3UUID); got != f3UUID {
		t.Fatalf("uuid routing: want uuid, got %q", got)
	}
}

func TestSchOwnershipAcrossKeysSeesUUIDGroups(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	f3SaveGroups(t, f3UUID, "J2", "U2")
	// Name routing with only the uuid record: the exemption must still apply.
	same := schSameLayoutOwnerAcrossKeys([]string{f3Name, f3UUID}, f3Doc, loadPcbStageState, workflow.Exists)
	if same == nil || !same("J2", "U2") {
		t.Fatal("name-routed gate must see the uuid-keyed group ownership")
	}
	if same("J2", "R9") {
		t.Fatal("undeclared pair must not be exempt")
	}
	// Name-only routing (no uuid known) keeps the old, conservative result.
	if same := schSameLayoutOwnerAcrossKeys([]string{f3Name}, f3Doc, loadPcbStageState, workflow.Exists); same != nil {
		t.Fatal("no declaration under the typed key: expected no exemption")
	}
	// Declarations under both records are unioned.
	st, _ := loadPcbStageState(f3Name)
	st.SetGroupsForPage(f3Doc, []*schGroup{{ID: "g1", Name: "UART", Members: []string{"U3", "C7"}}})
	if err := savePcbStageState(st); err != nil {
		t.Fatal(err)
	}
	same = schSameLayoutOwnerAcrossKeys([]string{f3Name, f3UUID}, f3Doc, loadPcbStageState, workflow.Exists)
	if !same("J2", "U2") || !same("U3", "C7") {
		t.Fatal("expected union of name- and uuid-keyed declarations")
	}
}

// End-to-end through the CLI resolver with a fake daemon: `--project ceshi`
// (name routing) must see groups an apply queue filed under the uuid.
func TestNameRoutedGroupContextSeesUUIDKeyedGroups(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	cfg, _, cleanup := newAutolayoutTestDaemon(t, func(_ int, c autolayoutTestCall) string {
		result := `{}`
		switch c.Action {
		case "document.current":
			result = `{"uuid":"` + f3Doc + `"}`
		case "project.current":
			result = `{"uuid":"` + f3UUID + `","friendlyName":"` + f3Name + `"}`
		case "schematic.pages.list":
			result = `{"pages":[{"uuid":"` + f3Doc + `","name":"P2"}]}`
		case "pcb.documents.list":
			result = `{"pcbs":[]}`
		}
		return `{"ok":true,"context":{"projectUuid":"` + f3UUID + `","documentUuid":"` + f3Doc + `","documentType":"schematic"},"result":` + result + `}`
	})
	defer cleanup()
	f3SaveGroups(t, f3UUID, "J2", "U2")
	for _, typed := range []string{f3Name, f3UUID} {
		cfg.project = typed
		_, _, doc, key, _, groups, err := loadSchGroupsContext(cfg, "w1")
		if err != nil {
			t.Fatal(err)
		}
		if doc != f3Doc || key != f3UUID || len(groups) != 1 {
			t.Fatalf("--project %s: doc=%s key=%s groups=%d", typed, doc, key, len(groups))
		}
		same, err := loadSchPageOwnership(cfg, "w1")
		if err != nil || same == nil || !same("J2", "U2") {
			t.Fatalf("--project %s: ownership exemption lost (%v)", typed, err)
		}
	}
	if workflow.Exists(f3Name) {
		t.Fatal("reads must not create a second, name-keyed state file")
	}
}
