package app

import (
	"strings"
	"testing"
	"time"
)

func win(id, proj string) healthWindow {
	var w healthWindow
	w.WindowID = id
	w.Context.ProjectName = proj
	return w
}

func TestSelectWindow(t *testing.T) {
	a := win("w-esp", "立创·实战派ESP32-S3开发板")
	b := win("w-moto", "motobox2026")

	// explicit --window always wins, even with many windows
	if id, err := selectWindow([]healthWindow{a, b}, "", "w-x"); err != nil || id != "w-x" {
		t.Fatalf("explicit window: %q %v", id, err)
	}
	// --project unique match (CJK name)
	if id, err := selectWindow([]healthWindow{a, b}, "立创·实战派ESP32-S3开发板", ""); err != nil || id != "w-esp" {
		t.Fatalf("project match: %q %v", id, err)
	}
	// sole window, no project
	if id, err := selectWindow([]healthWindow{b}, "", ""); err != nil || id != "w-moto" {
		t.Fatalf("sole window: %q %v", id, err)
	}
	// 2+ windows, no project → error
	if _, err := selectWindow([]healthWindow{a, b}, "", ""); err == nil {
		t.Fatal("expected multi-window error")
	}
	// project not found → error
	if _, err := selectWindow([]healthWindow{a, b}, "ghost", ""); err == nil {
		t.Fatal("expected no-match error")
	}
	// project maps to 2 windows → error
	if _, err := selectWindow([]healthWindow{win("w1", "dup"), win("w2", "dup")}, "dup", ""); err == nil {
		t.Fatal("expected ambiguous-project error")
	}
	// no windows → error
	if _, err := selectWindow(nil, "", ""); err == nil {
		t.Fatal("expected no-connector error")
	}
}

func sameTab(id, ua string, at int64) healthWindow {
	w := win(id, "ceshi")
	w.Context.ProjectUUID, w.Context.DocumentUUID, w.Context.DocumentType = "p", "d", "pcb"
	w.Context.TabID = "d@p"
	w.Host.UserAgent = ua
	if ua != "" {
		w.Host.Kind = "browser"
	}
	w.ConnectedAt = time.Unix(at, 0)
	return w
}

func TestSelectWindowSameTabDifferentHostsStayAmbiguous(t *testing.T) {
	// Reload leftovers from one host collapse to the newest registration.
	if id, err := selectWindow([]healthWindow{sameTab("old", "Chrome/141", 1), sameTab("new", "Chrome/141", 2)}, "ceshi", ""); err != nil || id != "new" {
		t.Fatalf("same-host duplicates = (%q,%v), want newest", id, err)
	}
	// Older connectors report no user agent: keep the previous behaviour.
	if id, err := selectWindow([]healthWindow{sameTab("old", "", 1), sameTab("new", "", 2)}, "ceshi", ""); err != nil || id != "new" {
		t.Fatalf("legacy duplicates = (%q,%v), want newest", id, err)
	}
	// A browser tab and the edadesk shell on the same PCB are two editors.
	desk := sameTab("desk", "Chrome/152 edadesk/0.0.1 Electron/44.4.5", 2)
	desk.Host.Kind = "edadesk"
	_, err := selectWindow([]healthWindow{sameTab("tab", "Chrome/141", 1), desk}, "ceshi", "")
	if err == nil || !strings.Contains(err.Error(), "desk [host=edadesk doc=pcb]") || !strings.Contains(err.Error(), "tab [host=browser doc=pcb]") {
		t.Fatalf("cross-host same tab must be ambiguous and name hosts, got %v", err)
	}
}
