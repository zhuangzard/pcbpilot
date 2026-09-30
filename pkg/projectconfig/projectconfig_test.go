package projectconfig

import (
	"os"
	"strings"
	"testing"
)

func TestTemplatesAreValid(t *testing.T) {
	for _, tpl := range Templates {
		c, err := New("demo", tpl.ID)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		if issues := c.Validate(); HasErrors(issues) {
			t.Fatalf("template %s invalid: %+v", tpl.ID, issues)
		}
	}
}

func TestQuickProtoSkips(t *testing.T) {
	c, _ := New("demo", "quick-proto")
	if c.StepEnabled("S5.5") || c.StepEnabled("P10.5") {
		t.Fatal("quick-proto must skip S5.5 and P10.5")
	}
	if !c.StepEnabled("P10") {
		t.Fatal("P10 must stay on")
	}
	var ids []string
	for _, s := range c.SkippedSections() {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "3A,6A" {
		t.Fatalf("sections = %v", ids)
	}
}

func TestGuardRules(t *testing.T) {
	c, _ := New("demo", "full")
	c.Steps["P10"] = Toggle{Enabled: false, Reason: "x"}
	if !HasErrors(c.Validate()) {
		t.Fatal("skipping P10 while routing must be an error")
	}
	c, _ = New("demo", "full")
	c.Report["7"] = Toggle{Enabled: false}
	if !HasErrors(c.Validate()) {
		t.Fatal("required section 7 cannot be skipped")
	}
	c, _ = New("demo", "full")
	c.Sims["analog"] = Toggle{Enabled: false, Reason: "x"}
	if !HasErrors(c.Validate()) {
		t.Fatal("S5.5 on with analog off must be an error")
	}
	c, _ = New("demo", "full")
	c.Constraints.Parts = PartsRules{Preferred: []string{"C1234"}, Banned: []string{"c1234"}}
	if !HasErrors(c.Validate()) {
		t.Fatal("part both preferred and banned must be an error")
	}
}

func TestSaveLoadRoundTripAndStrict(t *testing.T) {
	dir := t.TempDir()
	c, _ := New("demo", "schematic-only")
	c.Constraints.Standards = []string{"IPC-2221B"}
	if err := Save(dir, c, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.StepEnabled("P7") || got.Constraints.Standards[0] != "IPC-2221B" || got.UpdatedBy != "test" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if err := os.WriteFile(Path(dir), []byte(`{"schemaVersion":1,"stepz":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("unknown field must be refused")
	}
	if err := os.WriteFile(Path(dir), []byte(`{"schemaVersion":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer schema must be refused: %v", err)
	}
	if _, err := Load(t.TempDir()); err != ErrNotFound {
		t.Fatalf("missing config: %v", err)
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	c, _ := New("demo", "full")
	c.Steps["S5"] = Toggle{Enabled: false, Reason: "x"}
	if err := Save(t.TempDir(), c, "t"); err == nil {
		t.Fatal("invalid config must not be written")
	}
}
