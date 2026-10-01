package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApiUpstreamDiffOffline(t *testing.T) {
	dir := t.TempDir()
	dts := filepath.Join(dir, "index.d.ts")
	// A tiny "newer" surface: the pinned sch_PrimitiveWire is gone, a bus class appears.
	src := "declare global {\n\tclass SCH_PrimitiveBus {\n\t\t/**\n\t\t * 创建总线\n\t\t * @beta\n\t\t */\n\t\tpublic create(name: string, line: Array<number>): Promise<ISCH_PrimitiveBus | undefined>;\n\t}\n\tclass EDA {\n\t\tpublic sch_PrimitiveBus: SCH_PrimitiveBus;\n\t}\n}\n"
	if err := os.WriteFile(dts, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"9.9.9"}`), 0o644)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"api", "upstream-diff", dts, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var rep struct {
		Pinned struct {
			Version string `json:"version"`
		} `json:"pinned"`
		Newer struct {
			Version string `json:"version"`
		} `json:"newer"`
		BreakingForConnector []struct {
			Namespace string `json:"namespace"`
			Kind      string `json:"kind"`
		} `json:"breakingForConnector"`
		Counts struct{ Removed int } `json:"counts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Pinned.Version == "" || rep.Newer.Version != "9.9.9" || rep.Counts.Removed == 0 || len(rep.BreakingForConnector) == 0 {
		t.Fatalf("report = %+v", rep)
	}

	// --fail-on-breaking turns used-and-removed methods into exit 3.
	stdout.Reset()
	if code := Run([]string{"api", "upstream-diff", dts, "--fail-on-breaking"}, &stdout, &stderr); code != 3 {
		t.Fatalf("--fail-on-breaking exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "notable unlocks") || !strings.Contains(stdout.String(), "eda.sch_PrimitiveBus.create") {
		t.Errorf("text output:\n%s", stdout.String())
	}

	// Exactly one source: neither or both is a usage error (no network touched).
	for _, args := range [][]string{{"api", "upstream-diff"}, {"api", "upstream-diff", dts, "--fetch"}} {
		stderr.Reset()
		if code := Run(args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "exactly one newer source") {
			t.Errorf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
	}
}
