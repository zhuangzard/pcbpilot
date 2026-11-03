package kicad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSlowBridgeEnv makes this test binary, run as the bridge's Python, sleep
// (TestMain).
const fakeSlowBridgeEnv = "PCBPILOT_FAKE_SLOW_BRIDGE"

// fakeSlowBridge is this test binary run as the bridge's Python: SCRIPT edit PCB
// OPS OUT. Mode 1 only sleeps; partial writes a truncated board, then sleeps; ok
// saves a board and sidecars and reports success; bad writes a truncated board
// and reports failure; none reports success and saves nothing.
func fakeSlowBridge() {
	mode := os.Getenv(fakeSlowBridgeEnv)
	if mode == "" {
		return
	}
	out := os.Args[len(os.Args)-1]
	stem := strings.TrimSuffix(out, filepath.Ext(out))
	report := func(res string) {
		fmt.Println(resultPrefix + res)
		os.Exit(0)
	}
	switch mode {
	case "partial":
		_ = os.WriteFile(out, []byte("(kicad_pcb (trunc"), 0o644)
		_ = os.WriteFile(stem+".kicad_pro", []byte("{trunc"), 0o644)
	case "ok":
		_ = os.WriteFile(out, []byte("(kicad_pcb new)"), 0o644)
		_ = os.WriteFile(stem+".kicad_pro", []byte("{\"new\":1}"), 0o644)
		_ = os.WriteFile(stem+".kicad_dru", []byte("(version 1) new"), 0o644)
		b, _ := json.Marshal(map[string]any{"ok": true, "out": out, "done": 1})
		report(string(b))
	case "bad":
		_ = os.WriteFile(out, []byte("(kicad_pcb (trunc"), 0o644)
		report(`{"ok":false,"error":"refused"}`)
	case "none":
		report(`{"ok":true}`)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// A deadline reaches the bridge process: it is killed, the caller gets the
// context's error at once, and an edit leaves no result behind.
func TestBridgeContextKillsBridge(t *testing.T) {
	t.Setenv(fakeSlowBridgeEnv, "sleep")
	tools := &Tools{Python: os.Args[0]}
	dir := t.TempDir()
	calls := map[string]func(ctx context.Context) error{
		"snapshot": func(ctx context.Context) error {
			_, err := tools.SnapshotContext(ctx, filepath.Join(dir, "in.kicad_pcb"))
			return err
		},
		"edit": func(ctx context.Context) error {
			_, err := tools.EditContext(ctx, filepath.Join(dir, "in.kicad_pcb"), map[string]any{"setText": []any{}}, filepath.Join(dir, "out.kicad_pcb"))
			return err
		},
	}
	for name, call := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		start := time.Now()
		err := call(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(start); d > 30*time.Second {
			t.Fatalf("%s: returned after %s", name, d)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out.kicad_pcb")); !os.IsNotExist(err) {
		t.Fatalf("edit left a result: %v", err)
	}
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func wantFile(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s = %q, want %q", p, b, want)
	}
}

// noTemp: no temporary board or sidecar is left beside out (a killed process
// may hold one for a moment on Windows).
func noTemp(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		left, _ := filepath.Glob(filepath.Join(dir, "*.pcbpilot-*"))
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("left behind: %v", left)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A bridge killed after writing part of its output leaves out as it was: absent
// or with the last good board, and the same for its sidecars.
func TestBridgeEditKilledMidSaveLeavesOutUntouched(t *testing.T) {
	t.Setenv(fakeSlowBridgeEnv, "partial")
	tools := &Tools{Python: os.Args[0]}
	for name, existing := range map[string]bool{"no earlier result": false, "over an earlier result": true} {
		dir := t.TempDir()
		pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
		pro := filepath.Join(dir, "out.kicad_pro")
		writeFiles(t, map[string]string{pcb: "(kicad_pcb in)"})
		if existing {
			writeFiles(t, map[string]string{out: "(kicad_pcb old)", pro: "{\"old\":1}"})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, err := tools.EditContext(ctx, pcb, map[string]any{}, out)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", name, err)
		}
		wantFile(t, pcb, "(kicad_pcb in)")
		if existing {
			wantFile(t, out, "(kicad_pcb old)")
			wantFile(t, pro, "{\"old\":1}")
		} else if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("%s: truncated board at out: %v", name, err)
		}
		noTemp(t, dir)
	}
}

// A bridge that reports failure after a partial write, or success without a
// board: an error, and out untouched.
func TestBridgeEditFailedSaveLeavesOutUntouched(t *testing.T) {
	for mode, want := range map[string]string{"bad": "refused", "none": "saved no board"} {
		t.Setenv(fakeSlowBridgeEnv, mode)
		dir := t.TempDir()
		pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
		writeFiles(t, map[string]string{pcb: "(kicad_pcb in)", out: "(kicad_pcb old)"})
		_, err := (&Tools{Python: os.Args[0]}).EditContext(context.Background(), pcb, map[string]any{}, out)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", mode, err)
		}
		wantFile(t, out, "(kicad_pcb old)")
		noTemp(t, dir)
	}
}

// A successful save replaces out on every platform: over an existing file, over
// itself, with its sidecars as a direct save would leave them, and the result
// names out. The bridge was told a temporary board beside out.
func TestBridgeEditCommitsOverExistingOut(t *testing.T) {
	t.Setenv(fakeSlowBridgeEnv, "ok")
	tools := &Tools{Python: os.Args[0]}
	edit := func(t *testing.T, pcb, out string) {
		t.Helper()
		raw, err := tools.Edit(pcb, map[string]any{}, out)
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Out  string `json:"out"`
			Done int    `json:"done"`
		}
		if err := json.Unmarshal(raw, &res); err != nil || res.Out != out || res.Done != 1 {
			t.Fatalf("%s %v", raw, err)
		}
		wantFile(t, out, "(kicad_pcb new)")
		noTemp(t, filepath.Dir(out))
	}
	t.Run("fresh", func(t *testing.T) {
		dir := t.TempDir()
		pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
		writeFiles(t, map[string]string{pcb: "(kicad_pcb in)"})
		edit(t, pcb, out)
		wantFile(t, filepath.Join(dir, "out.kicad_pro"), "{\"new\":1}")
		wantFile(t, filepath.Join(dir, "out.kicad_dru"), "(version 1) new")
	})
	t.Run("over an earlier result", func(t *testing.T) {
		dir := t.TempDir()
		pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
		dru := filepath.Join(dir, "out.kicad_dru")
		prl := filepath.Join(dir, "out.kicad_prl")
		writeFiles(t, map[string]string{pcb: "(kicad_pcb in)", out: "(kicad_pcb old)", filepath.Join(dir, "out.kicad_pro"): "{\"old\":1}", dru: "(version 1) kept", prl: "{}"})
		edit(t, pcb, out)
		wantFile(t, filepath.Join(dir, "out.kicad_pro"), "{\"new\":1}")
		wantFile(t, dru, "(version 1) kept") // save_board: a rules file at out stays
		if _, err := os.Stat(prl); !os.IsNotExist(err) {
			t.Fatalf("stale .kicad_prl: %v", err)
		}
	})
	t.Run("in place", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "b.kicad_pcb")
		prl := filepath.Join(dir, "b.kicad_prl")
		writeFiles(t, map[string]string{out: "(kicad_pcb old)", prl: "{}"})
		edit(t, out, out)
		wantFile(t, prl, "{}") // a save over itself never removed sidecars
	})
}

