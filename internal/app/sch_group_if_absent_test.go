package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

func groupIfAbsentFixture() *schGroup {
	return &schGroup{ID: "g7", Name: "POWER", Members: []string{"C1", "C2", "U1"}, BlockID: "block.power", Instance: "U1", Roles: map[string]string{"CORE": "U1", "OUT": "C2"}, At: "2026-09-06T10:00:00+08:00", Annotations: []string{"frame-old"}}
}

func TestGroupIfAbsentReusesOnlyCompleteEquivalentDeclaration(t *testing.T) {
	g := groupIfAbsentFixture()
	groups := []*schGroup{g}
	before, _ := json.Marshal(groups)
	next, got, unchanged, err := groupsCreateWithProvenance(groups, " power ", []string{"U1", "C2", "C1", "C1"}, "block.power", "U1", map[string]string{"OUT": "C2", "CORE": "U1"}, true)
	if err != nil || !unchanged || got != g || len(next) != 1 {
		t.Fatalf("reordered complete member set/provenance should reuse the group: %+v %t %v", got, unchanged, err)
	}
	after, _ := json.Marshal(groups)
	if !bytes.Equal(before, after) {
		t.Fatal("no-op changed group IDs, timestamps, annotations or member ordering")
	}
	// Absence and an empty map both declare no role bindings.
	g.BlockID, g.Instance, g.Roles = "", "", nil
	if _, _, unchanged, err := groupsCreateWithProvenance(groups, "POWER", g.Members, "", "", map[string]string{}, true); err != nil || !unchanged {
		t.Fatalf("empty provenance map should equal absent provenance: %v", err)
	}
}

func TestGroupIfAbsentRejectsPartialMembersAndConflictingProvenance(t *testing.T) {
	for _, tc := range []struct {
		name            string
		members         []string
		block, instance string
		roles           map[string]string
	}{
		{"subset", []string{"U1", "C1"}, "block.power", "U1", map[string]string{"CORE": "U1"}},
		{"superset", []string{"U1", "C1", "C2", "R1"}, "block.power", "U1", map[string]string{"CORE": "U1", "OUT": "C2"}},
		{"disjoint", []string{"U9", "C9"}, "block.power", "U9", nil},
		{"different-block", []string{"U1", "C1", "C2"}, "block.other", "U1", map[string]string{"CORE": "U1", "OUT": "C2"}},
		{"missing-provenance", []string{"U1", "C1", "C2"}, "", "", nil},
		{"different-instance", []string{"U1", "C1", "C2"}, "block.power", "OTHER", map[string]string{"CORE": "U1", "OUT": "C2"}},
		{"partial-roles", []string{"U1", "C1", "C2"}, "block.power", "U1", map[string]string{"CORE": "U1"}},
		{"wrong-role-member", []string{"U1", "C1", "C2"}, "block.power", "U1", map[string]string{"CORE": "C1", "OUT": "C2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := groupIfAbsentFixture()
			before, _ := json.Marshal(g)
			if _, _, unchanged, err := groupsCreateWithProvenance([]*schGroup{g}, "POWER", tc.members, tc.block, tc.instance, tc.roles, true); err == nil || unchanged {
				t.Fatalf("conflicting declaration was accepted: %v", err)
			}
			after, _ := json.Marshal(g)
			if !bytes.Equal(before, after) {
				t.Fatal("conflict changed the existing declaration")
			}
		})
	}
}

