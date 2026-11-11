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

func TestSelectWindowSameHostAcrossVersionsCollapses(t *testing.T) {
	const chrome141 = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	const chrome142 = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.7444.60 Safari/537.36"
	const edge142 = chrome142 + " Edg/142.0.3595.53"
	// The browser updated itself between the stale registration and the new
	// one: still one host, so the newest registration wins.
	if id, err := selectWindow([]healthWindow{sameTab("old", chrome141, 1), sameTab("new", chrome142, 2)}, "ceshi", ""); err != nil || id != "new" {
		t.Fatalf("same browser, new version = (%q,%v), want newest", id, err)
	}
	// Same for the desktop shell.
	deskOld := sameTab("deskOld", "Chrome/152.0 edadesk/0.0.1 Electron/44.4.5", 1)
	deskNew := sameTab("deskNew", "Chrome/153.1 edadesk/0.0.2 Electron/45.0.0", 2)
	deskOld.Host.Kind, deskNew.Host.Kind = "edadesk", "edadesk"
	if id, err := selectWindow([]healthWindow{deskOld, deskNew}, "ceshi", ""); err != nil || id != "deskNew" {
		t.Fatalf("same shell, new version = (%q,%v), want newest", id, err)
	}
	// Two different browsers are two live editors, whatever their versions.
	if _, err := selectWindow([]healthWindow{sameTab("chrome", chrome142, 1), sameTab("edge", edge142, 2)}, "ceshi", ""); err == nil {
		t.Fatal("different browsers on the same tab must stay ambiguous")
	}
	// A registration without host data next to one with it is not provably
	// the same host.
	if _, err := selectWindow([]healthWindow{sameTab("legacy", "", 1), sameTab("new", chrome142, 2)}, "ceshi", ""); err == nil {
		t.Fatal("host-less and host-reporting registrations must stay ambiguous")
	}
	// The same user agent under two kinds is two hosts.
	other := sameTab("other", chrome142, 2)
	other.Host.Kind = "electron"
	if _, err := selectWindow([]healthWindow{sameTab("tab", chrome142, 1), other}, "ceshi", ""); err == nil {
		t.Fatal("different host kinds must stay ambiguous")
	}
}