// On Windows a target held open without delete sharing cannot be replaced: the
// commit fails after its retries, out keeps its bytes, nothing is left behind,
// and it succeeds once the holder is gone.
func TestBridgeEditCommitTargetHeldOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("POSIX rename replaces an open file")
	}
	t.Setenv(fakeSlowBridgeEnv, "ok")
	tools := &Tools{Python: os.Args[0]}
	dir := t.TempDir()
	pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
	writeFiles(t, map[string]string{pcb: "(kicad_pcb in)", out: "(kicad_pcb old)", filepath.Join(dir, "out.kicad_pro"): "{\"old\":1}", filepath.Join(dir, "out.kicad_prl"): "old prl"})
	f, err := os.OpenFile(out, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.Edit(pcb, map[string]any{}, out)
	f.Close()
	if err == nil {
		// os.OpenFile allowed delete sharing on this Go: POSIX-semantics replace.
		wantFile(t, out, "(kicad_pcb new)")
		return
	}
	wantFile(t, out, "(kicad_pcb old)")
	wantFile(t, filepath.Join(dir, "out.kicad_pro"), "{\"old\":1}") // the rules stay with the board
	wantFile(t, filepath.Join(dir, "out.kicad_prl"), "old prl")
	if _, err := os.Stat(filepath.Join(dir, "out.kicad_dru")); !os.IsNotExist(err) {
		t.Fatalf("new rules beside the old board: %v", err)
	}
	noTemp(t, dir)
	if left, _ := filepath.Glob(filepath.Join(dir, "*.bak")); len(left) != 0 {
		t.Fatalf("%v", left)
	}
	if _, err := tools.Edit(pcb, map[string]any{}, out); err != nil {
		t.Fatal(err)
	}
	wantFile(t, out, "(kicad_pcb new)")
}