func TestGroupIfAbsentCreatesWhenAbsentAndRejectsAmbiguousOwnership(t *testing.T) {
	roles := map[string]string{"CORE": "U1"}
	groups, g, unchanged, err := groupsCreateWithProvenance(nil, "POWER", []string{"U1", "C1"}, "block.power", "U1", roles, true)
	if err != nil || unchanged || len(groups) != 1 || g.ID != "g1" || g.BlockID != "block.power" {
		t.Fatalf("absent declaration should create normally: %+v %t %v", g, unchanged, err)
	}
	roles["CORE"] = "C1"
	if g.Roles["CORE"] != "U1" {
		t.Fatal("caller map can mutate recorded provenance")
	}
	for _, existing := range [][]*schGroup{
		{mkGroup("g1", "POWER", "C1", "U1"), mkGroup("g2", "power", "U2")},
		{mkGroup("g1", "OTHER", "C1", "U1")},
		{mkGroup("g1", "POWER", "C1", "U1"), mkGroup("g2", "OTHER", "U1")},
	} {
		if _, _, _, err := groupsCreateWithProvenance(existing, "POWER", []string{"U1", "C1"}, "", "", nil, true); err == nil {
			t.Fatalf("ambiguous name/member ownership passed: %+v", existing)
		}
	}
	if _, _, _, err := groupsCreateWithProvenance(nil, "", []string{"U1"}, "", "", nil, true); err == nil {
		t.Fatal("if-absent needs an explicit name")
	}
	if _, _, _, err := groupsCreateWithProvenance(nil, "POWER", []string{"U1"}, "block.power", "", map[string]string{"OUT": "R9"}, true); err == nil {
		t.Fatal("provenance role outside the member set passed")
	}
	if _, _, _, err := groupsCreateWithProvenance(groups, "POWER", []string{"U1", "C1"}, "block.power", "U1", map[string]string{"CORE": "U1"}, false); err == nil {
		t.Fatal("ordinary create must not silently become idempotent")
	}
}

func TestGroupCreateIfAbsentCLILeavesPersistedStateUntouchedOnRepeat(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	const project, doc = "group-if-absent-test", "pageA"
	cfg, daemon, cleanup := newAutolayoutTestDaemon(t, func(_ int, c autolayoutTestCall) string {
		result := `{}`
		switch c.Action {
		case "document.current":
			result = `{"uuid":"pageA"}`
		case "project.current":
			result = `{"uuid":"project-id","name":"group-if-absent-test"}`
		case "schematic.pages.list":
			result = `{"pages":[{"uuid":"pageA","name":"A"}]}`
		case "pcb.documents.list":
			result = `{"pcbs":[]}`
		}
		return fmt.Sprintf(`{"ok":true,"context":{"projectUuid":"project-id","documentUuid":"pageA","documentType":"schematic"},"result":%s}`, result)
	})
	defer cleanup()
	cfg.project = project
	window := "w1"
	run := func(members string) (string, error) {
		var stdout bytes.Buffer
		cmd := newSchGroupCmd(cfg, &window, &stdout, io.Discard)
		cmd.SetArgs([]string{"create", "--name", "POWER", "--members", members, "--if-absent"})
		err := cmd.Execute()
		return stdout.String(), err
	}
	if out, err := run("U1,C2,C1"); err != nil || !strings.Contains(out, "created group") {
		t.Fatalf("initial CLI declaration failed: %s %v", out, err)
	}
	file := workflow.Path(project)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(file)
	if out, err := run("C1,U1,C2"); err != nil || !strings.Contains(out, "unchanged group") {
		t.Fatalf("repeated CLI declaration did not no-op: %s %v", out, err)
	}
	after, _ := os.ReadFile(file)
	statAfter, _ := os.Stat(file)
	if !bytes.Equal(before, after) || !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Fatal("a repeated declaration rewrote persistent state")
	}
	if _, err := run("U1,C1"); err == nil {
		t.Fatal("CLI silently accepted only part of an existing Lib")
	}
	after, _ = os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("conflicting CLI declaration changed persistent state")
	}
	st, err := loadPcbStageState(project)
	if err != nil || len(st.GroupsForPage(doc)) != 1 || !reflect.DeepEqual(st.GroupsForPage(doc)[0].Members, []string{"U1", "C2", "C1"}) {
		t.Fatalf("final registry is not the complete Lib: %+v %v", st, err)
	}
	for _, c := range daemon.snapshot() {
		switch c.Action {
		case "document.current", "document.open", "project.current", "schematic.pages.list", "pcb.documents.list":
		default:
			t.Fatalf("group registration performed an unexpected EDA action: %s", c.Action)
		}
	}
}
