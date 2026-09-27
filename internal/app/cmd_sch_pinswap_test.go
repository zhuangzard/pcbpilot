package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

func ledSwapItem() *pcbauto.FeedbackItem {
	return &pcbauto.FeedbackItem{ID: "FB01", Kind: pcbauto.FBPinSwap, Applyable: true, Title: "U1 LED_CTRL",
		Proposal: pcbauto.FeedbackProposal{Swaps: []pcbauto.PinSwap{{Ref: "U1", Net: "LED_CTRL", FromPin: "38", FromName: "IO2", ToPin: "39", ToName: "IO1", ToWasFree: true, FromBecomesFree: true}}}}
}

func loadMCUPage(t *testing.T) *connectivity.Document {
	raw, err := os.ReadFile("testdata/esp32-mini-sch-mcu-page.json")
	if err != nil {
		t.Fatal(err)
	}
	var d connectivity.Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestPinSwapPlanWithSnapshot(t *testing.T) {
	sch := loadMCUPage(t)
	plan, err := buildPinSwapPlan(ledSwapItem(), sch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"- U1.38(IO2): LED_CTRL", "+ U1.38(IO2): (NC)", "- U1.39(IO1): (NC)", "+ U1.39(IO1): LED_CTRL"}
	if strings.Join(plan.Diff, "\n") != strings.Join(want, "\n") {
		t.Fatalf("diff:\n%s", strings.Join(plan.Diff, "\n"))
	}
	var ids []string
	for _, s := range plan.Playbook.Steps {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "check-before,disconnect-U1-38,clear-nc-U1-39,connect-U1-39,set-nc-U1-38,save,check-after" {
		t.Fatalf("steps %s", got)
	}
	if plan.Playbook.Meta.Doc != sch.DocumentID || !plan.Playbook.RequireFullExecution {
		t.Fatalf("meta %+v", plan.Playbook.Meta)
	}
	// the after checkpoint moves exactly one pin
	before, _ := sch.NamedPins()
	after, err := plan.Playbook.Steps[len(plan.Playbook.Steps)-1].ExpectedConnectivity.NamedPins()
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	for k, v := range before {
		w := after[k]
		if (v == nil) != (w == nil) || (v != nil && *v != *w) {
			changed++
		}
	}
	if changed != 2 || after[`"U1"/"39"`] == nil || *after[`"U1"/"39"`] != "LED_CTRL" || after[`"U1"/"38"`] != nil {
		t.Fatalf("after checkpoint wrong (%d pins changed)", changed)
	}
	// the input snapshot is untouched
	if b2, _ := sch.NamedPins(); *b2[`"U1"/"38"`] != "LED_CTRL" {
		t.Fatal("snapshot mutated")
	}
}

func TestPinSwapPlanRejectsStale(t *testing.T) {
	sch := loadMCUPage(t)
	it := ledSwapItem()
	it.Proposal.Swaps[0].FromPin = "37" // ESP_TXD lives there
	if _, err := buildPinSwapPlan(it, sch); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("want stale error, got %v", err)
	}
	it = ledSwapItem()
	it.Applyable = false
	if _, err := buildPinSwapPlan(it, sch); err == nil {
		t.Fatal("non-applyable item compiled")
	}
}

func TestPinSwapCmdDryRunWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	fb := &pcbauto.Feedback{SchemaVersion: 1, Items: []*pcbauto.FeedbackItem{ledSwapItem()}}
	raw, _ := json.Marshal(fb)
	p := filepath.Join(dir, "feedback.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"sch", "pin-swap", "--plan", p, "--item", "FB01", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no --sch snapshot") || !strings.Contains(errb.String(), "+ U1.39(IO1): LED_CTRL") {
		t.Fatalf("stderr: %s", errb.String())
	}
	var plan pinSwapPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Status != "live-unverified" || plan.Playbook == nil {
		t.Fatalf("plan %+v", plan)
	}
	for _, s := range plan.Playbook.Steps {
		if s.ExpectedConnectivity != nil {
			t.Fatal("checkpoint without a snapshot")
		}
	}
}