// The files of an output set, absent ones as absent.
func readSet(t *testing.T, stem string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_prl", ".kicad_dru"} {
		if b, err := os.ReadFile(stem + ext); err == nil {
			out[ext] = string(b)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return out
}

// Every rename and delete of a commit, failing in turn, leaves the complete
// previous set of bytes and nothing else; so does a board held open by another
// process, here injected for every platform.
func TestBridgeCommitBoardRollsBack(t *testing.T) {
	rename, remove := commitRename, commitRemove
	defer func() { commitRename, commitRemove = rename, remove }()
	for _, c := range []struct {
		name string
		prev map[string]string // files at out
		tmp  map[string]string // files the bridge saved
	}{
		{"replaces the project", map[string]string{".kicad_pcb": "old", ".kicad_pro": "old pro", ".kicad_prl": "old prl", ".kicad_dru": "old dru"},
			map[string]string{".kicad_pcb": "new", ".kicad_pro": "new pro", ".kicad_prl": "new prl", ".kicad_dru": "new dru"}},
		{"deletes what it did not write", map[string]string{".kicad_pcb": "old", ".kicad_pro": "old pro", ".kicad_prl": "old prl"},
			map[string]string{".kicad_pcb": "new"}},
		{"adds new files", map[string]string{".kicad_pcb": "old"},
			map[string]string{".kicad_pcb": "new", ".kicad_pro": "new pro", ".kicad_prl": "new prl", ".kicad_dru": "new dru"}},
		{"no earlier result", nil, map[string]string{".kicad_pcb": "new", ".kicad_pro": "new pro", ".kicad_dru": "new dru"}},
	} {
		for k := 0; ; k++ {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.kicad_pcb")
			outStem, tmpStem := filepath.Join(dir, "out"), filepath.Join(dir, "out.pcbpilot-7")
			writeFiles(t, map[string]string{src: "in"})
			for ext, v := range c.prev {
				writeFiles(t, map[string]string{outStem + ext: v})
			}
			for ext, v := range c.tmp {
				writeFiles(t, map[string]string{tmpStem + ext: v})
			}
			ops := 0
			boom := errors.New("injected")
			fail := func() bool { ops++; return ops == k+1 }
			commitRename = func(from, to string) error {
				if fail() {
					return boom
				}
				return rename(from, to)
			}
			commitRemove = func(p string) error {
				if fail() {
					return boom
				}
				return remove(p)
			}
			err := commitBoard(src, tmpStem+".kicad_pcb", outStem+".kicad_pcb")
			if ops <= k { // past the last operation: this was the clean commit
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				if got := readSet(t, outStem); got[".kicad_pcb"] != "new" {
					t.Fatalf("%s: %v", c.name, got)
				}
				if left, _ := filepath.Glob(filepath.Join(dir, "*.bak")); len(left) != 0 {
					t.Fatalf("%s: %v", c.name, left)
				}
				if k == 0 {
					t.Fatalf("%s: no operation", c.name)
				}
				break
			}
			if !errors.Is(err, boom) {
				t.Fatalf("%s op %d: %v", c.name, k, err)
			}
			got := readSet(t, outStem)
			if len(got) != len(c.prev) {
				t.Fatalf("%s op %d: %v, want %v", c.name, k, got, c.prev)
			}
			for ext, v := range c.prev {
				if got[ext] != v {
					t.Fatalf("%s op %d: %s = %q, want %q", c.name, k, ext, got[ext], v)
				}
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "*.bak")); len(left) != 0 {
				t.Fatalf("%s op %d: %v", c.name, k, left)
			}
		}
	}
}

