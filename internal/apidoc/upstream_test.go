package apidoc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var updateUsage = flag.Bool("update-usage", false, "rewrite connector-usage.json from extension/src")

// Two synthetic pro-api-types snapshots: the 0.2-era layout (no modifiers) and
// the 0.4 layout (`public` members, generic method, union surface property).
const dtsOld = `declare global {
	class PCB_Document {
		/**
		 * 自动布线
		 * @alpha
		 */
		autoRouting(props?: IPCB_AutoRoutingProps): Promise<IPCB_AutoRoutingResult>;
		/**
		 * 保存
		 */
		save(): Promise<boolean>;
		/**
		 * 旧接口
		 */
		legacyThing(): void;
	}
	class SCH_PrimitiveAttribute {
		/**
		 * 创建网络标签
		 * @alpha
		 */
		createNetLabel(x: number, y: number, net: string): Promise<ISCH_PrimitiveAttribute | undefined>;
	}
	class SCH_PrimitiveWire {
		/**
		 * 创建导线
		 */
		create(line: Array<number>, net?: string): Promise<ISCH_PrimitiveWire | undefined>;
	}
	class EDA {
		pcb_Document: PCB_Document;
		sch_PrimitiveAttribute: SCH_PrimitiveAttribute;
		sch_PrimitiveWire: SCH_PrimitiveWire;
	}
}
`

const dtsNew = `declare global {
	class PCB_Document {
		/**
		 * 自动布线
		 * @beta
		 */
		public autoRouting(props?: IPCB_AutoRoutingProps): Promise<IPCB_AutoRoutingResult>;
		/**
		 * 保存
		 */
		public save(): Promise<boolean>;
		private internalHelper(): void;
	}
	class SCH_PrimitiveAttribute {
		/**
		 * 创建网络标签
		 * @beta
		 */
		public createNetLabel(x: number, y: number, net: string): Promise<ISCH_PrimitiveAttribute | undefined>;
	}
	class SCH_PrimitiveWire {
		/**
		 * 创建导线
		 */
		public create(line: Array<number>, net?: string, color?: string): Promise<ISCH_PrimitiveWire | undefined>;
	}
	class SCH_PrimitiveBus {
		/**
		 * 创建总线
		 * @beta
		 */
		public create<T extends string>(name: T, line: Array<number>): Promise<ISCH_PrimitiveBus | undefined>;
	}
	class SCH_PrimitiveBus2 {
		/**
		 * 获取全部总线
		 */
		public getAll(): Promise<Array<ISCH_PrimitiveBus>>;
	}
	class EDA {
		public pcb_Document: PCB_Document;
		public sch_PrimitiveAttribute: SCH_PrimitiveAttribute;
		public sch_PrimitiveWire: SCH_PrimitiveWire;
		public sch_PrimitiveBus: SCH_PrimitiveBus | SCH_PrimitiveBus2;
	}
}
`

