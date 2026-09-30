package console

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

func newTestConsole(t *testing.T) (*Console, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PCBPILOT_HOME", home)
	t.Setenv("PCBPILOT_WORKFLOW_DIR", t.TempDir())
	audit := t.TempDir()
	c, err := New(Options{Home: home, UserHome: t.TempDir(), Version: "0.7.0-test", Host: "127.0.0.1", AuditDir: audit,
		Health: func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"service":"pcbpilot","version":"0.7.0-test","windows":[{"windowId":"w-123456789","connectorVersion":"0.7.0","easyedaVersion":"3.2.149","context":{"projectUuid":"p1","projectName":"ceshi"}}],"updates":{"latest":"0.7.1"}}`), nil
		},
		Daemon: func() DaemonInfo { return DaemonInfo{PID: 42, Port: 61832, StartedAt: time.Now().Add(-time.Minute)} },
	})
	if err != nil {
		t.Fatal(err)
	}
	c.heartbeat = 50 * time.Millisecond
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(func() { c.Stop(); srv.Close() })
	return c, srv
}

func req(t *testing.T, srv *httptest.Server, method, path, token string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(method, srv.URL+path, body)
	if token != "" {
		r.Header.Set(TokenHeader, token)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if path == "/api/events" {
		return resp
	}
	// Drain and close so no handler stays blocked on an unread body (which
	// would hang httptest.Server.Close).
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestAuthRejects(t *testing.T) {
	c, srv := newTestConsole(t)
	if resp := req(t, srv, "GET", "/api/status", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("missing token: %d", resp.StatusCode)
	}
	if resp := req(t, srv, "GET", "/api/status", "wrong", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
	if resp := req(t, srv, "GET", "/api/status", c.Token(), nil, map[string]string{"Origin": "http://evil.example"}); resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if resp := req(t, srv, "GET", "/api/status", c.Token(), nil, map[string]string{"Origin": "null"}); resp.StatusCode != 403 {
		t.Fatalf("null origin: %d", resp.StatusCode)
	}
	// DNS rebinding: a foreign Host header.
	r, _ := http.NewRequest("GET", srv.URL+"/api/status", nil)
	r.Host = "evil.example:61832"
	r.Header.Set(TokenHeader, c.Token())
	resp, _ := http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign host: %d", resp.StatusCode)
	}
	resp = req(t, srv, "GET", "/api/status", c.Token(), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("valid token: %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no CORS headers allowed")
	}
	var st map[string]any
	decode(t, resp, &st)
	if st["updates"] == nil || st["components"] == nil {
		t.Fatalf("status must pass through updates and list components: %v", st)
	}
	// Cookie session: GET ok, POST without CSRF header refused.
	resp = req(t, srv, "POST", "/api/session", c.Token(), nil, nil)
	var ck *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == CookieName {
			ck = k
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", ck)
	}
	r, _ = http.NewRequest("POST", srv.URL+"/api/ask", strings.NewReader(`{"question":"x","options":[{"id":"a"}]}`))
	r.AddCookie(ck)
	if resp, _ := http.DefaultClient.Do(r); resp.Body.Close() != nil || resp.StatusCode != 401 {
		t.Fatalf("cookie POST without CSRF header: %d", resp.StatusCode)
	}
	r, _ = http.NewRequest("POST", srv.URL+"/api/ask", strings.NewReader(`{"question":"x","options":[{"id":"a"}]}`))
	r.AddCookie(ck)
	r.Header.Set(CSRFHeader, "1")
	if resp, _ := http.DefaultClient.Do(r); resp.Body.Close() != nil || resp.StatusCode != 200 {
		t.Fatalf("cookie POST with CSRF header: %d", resp.StatusCode)
	}
	// Static UI needs no token but still the loopback host.
	if resp := req(t, srv, "GET", "/ui/", "", nil, nil); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("ui: %d %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestSSEStreamsActivityAndHello(t *testing.T) {
	c, srv := newTestConsole(t)
	r, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	r.Header.Set(TokenHeader, c.Token())
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	go func() {
		time.Sleep(100 * time.Millisecond)
		c.Observe(daemon.ActionEvent{Timestamp: time.Now(), Action: "pcb.save", OK: true, ProjectUUID: "p1", ProjectName: "ceshi"})
	}()
	for time.Now().Before(deadline) && !(seen["hello"] && seen["activity"] && seen["heartbeat"]) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if ev, ok := strings.CutPrefix(strings.TrimSpace(line), "event: "); ok {
			seen[ev] = true
		}
	}
	if !seen["hello"] || !seen["activity"] || !seen["heartbeat"] {
		t.Fatalf("events seen: %v", seen)
	}
	// The live event also reached the project registry.
	var pr struct {
		Projects []ProjectView `json:"projects"`
	}
	decode(t, req(t, srv, "GET", "/api/projects", c.Token(), nil, nil), &pr)
	if len(pr.Projects) != 1 || pr.Projects[0].Name != "ceshi" || pr.Projects[0].Status != "running" || pr.Projects[0].Connected != 1 {
		t.Fatalf("projects: %+v", pr.Projects)
	}
}

func TestAskQueueFlow(t *testing.T) {
	c, srv := newTestConsole(t)
	var q Question
	decode(t, req(t, srv, "POST", "/api/ask", c.Token(), strings.NewReader(`{"question":"进入布线？","options":[{"id":"ok","label":"确认"},{"id":"adjust","label":"调整"}],"timeoutSec":30}`), nil), &q)
	if q.ID == "" || q.Status != "pending" {
		t.Fatalf("create: %+v", q)
	}
	done := make(chan Question, 1)
	go func() {
		var got Question
		decode(t, req(t, srv, "GET", "/api/ask/"+q.ID+"?wait=5", c.Token(), nil, nil), &got)
		done <- got
	}()
	time.Sleep(100 * time.Millisecond)
	if resp := req(t, srv, "POST", "/api/ask/"+q.ID+"/answer", c.Token(), strings.NewReader(`{"choice":"nope"}`), nil); resp.StatusCode != 409 {
		t.Fatalf("invalid choice must be refused: %d", resp.StatusCode)
	}
	if resp := req(t, srv, "POST", "/api/ask/"+q.ID+"/answer", c.Token(), strings.NewReader(`{"choice":"ok","note":"looks good"}`), nil); resp.StatusCode != 200 {
		t.Fatalf("answer: %d", resp.StatusCode)
	}
	select {
	case got := <-done:
		if got.Status != "answered" || got.Answer.Choice != "ok" || got.Answer.By != "console" {
			t.Fatalf("long poll result: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll did not return on answer")
	}
	// Second answer is a conflict; decision log written.
	if resp := req(t, srv, "POST", "/api/ask/"+q.ID+"/answer", c.Token(), strings.NewReader(`{"choice":"adjust"}`), nil); resp.StatusCode != 409 {
		t.Fatalf("double answer: %d", resp.StatusCode)
	}
	if b, err := os.ReadFile(filepath.Join(c.home, "console", "decisions.jsonl")); err != nil || !strings.Contains(string(b), `"choice":"ok"`) {
		t.Fatalf("decision log: %s %v", b, err)
	}
	// Bad questions are 400.
	if resp := req(t, srv, "POST", "/api/ask", c.Token(), strings.NewReader(`{"question":""}`), nil); resp.StatusCode != 400 {
		t.Fatalf("empty question: %d", resp.StatusCode)
	}
}

func TestAskExpiry(t *testing.T) {
	now := time.Now()
	q := newAskQueue("", func() time.Time { return now }, nil)
	item, err := q.create(AskRequest{Question: "x", Options: []Option{{ID: "a"}}, TimeoutSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	got, _, _ := q.get(item.ID)
	if got.Status != "expired" {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := q.answer(item.ID, "a", "", "console"); err == nil {
		t.Fatal("expired card must not accept answers")
	}
}

// copyTree copies the ESP32 example report package into a temp work dir.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkDirTimelineSimsReportsAndFiles(t *testing.T) {
	c, srv := newTestConsole(t)
	work := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "docs", "examples", "esp32-mini-design-report"), filepath.Join(work, "reports", "esp32"))
	cfg, _ := projectconfig.New("esp32", "quick-proto")
	if err := projectconfig.Save(work, cfg, "test"); err != nil {
		t.Fatal(err)
	}
	wd, err := RegisterWorkDir(c.home, work, "esp32", "test")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Timeline []StepStatus `json:"timeline"`
		Sims     []SimRun     `json:"sims"`
		Reports  []ReportPkg  `json:"reports"`
		Arts     []Artifact   `json:"artifacts"`
	}
	decode(t, req(t, srv, "GET", "/api/workdirs/"+wd.ID, c.Token(), nil, nil), &out)
	status := map[string]string{}
	for _, s := range out.Timeline {
		status[s.ID] = s.Status
	}
	if status["P11"] != "done" || status["S6.5"] != "done" || status["S5.5"] != "done" || status["P10.5"] != "done" {
		// quick-proto skips S5.5/P10.5 but the example package holds their evidence → shown done with a reason.
		t.Fatalf("timeline: %v", status)
	}
	if len(out.Reports) != 3 || out.Reports[0].Version != "v3" || out.Reports[0].HTML == "" {
		t.Fatalf("reports: %+v", out.Reports)
	}
	kinds := map[string]int{}
	for _, s := range out.Sims {
		kinds[s.Kind]++
		if s.Error != "" {
			t.Fatalf("sim parse error %s: %s", s.Path, s.Error)
		}
	}
	if kinds["sim-power"] == 0 || kinds["sim-post"] == 0 || kinds["sim-analog"] == 0 {
		t.Fatalf("sim kinds: %v", kinds)
	}
	// Dedupe: v3 has analog.json twice with identical content.
	for _, a := range out.Arts {
		if a.Kind == "sim-analog" && strings.HasPrefix(a.Path, "reports/esp32/v3/data/analog") && len(a.Aliases) == 0 {
			if _, err := os.Stat(filepath.Join(work, "reports/esp32/v3/data/analog/analog.json")); err == nil {
				t.Fatalf("identical analog.json copies must be merged: %+v", a)
			}
		}
	}
	// File serving: inside ok (sandboxed), traversal refused.
	resp := req(t, srv, "GET", "/api/workdirs/"+wd.ID+"/file?path="+out.Reports[0].HTML, c.Token(), nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("file: %d %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd"} {
		if resp := req(t, srv, "GET", "/api/workdirs/"+wd.ID+"/file?path="+bad, c.Token(), nil, nil); resp.StatusCode == 200 {
			t.Fatalf("path %q must be refused", bad)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(work, "link.txt"))
	if resp := req(t, srv, "GET", "/api/workdirs/"+wd.ID+"/file?path=link.txt", c.Token(), nil, nil); resp.StatusCode != 403 {
		t.Fatalf("symlink escape: %d", resp.StatusCode)
	}
}

func TestConfigPutValidatesAndUploadIndexes(t *testing.T) {
	c, srv := newTestConsole(t)
	work := t.TempDir()
	wd, _ := RegisterWorkDir(c.home, work, "w", "test")
	if resp := req(t, srv, "POST", "/api/workdirs/"+wd.ID+"/config/init", c.Token(), strings.NewReader(`{"template":"full"}`), nil); resp.StatusCode != 200 {
		t.Fatalf("init: %d", resp.StatusCode)
	}
	cfg, err := projectconfig.Load(work)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Steps["P10"] = projectconfig.Toggle{Enabled: false, Reason: "x"}
	b, _ := json.Marshal(cfg)
	if resp := req(t, srv, "PUT", "/api/workdirs/"+wd.ID+"/config", c.Token(), bytes.NewReader(b), nil); resp.StatusCode != 422 {
		t.Fatalf("invalid config must be 422, got %d", resp.StatusCode)
	}
	cfg.Steps["P10"] = projectconfig.Toggle{Enabled: true}
	cfg.Steps["P10.5"] = projectconfig.Toggle{Enabled: false, Reason: "打样"}
	b, _ = json.Marshal(cfg)
	if resp := req(t, srv, "PUT", "/api/workdirs/"+wd.ID+"/config", c.Token(), bytes.NewReader(b), nil); resp.StatusCode != 200 {
		t.Fatalf("valid config: %d", resp.StatusCode)
	}
	if got, _ := projectconfig.Load(work); got.StepEnabled("P10.5") || got.UpdatedBy != "console" {
		t.Fatalf("config not written: %+v", got.Steps["P10.5"])
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("kind", "requirement")
	mw.WriteField("tags", "customer")
	fw, _ := mw.CreateFormFile("file", "../../客户 需求.md")
	fw.Write([]byte("# 需求\n四层板，5V 供电端子，降压到 3V3。"))
	fw, _ = mw.CreateFormFile("file", "evil.exe")
	fw.Write([]byte("MZ"))
	mw.Close()
	resp := req(t, srv, "POST", "/api/workdirs/"+wd.ID+"/kb/upload", c.Token(), &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	var up struct {
		Added    []map[string]any    `json:"added"`
		Rejected []map[string]string `json:"rejected"`
	}
	decode(t, resp, &up)
	if len(up.Added) != 1 || len(up.Rejected) != 1 {
		t.Fatalf("upload: %+v", up)
	}
	if _, err := os.Stat(filepath.Join(work, "resources", "requirement", "客户-需求.md")); err != nil {
		t.Fatalf("stored name: %v", err)
	}
	var sr struct {
		Hits []struct{ Cite string } `json:"hits"`
	}
	decode(t, req(t, srv, "GET", "/api/workdirs/"+wd.ID+"/kb/search?q=降压", c.Token(), nil, nil), &sr)
	if len(sr.Hits) != 1 {
		t.Fatalf("search after upload: %+v", sr)
	}
}

func TestRegistryBackfillAttribution(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().UTC()
	rows := []string{
		`{"ts":"` + day.Add(-time.Hour).Format(time.RFC3339Nano) + `","windowId":"w1","clientId":"h:1","action":"project.current","ok":true,"result":{"uuid":"U1","friendlyName":"alpha"}}`,
		`{"ts":"` + day.Add(-50*time.Minute).Format(time.RFC3339Nano) + `","windowId":"w1","clientId":"h:2","action":"pcb.drc.check","ok":true,"result":{"passed":false,"violations":[1,2]}}`,
		`{"ts":"` + day.Add(-49*time.Minute).Format(time.RFC3339Nano) + `","windowId":"w1","clientId":"h:3","action":"pcb.line.create","ok":false,"errorCode":"DISPATCH_FAILED","errorMsg":"boom"}`,
		`{"ts":"` + day.Add(-48*time.Minute).Format(time.RFC3339Nano) + `","windowId":"w9","action":"system.health","ok":true}`,
	}
	os.WriteFile(filepath.Join(dir, day.Format("2006-01-02")+".jsonl"), []byte(strings.Join(rows, "\n")+"\n{\"partial"), 0o644)
	r := newRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err := r.backfill(dir, 24*time.Hour, day, nil); err != nil {
		t.Fatal(err)
	}
	var alpha *ProjectRecord
	for _, p := range r.snapshot() {
		if p.UUID == "U1" {
			cp := p
			alpha = &cp
		}
	}
	if alpha == nil || alpha.Name != "alpha" || alpha.Actions != 3 || alpha.Failures != 1 || alpha.LastDRC == nil || alpha.LastDRC.Violations != 2 || alpha.Attribution != "derived" {
		t.Fatalf("alpha: %+v", alpha)
	}
	// The 10-minute gap is not < activeGap and is excluded; the 1-minute gap counts.
	if alpha.ActiveMs != time.Minute.Milliseconds() {
		t.Fatalf("activeMs = %d", alpha.ActiveMs)
	}
	// Incremental: a second pass reads nothing new (partial line left alone).
	before := alpha.Actions
	r.backfill(dir, 24*time.Hour, day, nil)
	for _, p := range r.snapshot() {
		if p.UUID == "U1" && p.Actions != before {
			t.Fatalf("backfill re-read rows: %d → %d", before, p.Actions)
		}
	}
	// Persist and reload.
	if err := r.save(day); err != nil {
		t.Fatal(err)
	}
	r2 := newRegistry(r.path)
	if len(r2.snapshot()) != len(r.snapshot()) {
		t.Fatal("registry not persisted")
	}
}

func TestRunsAndSessions(t *testing.T) {
	c, srv := newTestConsole(t)
	for _, body := range []string{
		`{"runId":"r1","agent":"cli","kind":"start","title":"pcbpilot sim power","cwd":"/w"}`,
		`{"runId":"r1","kind":"end","status":"ok","detail":"exit 0"}`,
	} {
		if resp := req(t, srv, "POST", "/api/runs/events", c.Token(), strings.NewReader(body), nil); resp.StatusCode != 200 {
			t.Fatalf("run event: %d", resp.StatusCode)
		}
	}
	if resp := req(t, srv, "POST", "/api/runs/events", c.Token(), strings.NewReader(`{"runId":"r2","kind":"bogus"}`), nil); resp.StatusCode != 400 {
		t.Fatalf("bad kind: %d", resp.StatusCode)
	}
	now := time.Now()
	c.Observe(daemon.ActionEvent{Timestamp: now.Add(-time.Minute), ClientID: "host:10:agentA", Action: "pcb.dump", OK: true})
	c.Observe(daemon.ActionEvent{Timestamp: now, ClientID: "host:11:agentA", Action: "pcb.save", OK: true})
	var out struct {
		Runs     []Run     `json:"runs"`
		Sessions []Session `json:"sessions"`
		Bridge   map[string]string
	}
	decode(t, req(t, srv, "GET", "/api/runs", c.Token(), nil, nil), &out)
	if len(out.Runs) != 1 || out.Runs[0].Status != "ok" || out.Runs[0].Title != "pcbpilot sim power" {
		t.Fatalf("runs: %+v", out.Runs)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Commands != 2 || !out.Sessions[0].Active || out.Sessions[0].Label != "agentA" {
		t.Fatalf("sessions: %+v", out.Sessions)
	}
	if out.Bridge["status"] != "planned" {
		t.Fatal("bridge must be labelled planned")
	}
	// Finished runs survive a restart via runs.jsonl.
	rs := newRunStore(filepath.Join(c.home, "console", "runs.jsonl"), time.Now)
	if len(rs.list()) != 1 {
		t.Fatal("finished run not restored")
	}
}

func TestWorkDirStatus(t *testing.T) {
	dir := "/w"
	t0 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	pass := []ReportPkg{{Version: "v2", Verdict: "PASS with warnings", GeneratedAt: t0.Format(time.RFC3339)}}
	fail := []ReportPkg{{Version: "v1", Verdict: "FAIL", GeneratedAt: t0.Format(time.RFC3339)}}
	cases := []struct {
		name string
		arts []Artifact
		reps []ReportPkg
		runs []Run
		want string
	}{
		{"empty", nil, nil, nil, "empty"},
		{"in-progress", []Artifact{{Kind: "intent"}}, nil, nil, "in-progress"},
		{"finished", []Artifact{{Kind: "report"}}, pass, nil, "finished"},
		{"failed report", []Artifact{{Kind: "report"}}, fail, nil, "failed"},
		{"running", nil, pass, []Run{{ID: "r", Status: "running", Cwd: "/w/sub"}}, "running"},
		{"failed after report", nil, pass, []Run{{ID: "r", Status: "failed", Cwd: "/w", EndedAt: t0.Add(time.Minute)}}, "failed"},
		{"old failure", nil, pass, []Run{{ID: "r", Status: "failed", Cwd: "/w", EndedAt: t0.Add(-time.Minute)}}, "finished"},
		{"other dir", nil, pass, []Run{{ID: "r", Status: "running", Cwd: "/w2"}}, "finished"},
	}
	for _, c := range cases {
		if got, _ := workDirStatus(dir, c.arts, c.reps, c.runs); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestScanPrefersOriginalOverPackagedCopy(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"schemaVersion":1,"generator":"pcbpilot sim power","results":[]}`)
	for _, p := range []string{"reports/demo/v1/data/sim.json", "round1/sim.json"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), body, 0o644)
	}
	arts, err := scanArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Path != "round1/sim.json" || len(arts[0].Aliases) != 1 {
		t.Fatalf("arts = %+v", arts)
	}
	tl := buildTimeline(nil, arts, nil)
	for _, s := range tl {
		if s.ID == "S0" && s.Status != "implied" {
			t.Fatalf("S0 = %s, want implied", s.Status)
		}
		if s.ID == "S6.5" && s.Status != "done" {
			t.Fatalf("S6.5 = %s", s.Status)
		}
		if s.ID == "P0" && s.Status != "pending" {
			t.Fatalf("P0 after the last evidence must stay pending: %s", s.Status)
		}
	}
}