// When putting back fails too, the previous bytes are still there, and named.
func TestBridgeCommitBoardRestoreFailureKeepsBackups(t *testing.T) {
	rename := commitRename
	defer func() { commitRename = rename }()
	dir := t.TempDir()
	outStem, tmpStem := filepath.Join(dir, "out"), filepath.Join(dir, "out.pcbpilot-7")
	writeFiles(t, map[string]string{filepath.Join(dir, "in.kicad_pcb"): "in", outStem + ".kicad_pcb": "old", outStem + ".kicad_pro": "old pro",
		tmpStem + ".kicad_pcb": "new", tmpStem + ".kicad_pro": "new pro"})
	n := 0
	commitRename = func(from, to string) error {
		if n++; n == 1 {
			return rename(from, to) // the project goes in
		}
		return errors.New("injected")
	}
	err := commitBoard(filepath.Join(dir, "in.kicad_pcb"), tmpStem+".kicad_pcb", outStem+".kicad_pcb")
	if err == nil || !strings.Contains(err.Error(), "kept as") || !strings.Contains(err.Error(), ".bak") {
		t.Fatalf("%v", err)
	}
	wantFile(t, outStem+".kicad_pcb", "old")
	left, _ := filepath.Glob(filepath.Join(dir, "*.bak"))
	found := false
	for _, p := range left {
		b, _ := os.ReadFile(p)
		found = found || string(b) == "old pro"
	}
	if !found {
		t.Fatalf("no backup of the project: %v", left)
	}
}

// A committed project file names itself, not the temporary board.
func TestBridgeCommitBoardNamesProject(t *testing.T) {
	dir := t.TempDir()
	outStem, tmpStem := filepath.Join(dir, "out"), filepath.Join(dir, "out.pcbpilot-7")
	writeFiles(t, map[string]string{filepath.Join(dir, "in.kicad_pcb"): "in", tmpStem + ".kicad_pcb": "new",
		tmpStem + ".kicad_pro": `{"meta":{"filename":"out.pcbpilot-7.kicad_pro","version":3}}`,
		tmpStem + ".kicad_prl": `{"meta":{"filename":"out.pcbpilot-7.kicad_prl"}}`})
	if err := commitBoard(filepath.Join(dir, "in.kicad_pcb"), tmpStem+".kicad_pcb", outStem+".kicad_pcb"); err != nil {
		t.Fatal(err)
	}
	wantFile(t, outStem+".kicad_pro", `{"meta":{"filename":"out.kicad_pro","version":3}}`)
	wantFile(t, outStem+".kicad_prl", `{"meta":{"filename":"out.kicad_prl"}}`)
}

// Real pcbnew: an edit through the temporary board gives out a board that loads,
// the edit in it, its rules beside it and the same identities; in place too.
func TestBridgeEditAtomicKiCadLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1")
	}
	kt, err := Locate()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("testdata", "tiny.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	pcb, out := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb")
	writeFiles(t, map[string]string{pcb: string(b),
		filepath.Join(dir, "in.kicad_dru"): "(version 1)\n(rule r (constraint clearance (min 0.11mm)))\n",
		filepath.Join(dir, "in.kicad_pro"): `{"net_settings":{"classes":[{"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.6,"via_drill":0.3,"priority":2147483647}]},"meta":{"filename":"in.kicad_pro","version":3}}`})
	ops := map[string]any{"addVias": []any{map[string]any{"net": "GND", "x": 0, "y": 0, "diameter": 30.0, "drill": 15.0}}}
	if _, err := kt.Edit(pcb, ops, out); err != nil {
		t.Fatal(err)
	}
	noTemp(t, dir)
	count := func(p string) int {
		raw, err := kt.Snapshot(p)
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Copper struct {
				Vias []struct {
					ID string `json:"primitiveId"`
				} `json:"vias"`
			} `json:"copper"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return len(s.Copper.Vias)
	}
	n := count(out)
	if n == 0 || n != count(pcb)+1 {
		t.Fatalf("vias: out %d in %d", n, count(pcb))
	}
	for _, ext := range []string{".kicad_pro", ".kicad_dru"} {
		if _, err := os.Stat(filepath.Join(dir, "out"+ext)); err != nil {
			t.Fatal(err)
		}
	}
	var pro struct {
		Meta struct {
			Filename string `json:"filename"`
		} `json:"meta"`
	}
	proBytes, err := os.ReadFile(filepath.Join(dir, "out.kicad_pro"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(proBytes, &pro); err != nil {
		t.Fatal(err)
	}
	if pro.Meta.Filename != "out.kicad_pro" || strings.Contains(string(proBytes), "pcbpilot-") {
		t.Fatalf("meta.filename %q:\n%s", pro.Meta.Filename, proBytes)
	}
	rb, err := kt.RulesRead(out)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Nets["GND"].ViaDiameter != 600000 {
		t.Fatalf("the class did not stay with the board: %+v", rb.Nets["GND"])
	}
	// Over itself, as the DRC step-back does.
	if _, err := kt.Edit(out, ops, out); err != nil {
		t.Fatal(err)
	}
	if got := count(out); got != n+1 {
		t.Fatalf("in place: %d vias, want %d", got, n+1)
	}
	noTemp(t, dir)
}
