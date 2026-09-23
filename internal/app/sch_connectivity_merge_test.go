package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func connectivityMergePage(page, ref string) connectivity.Document {
	return connectivity.Document{
		SchemaVersion: "1.4", ProjectID: "p", DocumentID: page,
		Components:  []connectivity.Component{{ID: "cmp-" + ref, Ref: ref, PageID: page, Pins: []connectivity.Pin{{Number: "1"}, {Number: "2", NoConnected: true}}}},
		Nets:        []connectivity.Net{{ID: "gnd", Name: "GND", Role: "ground", Scope: "global"}},
		Connections: []connectivity.Connection{{ComponentID: "cmp-" + ref, PinNumber: "1", NetID: "gnd"}},
		Modules:     []connectivity.Module{{ID: page, Name: page, CoreComponents: []string{"cmp-" + ref}}},
	}
}

func TestMergeConnectivitySharedNetsAndModuleProvenance(t *testing.T) {
	a, b := connectivityMergePage("a", "U1"), connectivityMergePage("b", "U2")
	b.Components[0].Pins[1].NoConnected = false
	b.Modules = append(b.Modules, a.Modules[0]) // one shared module declaration
	got, err := mergeSchConnectivityPages([]connectivity.Document{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "p" || got.DocumentID != "" || len(got.Nets) != 1 || len(got.Components) != 2 || len(got.Connections) != 2 || len(got.Modules) != 2 {
		t.Fatalf("incorrect project aggregation: %+v", got)
	}
	if len(got.Issues) != 1 || got.Issues[0].ComponentID != "cmp-U2" {
		t.Fatalf("lost or duplicated issue: %+v", got.Issues)
	}
	if got.Components[0].PageID != "a" || got.Components[1].PageID != "b" || !got.Components[0].Pins[1].NoConnected {
		t.Fatal("page provenance or NC was lost")
	}
	named, err := got.NamedPins()
	if err != nil || len(named) != 4 || *named[`"U1"/"1"`] != "GND" || *named[`"U2"/"1"`] != "GND" {
		t.Fatalf("shared net changed: %v, %v", named, err)
	}
	one, err := mergeSchConnectivityPages([]connectivity.Document{a})
	if err != nil || one.DocumentID != "a" {
		t.Fatalf("single-page identity lost: %+v %v", one, err)
	}
}

func TestMergeConnectivityRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, contains string
		mutate         func(*connectivity.Document)
	}{
		{"project", "different projects", func(d *connectivity.Document) { d.ProjectID = "elsewhere" }},
		{"net-name", "conflicting definitions", func(d *connectivity.Document) { d.Nets[0].Name = "+3V3" }},
		{"net-scope", "conflicting definitions", func(d *connectivity.Document) { d.Nets[0].Scope = "local" }},
		{"net-role", "conflicting definitions", func(d *connectivity.Document) { d.Nets[0].Role = "power" }},
		{"module", "module a", func(d *connectivity.Document) { d.Modules[0].ID = "a" }},
		{"page", "duplicate page", func(d *connectivity.Document) { d.DocumentID = "a" }},
		{"component-page", "conflicting page identity", func(d *connectivity.Document) { d.Components[0].PageID = "wrong" }},
		{"ref", "duplicate component", func(d *connectivity.Document) { d.Components[0].Ref = "U1" }},
		{"missing-project", "requires projectId", func(d *connectivity.Document) { d.ProjectID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := connectivityMergePage("a", "U1"), connectivityMergePage("b", "U2")
			tc.mutate(&b)
			if _, err := mergeSchConnectivityPages([]connectivity.Document{a, b}); err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("wanted %q, got %v", tc.contains, err)
			}
		})
	}
}

func TestConnectivityAllPagesRestoresOriginalAndRejectsMixedEvidence(t *testing.T) {
	for _, failure := range []string{"", "read-failed", "wrong-project", "wrong-pin-page", "missing-pins", "restore-failed"} {
		t.Run(failure, func(t *testing.T) {
			active := "a" // first listed page already active: old restore code missed this case
			cfg, daemon, closeServer := newAutolayoutTestDaemon(t, func(_ int, c autolayoutTestCall) string {
				project, doc := "p", active
				result := map[string]any{}
				switch c.Action {
				case "document.open":
					if failure == "restore-failed" && active == "b" && c.Payload["uuid"] == "a" {
						return `{"ok":false,"error":{"code":"EDA_CALL_FAILED","message":"test restore failure"}}`
					}
					active = c.Payload["uuid"].(string)
					doc = active
				case "document.current":
					result["uuid"] = active
				case "schematic.pages.list":
					result["pages"] = []any{map[string]any{"uuid": "a", "name": "A"}, map[string]any{"uuid": "b", "name": "B"}}
				case "pcb.documents.list":
					result["pcbs"] = []any{}
				case "schematic.components.list", "schematic.read":
					ref := "U1"
					if active == "b" {
						ref = "U2"
						if c.Action == "schematic.read" && failure == "read-failed" {
							return `{"ok":false,"error":{"code":"EDA_CALL_FAILED","message":"test read failure"}}`
						}
						if c.Action == "schematic.read" && failure == "wrong-project" {
							project = "other"
						}
						if c.Payload["includePins"] == true && failure == "wrong-pin-page" {
							doc = "a"
						}
					}
					result["count"] = 1
					if !(active == "b" && c.Payload["includePins"] == true && failure == "missing-pins") {
						result["components"] = []any{map[string]any{"componentType": "part", "designator": ref, "pins": []any{map[string]any{"number": "1", "net": "GND"}}}}
					}
				}
				b, _ := json.Marshal(result)
				return fmt.Sprintf(`{"ok":true,"context":{"projectUuid":%q,"documentUuid":%q,"documentType":"schematic"},"result":%s}`, project, doc, b)
			})
			defer closeServer()
			window := "w1"
			var stdout bytes.Buffer
			cmd := newSchConnectivityCmd(cfg, &window, &stdout, io.Discard)
			cmd.SetArgs([]string{"--all-pages"})
			err := cmd.Execute()
			if failure == "" {
				if err != nil {
					t.Fatal(err)
				}
				var got connectivity.Document
				if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got.Nets) != 1 || len(got.Components) != 2 || got.ProjectID != "p" || got.DocumentID != "" {
					t.Fatalf("invalid exported aggregate: %s (%v)", stdout.String(), err)
				}
			} else if err == nil || stdout.Len() != 0 {
				t.Fatalf("mixed/failed data emitted: %s (%v)", stdout.String(), err)
			}
			if failure == "restore-failed" && (err == nil || !strings.Contains(err.Error(), "restore original document")) {
				t.Fatalf("restore failure was hidden: %v", err)
			}
			if failure != "restore-failed" && active != "a" {
				t.Fatalf("original document was not restored after %s", failure)
			}
			calls := daemon.snapshot()
			last := calls[len(calls)-1]
			if last.Action != "document.open" || last.Payload["uuid"] != "a" {
				t.Fatalf("last action must restore page a: %+v", last)
			}
		})
	}
}