func mustParse(t *testing.T, src string) []Method {
	t.Helper()
	m, err := ParseDTS([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseDTSBothLayouts(t *testing.T) {
	old := mustParse(t, dtsOld)
	if len(old) != 5 || old[0].NS != "eda.pcb_Document" || old[0].Stability != "alpha" || old[0].Summary != "自动布线" {
		t.Fatalf("old = %+v", old)
	}
	nw := mustParse(t, dtsNew)
	got := map[string]Method{}
	for _, m := range nw {
		got[m.NS+"."+m.Method] = m
	}
	if _, ok := got["eda.pcb_Document.internalHelper"]; ok {
		t.Error("private member indexed")
	}
	if m := got["eda.sch_PrimitiveBus.create"]; !strings.HasPrefix(m.Sig, "create<T extends string>(") || m.Stability != "beta" {
		t.Errorf("generic method = %+v", m)
	}
	if _, ok := got["eda.sch_PrimitiveBus.getAll"]; !ok {
		t.Error("union surface member missing")
	}
	if m := got["eda.pcb_Document.save"]; strings.HasPrefix(m.Sig, "public") {
		t.Errorf("modifier not stripped: %q", m.Sig)
	}
	if _, err := ParseDTS([]byte("declare global { class X { a(): void; } }")); err == nil {
		t.Error("no class EDA must be an error")
	}
}

func TestDiffSyntheticSnapshots(t *testing.T) {
	pinned := Snapshot{Version: "0.2.0", Format: "dts", Methods: mustParse(t, dtsOld), HasSignatures: true}
	newer := Snapshot{Version: "0.4.0", Format: "dts", Methods: mustParse(t, dtsNew), HasSignatures: true}
	usage := Usage{Source: "test",
		Namespaces: []string{"eda.pcb_Document", "eda.sch_PrimitiveWire", "eda.sch_PrimitiveBus"},
		Methods:    []string{"eda.pcb_Document.save", "eda.pcb_Document.legacyThing", "eda.sch_PrimitiveWire.create"}}
	r := Diff(pinned, newer, usage)
	if !reflect.DeepEqual(r.AddedClasses, []string{"eda.sch_PrimitiveBus"}) || len(r.RemovedClasses) != 0 {
		t.Fatalf("classes +%v -%v", r.AddedClasses, r.RemovedClasses)
	}
	if r.Counts.Added != 2 || r.Counts.Removed != 1 || r.Counts.Signature != 1 || r.Counts.Stability != 2 {
		t.Fatalf("counts = %+v changes=%+v", r.Counts, r.Changes)
	}
	byKey := map[string]Change{}
	for _, c := range r.Changes {
		byKey[c.NS+"."+c.Method+"/"+c.Kind] = c
	}
	if c := byKey["eda.pcb_Document.autoRouting/stability"]; !reflect.DeepEqual(c.Old, []string{"alpha"}) || !reflect.DeepEqual(c.New, []string{"beta"}) || c.Notable == "" {
		t.Errorf("autoRouting = %+v", c)
	}
	if c := byKey["eda.sch_PrimitiveAttribute.createNetLabel/stability"]; c.Notable == "" || c.NamespaceInUse {
		t.Errorf("createNetLabel = %+v", c)
	}
	if c := byKey["eda.sch_PrimitiveBus.create/added"]; c.Notable == "" || !c.NamespaceInUse || c.ConnectorUses {
		t.Errorf("bus create = %+v", c)
	}
	// Used + removed / changed signature → breaking for the connector.
	var breaking []string
	for _, c := range r.BreakingForUs {
		breaking = append(breaking, c.Method+"/"+c.Kind)
	}
	if !reflect.DeepEqual(breaking, []string{"legacyThing/removed", "create/signature"}) {
		t.Errorf("breaking = %v", breaking)
	}
	if len(r.NotableUnlocks) != 4 { // autoRouting, createNetLabel, bus create, bus getAll
		t.Errorf("notable = %+v", r.NotableUnlocks)
	}
	if r.SameVersion {
		t.Error("versions differ")
	}
	// Identity diff is empty.
	if id := Diff(newer, newer, usage); len(id.Changes) != 0 || !id.SameVersion {
		t.Errorf("identity diff = %+v", id.Changes)
	}
}

func writeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLoadSnapshotSources(t *testing.T) {
	dir := t.TempDir()
	tgz := writeTarball(t, map[string]string{"package/index.d.ts": dtsNew, "package/package.json": `{"version":"0.4.99"}`})
	p := filepath.Join(dir, "pro-api-types-0.4.99.tgz")
	if err := os.WriteFile(p, tgz, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSnapshot(p)
	if err != nil || s.Version != "0.4.99" || s.Format != "dts" || len(s.Methods) != 6 {
		t.Fatalf("tgz snapshot = %+v err=%v", s, err)
	}
	// Unpacked package directory.
	pkg := filepath.Join(dir, "unpacked", "package")
	_ = os.MkdirAll(pkg, 0o755)
	_ = os.WriteFile(filepath.Join(pkg, "index.d.ts"), []byte(dtsNew), 0o644)
	_ = os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"0.4.98"}`), 0o644)
	if s, err := LoadSnapshot(filepath.Join(dir, "unpacked")); err != nil || s.Version != "0.4.98" {
		t.Fatalf("dir snapshot = %+v err=%v", s, err)
	}
	// api-index JSON.
	idx, _ := json.Marshal(index{Source: "x", Version: "0.2.63", Records: mustParse(t, dtsOld)})
	ip := filepath.Join(dir, "api-index.json")
	_ = os.WriteFile(ip, idx, 0o644)
	if s, err := LoadSnapshot(ip); err != nil || s.Format != "api-index" || s.Version != "0.2.63" {
		t.Fatalf("index snapshot = %+v err=%v", s, err)
	}
	// Official `doc api` dump (names only), envelope form, enum skipped.
	dump := `{"version":"4.1.60","classes":[
	 {"ok":true,"value":{"title":"PCB 文档","callPath":"pcb_Document","methods":[{"name":"autoRouting","comment":"自动布线"},{"name":"save","comment":"保存"}]}},
	 {"name":"EPCB_LayerId","values":[{"name":"TOP","value":1}]}]}`
	dp := filepath.Join(dir, "doc-api.json")
	_ = os.WriteFile(dp, []byte(dump), 0o644)
	ds, err := LoadSnapshot(dp)
	if err != nil || ds.Format != "doc-api" || ds.HasSignatures || len(ds.Methods) != 2 || ds.Version != "4.1.60" {
		t.Fatalf("doc-api snapshot = %+v err=%v", ds, err)
	}
	// Names-only comparison: no signature/stability noise.
	r := Diff(Snapshot{Methods: mustParse(t, dtsOld), HasSignatures: true}, ds, Usage{})
	if r.Counts.Signature != 0 || r.Counts.Stability != 0 || r.Counts.Removed != 3 || !strings.Contains(r.Note, "names") {
		t.Errorf("names-only diff = %+v", r.Counts)
	}
	// Malformed inputs.
	bad := filepath.Join(dir, "bad.tgz")
	_ = os.WriteFile(bad, []byte("not gzip"), 0o644)
	for _, p := range []string{bad, filepath.Join(dir, "missing.d.ts"), filepath.Join(dir, "x.txt")} {
		if _, err := LoadSnapshot(p); err == nil {
			t.Errorf("%s should fail", p)
		}
	}
	empty := filepath.Join(dir, "empty.json")
	_ = os.WriteFile(empty, []byte(`{"foo":1}`), 0o644)
	if _, err := LoadSnapshot(empty); err == nil {
		t.Error("unknown JSON should fail")
	}
}

func TestFetchLatestFromRegistry(t *testing.T) {
	tgz := writeTarball(t, map[string]string{"package/index.d.ts": dtsNew, "package/package.json": `{"version":"0.4.26"}`})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/@jlceda/pro-api-types/latest":
			_, _ = w.Write([]byte(`{"version":"0.4.26","dist":{"tarball":"` + srv.URL + `/t.tgz"}}`))
		case "/t.tgz":
			_, _ = w.Write(tgz)
		case "/slow/@jlceda/pro-api-types/latest":
			time.Sleep(300 * time.Millisecond)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := FetchLatest(context.Background(), srv.URL, 5*time.Second)
	if err != nil || s.Version != "0.4.26" || !strings.HasSuffix(s.Source, "/t.tgz") {
		t.Fatalf("fetch = %+v err=%v", s, err)
	}
	if _, err := FetchLatest(context.Background(), srv.URL+"/slow", 50*time.Millisecond); err == nil {
		t.Error("timeout must fail")
	}
	if _, err := FetchLatest(context.Background(), srv.URL+"/nope", time.Second); err == nil {
		t.Error("404 must fail")
	}
}

// The embedded index must be generated from the version the connector pins;
// a stale index (0.2.63 while the connector pinned 0.4.25) went unnoticed
// for months before this guard.
func TestPinnedVersionMatchesConnectorLock(t *testing.T) {
	b, err := os.ReadFile("../../extension/package-lock.json")
	if err != nil {
		t.Skip("extension/package-lock.json not available")
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	want := lock.Packages["node_modules/@jlceda/pro-api-types"].Version
	if want == "" || PinnedVersion() != want {
		t.Fatalf("api-index.json version %q != connector lock %q — regenerate with internal/apidoc/gen.py", PinnedVersion(), want)
	}
}

// gen.py and ParseDTS must agree; checked when the pinned index.d.ts is
// installed (extension/node_modules), skipped otherwise (CI).
func TestParseDTSMatchesGenPy(t *testing.T) {
	b, err := os.ReadFile("../../extension/node_modules/@jlceda/pro-api-types/index.d.ts")
	if err != nil {
		t.Skip("pro-api-types not installed")
	}
	m, err := ParseDTS(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, loaded.Records) {
		t.Fatalf("ParseDTS (%d) differs from embedded gen.py index (%d)", len(m), len(loaded.Records))
	}
}

func TestConnectorUsageFresh(t *testing.T) {
	src := "../../extension/src"
	if _, err := os.Stat(src); err != nil {
		t.Skip("extension/src not available")
	}
	u, err := ExtractConnectorUsage(src)
	if err != nil {
		t.Fatal(err)
	}
	if *updateUsage {
		b, _ := json.MarshalIndent(u, "", " ")
		if err := os.WriteFile("connector-usage.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := ConnectorUsage(); !reflect.DeepEqual(got, u) {
		t.Fatalf("connector-usage.json is stale (%d/%d embedded vs %d/%d extracted ns/methods); run: go test ./internal/apidoc -run TestConnectorUsageFresh -update-usage",
			len(got.Namespaces), len(got.Methods), len(u.Namespaces), len(u.Methods))
	}
	for _, must := range []string{"eda.sch_PrimitiveBus.getAll", "eda.sch_PrimitiveAttribute.createNetLabel"} {
		if !contains(u.Methods, must) {
			t.Errorf("usage misses %s", must)
		}
	}
}

// 0.4 splits object types over lines; the signature must be joined and
// normalized so it compares equal to the single-line 0.2 rendering.
func TestParseDTSMultiLineSignature(t *testing.T) {
	multi := "declare global {\n\tclass DMT_EditorControl {\n\t\tpublic zoomTo(x?: number, tabId?: string): Promise<{\n\t\t\tleft: number;\n\t\t\tright: number;\n\t\t} | false>;\n\t\tpublic next(): void;\n\t}\n\tclass EDA {\n\t\tpublic dmt_EditorControl: DMT_EditorControl;\n\t}\n}\n"
	single := "declare global {\n\tclass DMT_EditorControl {\n\t\tzoomTo(x?: number, tabId?: string): Promise<{ left: number; right: number } | false>;\n\t\tnext(): void;\n\t}\n\tclass EDA {\n\t\tdmt_EditorControl: DMT_EditorControl;\n\t}\n}\n"
	a, b := mustParse(t, multi), mustParse(t, single)
	if !reflect.DeepEqual(a, b) || len(a) != 2 {
		t.Fatalf("multi = %+v\nsingle = %+v", a, b)
	}
	if r := Diff(Snapshot{Methods: b, HasSignatures: true}, Snapshot{Methods: a, HasSignatures: true}, Usage{}); len(r.Changes) != 0 {
		t.Fatalf("formatting-only change reported: %+v", r.Changes)
	}
}

func TestDiffCosmeticSignature(t *testing.T) {
	mk := func(sig string) Snapshot {
		return Snapshot{HasSignatures: true, Methods: []Method{{NS: "eda.pcb_Drc", Method: "getNetRules", Sig: sig}}}
	}
	u := Usage{Methods: []string{"eda.pcb_Drc.getNetRules"}}
	r := Diff(mk("getNetRules(): Promise<Array<{ [key: string]: any }>>;"), mk("getNetRules(): Promise<Record<string, any>[]>;"), u)
	if r.Counts.Cosmetic != 1 || r.Counts.Signature != 0 || len(r.BreakingForUs) != 0 || r.Changes[0].Kind != "signature-cosmetic" {
		t.Fatalf("cosmetic = %+v", r)
	}
	r = Diff(mk("delete(a: string): Promise<boolean>;"), mk("delete(a: string): Promise<boolean | undefined>;"), Usage{Methods: []string{"eda.pcb_Drc.getNetRules"}})
	if r.Counts.Signature != 1 || len(r.BreakingForUs) != 1 {
		t.Fatalf("real change = %+v", r.Counts)
	}
}
