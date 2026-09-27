package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

const esp32IntentFixture = "testdata/intent/esp32-mini.intent.json"

// hostConfigFixture is the live Web 3.2.203 pcb.config.get capture the
// connector's own planner is tested against.
func hostConfigFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../extension/src/testdata/pcb-config-web-3.2.203.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// cleanHostConfig drops the capture's PWR_Class so the ESP32 intent starts
// from a board with no classes; netRules keep one top-level net row per net.
func cleanHostConfig(t *testing.T, nets []string) map[string]any {
	cfg := hostConfigFixture(t)
	cfg["classes"] = []any{}
	var rows []any
	for _, n := range nets {
		rows = append(rows, fakeNetRuleRow(n, "net"))
	}
	cfg["netRules"] = rows
	return cfg
}

func fakeNetRuleRow(name, typ string) map[string]any {
	return map[string]any{
		"Blind/Buried Via": "default", "Copper Safe Spacing": nil, "Copper Zone": "default", "Creepage Distance": nil,
		"Net Length Range": "default", "Net Length Tolerance": nil, "Paste Mask Expansion": "default", "Plane Safe Spacing": nil,
		"Plane Zone": "default", "Safe Spacing": "default", "Solder Mask Expansion": "default", "Track": "default", "Via Size": "default",
		"name": name, "targetNet": nil, "type": typ,
	}
}

var esp32PcbNets = []string{"5V_TERM", "VBUS", "+5V", "+3V3", "SW", "GND", "USB_DP", "USB_DM", "TXD0", "RXD0", "EN", "IO0", "LED"}

// fakeRulesHost simulates the typed actions pcb rules uses, with the host's
// observable semantics (class creation adds a netRules class entry, etc.).
type fakeRulesHost struct {
	mu      sync.Mutex
	nets    []string
	cfg     map[string]any
	pairs   []any
	writes  []string
	actions []string
	// failure modes
	rulesUnverified bool // rules.set reports verified:false
	dropRules       bool // rules.set claims verified but stores nothing
	noClassEntry    bool // class create does not add a netRules entry
	roundoff        bool // stored rule numbers shift by one ulp
}

func newFakeRulesHost(t *testing.T) *fakeRulesHost {
	return &fakeRulesHost{nets: esp32PcbNets, cfg: cleanHostConfig(t, esp32PcbNets), pairs: []any{}}
}

func (h *fakeRulesHost) handle(action string, payload map[string]any) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actions = append(h.actions, action)
	switch action {
	case "pcb.nets.list":
		var nets []any
		for _, n := range h.nets {
			nets = append(nets, map[string]any{"net": n, "length": 0})
		}
		return map[string]any{"nets": nets, "count": len(nets)}, nil
	case "pcb.config.get":
		return jsonClone(h.cfg).(map[string]any), nil
	case "pcb.constraint.list":
		return map[string]any{"differentialPairs": jsonClone(h.pairs), "equalLengthGroups": []any{}}, nil
	case "pcb.net_class.create", "pcb.net_class.add_nets":
		h.writes = append(h.writes, action)
		name := asString(payload["name"])
		nets := asStrSlice(payload["nets"])
		classes := h.cfg["classes"].([]any)
		var cls map[string]any
		for _, c := range classes {
			if m := c.(map[string]any); m["name"] == name {
				cls = m
			}
		}
		if action == "pcb.net_class.create" {
			if cls != nil {
				return nil, fmt.Errorf("exists")
			}
			cls = map[string]any{"name": name, "nets": []any{}, "color": nil}
			h.cfg["classes"] = append(classes, cls)
			if !h.noClassEntry {
				entry := fakeNetRuleRow(name, "netClass")
				entry["sub"] = []any{}
				h.cfg["netRules"] = append(h.cfg["netRules"].([]any), entry)
			}
		} else if cls == nil {
			return nil, fmt.Errorf("missing class")
		}
		for _, n := range nets {
			cls["nets"] = append(cls["nets"].([]any), n)
			var keep []any
			for _, r := range h.cfg["netRules"].([]any) {
				m := r.(map[string]any)
				if m["type"] == "net" && m["name"] == n {
					continue
				}
				if m["type"] == "netClass" && m["name"] == name {
					m["sub"] = append(m["sub"].([]any), fakeNetRuleRow(n, "net"))
				}
				keep = append(keep, m)
			}
			h.cfg["netRules"] = keep
		}
		return map[string]any{"verified": true, "netClass": cls}, nil
	case "pcb.drc.rules.set":
		h.writes = append(h.writes, action)
		if h.rulesUnverified {
			return map[string]any{"verified": false, "partial": true, "rolledBack": true}, nil
		}
		if !h.dropRules {
			rc := jsonClone(payload["ruleConfiguration"]).(map[string]any)
			if h.roundoff {
				bumpFloats(rc)
			}
			h.cfg["ruleConfiguration"] = rc
			if nr, ok := payload["netRules"]; ok {
				h.cfg["netRules"] = jsonClone(nr)
			}
		}
		return map[string]any{"verified": true, "rulesWritten": true}, nil
	case "pcb.differential_pair.create":
		h.writes = append(h.writes, action)
		h.pairs = append(h.pairs, map[string]any{"name": payload["name"], "positiveNet": payload["positiveNet"], "negativeNet": payload["negativeNet"]})
		return map[string]any{"verified": true, "created": true}, nil
	}
	return nil, fmt.Errorf("unexpected action %s", action)
}

func bumpFloats(v any) {
	switch m := v.(type) {
	case map[string]any:
		for k, x := range m {
			if f, ok := x.(float64); ok && f != 0 {
				m[k] = math.Nextafter(f, math.Inf(1))
			} else {
				bumpFloats(x)
			}
		}
	case []any:
		for i, x := range m {
			if f, ok := x.(float64); ok && f != 0 {
				m[i] = math.Nextafter(f, math.Inf(1))
			} else {
				bumpFloats(x)
			}
		}
	}
}

func (h *fakeRulesHost) serve(t *testing.T) (*appConfig, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`)
		case "/action":
			var body struct {
				Action  string         `json:"action"`
				Payload map[string]any `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res, err := h.handle(body.Action, body.Payload)
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": "PRECONDITION_REFUSED", "message": err.Error()}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
		default:
			http.NotFound(w, r)
		}
	}))
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	return &appConfig{host: host, ports: port + "-" + port}, srv.Close
}

func runRulesCLI(t *testing.T, h *fakeRulesHost, args ...string) (intentRulesReport, error, string) {
	t.Helper()
	cfg, stop := h.serve(t)
	defer stop()
	var stdout, stderr bytes.Buffer
	cmd := newPcbCmd(cfg, &stdout, &stderr)
	cmd.SetOut(&stderr)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"rules"}, args...))
	err := cmd.Execute()
	var rep intentRulesReport
	if jerr := json.Unmarshal(stdout.Bytes(), &rep); jerr != nil {
		t.Fatalf("stdout is not a report: %v\n%s\nstderr=%s", jerr, stdout.String(), stderr.String())
	}
	return rep, err, stderr.String()
}

func mustIntent(t *testing.T) *designIntent {
	t.Helper()
	in, err := loadDesignIntent(esp32IntentFixture)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func snapshotOf(t *testing.T, cfg map[string]any) intentRulesSnapshot {
	var s intentRulesSnapshot
	b, _ := json.Marshal(cfg)
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func netSet(nets []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range nets {
		m[n] = true
	}
	return m
}

func closeTo(t *testing.T, what string, got any, want float64) {
	t.Helper()
	f, ok := got.(float64)
	if !ok || math.Abs(f-want) > 1e-12 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestIntentRulesPlanESP32FromCleanBoard(t *testing.T) {
	in := mustIntent(t)
	p := planIntentRules(in, snapshotOf(t, cleanHostConfig(t, esp32PcbNets)), netSet(esp32PcbNets), nil)
	if len(p.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	creates := map[string]bool{}
	for _, c := range p.classOps() {
		if c.Action != "create" {
			t.Fatalf("class %s action %s", c.Name, c.Action)
		}
		creates[c.Name] = true
	}
	for _, want := range []string{"POWER", "SWITCH", "GND", "USB"} {
		if !creates[want] {
			t.Fatalf("missing create for %s: %+v", want, p.Classes)
		}
	}
	if len(p.PendingBindings) != 4 {
		t.Fatalf("pendingBindings=%v", p.PendingBindings)
	}
	rc := p.ruleConfiguration
	pw := mnav(rc, "Physics", "Track", "PP_POWER").(map[string]any)
	if pw["isSetDefault"] != false || pw["editName"] != "PP_POWER" {
		t.Fatalf("PP_POWER not a non-default copy: %v", pw)
	}
	l1 := mnav(pw, "form", "data", "1").(map[string]any)
	closeTo(t, "POWER default", l1["defaultValue"], 20*0.0254)
	closeTo(t, "POWER min (smallest member min = +3V3 12 mil)", l1["minValue"], 12*0.0254)
	closeTo(t, "POWER max kept", l1["maxValue"], 2.54)
	sp := mnav(rc, "Spacing", "Safe Spacing", "PP_SWITCH", "tables", "1", "content").([]any)
	closeTo(t, "SWITCH Track/Track", sp[0].([]any)[0], 10*0.0254)
	closeTo(t, "SWITCH Copper zone/Track", sp[7].([]any)[0], 0.254)                // default 0.254 ≥ 10 mil kept
	closeTo(t, "SWITCH Board Outline/Track untouched", sp[11].([]any)[0], 0.29972) // non-copper pair
	via := mnav(rc, "Physics", "Via Size", "PP_POWER", "form").(map[string]any)
	closeTo(t, "via outer", via["viaOuterdiameterDefault"], 24*0.0254)
	closeTo(t, "via hole", via["viaInnerdiameterDefault"], 12*0.0254)
	if mnav(rc, "Physics", "Via Size", "PP_SWITCH") != nil {
		t.Fatal("SWITCH has no via intent, no via rule expected")
	}
	// default rules untouched
	def := hostConfigFixture(t)["ruleConfiguration"]
	if !jsonEqualTol(mnav(rc, "Physics", "Track", "copperThickness1oz"), mnav(def, "Physics", "Track", "copperThickness1oz")) {
		t.Fatal("default track rule modified")
	}
	if len(p.DiffPairs) != 1 || p.DiffPairs[0].Positive != "USB_DP" || p.DiffPairs[0].Negative != "USB_DM" || p.DiffPairs[0].Action != "create" {
		t.Fatalf("diff pairs %+v", p.DiffPairs)
	}
	dp := mnav(rc, "Physics", "Differential Pair", "differentialPair", "form").(map[string]any)
	closeTo(t, "diff width", mnav(dp, "strokeWidthTables", "data", "1", "defaultValue"), 8*0.0254)
	closeTo(t, "diff gap", mnav(dp, "diffPairSpacingTables", "data", "1", "defaultValue"), 6*0.0254)
	closeTo(t, "diff gap min lowered to default", mnav(dp, "diffPairSpacingTables", "data", "1", "minValue"), 6*0.0254)
	if len(p.Unsupported) != 0 {
		t.Fatalf("ESP32 has no domain pairs: %+v", p.Unsupported)
	}
	if !strings.Contains(fmt.Sprint(p.Advisories), "viasPerTransition=2") || !strings.Contains(fmt.Sprint(p.Advisories), "90 Ω") {
		t.Fatalf("advisories: %+v", p.Advisories)
	}
}

func TestPcbRulesApplyIsIdempotentAndVerified(t *testing.T) {
	h := newFakeRulesHost(t)
	rep, err, stderr := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture)
	if err != nil || !rep.Verified || rep.Status != "applied" {
		t.Fatalf("apply err=%v status=%s stderr=%s", err, rep.Status, stderr)
	}
	want := []string{"pcb.net_class.create", "pcb.net_class.create", "pcb.net_class.create", "pcb.net_class.create", "pcb.drc.rules.set", "pcb.differential_pair.create"}
	if strings.Join(h.writes, ",") != strings.Join(want, ",") {
		t.Fatalf("writes=%v", h.writes)
	}
	if !strings.Contains(stderr, "bug #258") {
		t.Fatal("rules write must surface the known host warning")
	}
	// every member row is bound to its class rules
	for _, r := range h.cfg["netRules"].([]any) {
		m := r.(map[string]any)
		if m["type"] == "netClass" && m["name"] == "POWER" {
			if m["Track"] != "PP_POWER" || m["Safe Spacing"] != "PP_POWER" || m["Via Size"] != "PP_POWER" {
				t.Fatalf("POWER entry %v", m)
			}
			for _, s := range m["sub"].([]any) {
				if s.(map[string]any)["Track"] != "PP_POWER" {
					t.Fatalf("member %v", s)
				}
			}
		}
		if m["type"] == "netClass" && m["name"] == "SWITCH" && m["Via Size"] != "default" {
			t.Fatalf("SWITCH has no via rule: %v", m)
		}
	}
	h.writes = nil
	rep, err, _ = runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture)
	if err != nil || rep.Status != "in-sync" || len(h.writes) != 0 {
		t.Fatalf("replay err=%v status=%s writes=%v", err, rep.Status, h.writes)
	}
	rep, err, _ = runRulesCLI(t, h, "check", "--intent", esp32IntentFixture)
	if err != nil || !rep.Verified || rep.Status != "in-sync" {
		t.Fatalf("check after apply err=%v rep=%+v", err, rep)
	}
}

func TestPcbRulesHostRoundoffDoesNotChurn(t *testing.T) {
	h := newFakeRulesHost(t)
	h.roundoff = true
	if rep, err, _ := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture); err != nil || !rep.Verified {
		t.Fatalf("apply with roundoff err=%v rep=%+v", err, rep.Final)
	}
	h.writes = nil
	if rep, err, _ := runRulesCLI(t, h, "check", "--intent", esp32IntentFixture); err != nil || rep.Plan.PendingWrites != 0 {
		t.Fatalf("roundoff caused drift: %v %+v", err, rep.Plan.RuleChanges)
	}
}

func TestPcbRulesDryRunAndCheckWriteNothing(t *testing.T) {
	h := newFakeRulesHost(t)
	rep, err, _ := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture, "--dry-run")
	if err != nil || rep.Mode != "dry-run" || rep.Status != "planned" || rep.Plan.PendingWrites == 0 {
		t.Fatalf("dry-run err=%v rep=%+v", err, rep)
	}
	if len(h.writes) != 0 {
		t.Fatalf("dry-run wrote %v", h.writes)
	}
	if len(rep.Plan.RuleChanges) == 0 || rep.IntentSHA == "" {
		t.Fatalf("dry-run must show rule changes and provenance: %+v", rep.Plan)
	}
	rep, err, _ = runRulesCLI(t, h, "check", "--intent", esp32IntentFixture)
	if err == nil || rep.Status != "drift" || len(h.writes) != 0 {
		t.Fatalf("check on drifted board must fail read-only: err=%v status=%s writes=%v", err, rep.Status, h.writes)
	}
}

func TestPcbRulesUnverifiedWritesExitNonZero(t *testing.T) {
	for _, mode := range []string{"rulesUnverified", "dropRules", "noClassEntry"} {
		t.Run(mode, func(t *testing.T) {
			h := newFakeRulesHost(t)
			switch mode {
			case "rulesUnverified":
				h.rulesUnverified = true
			case "dropRules":
				h.dropRules = true
			case "noClassEntry":
				h.noClassEntry = true
			}
			rep, err, _ := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture)
			if err == nil || rep.Verified || rep.Status != "unverified" {
				t.Fatalf("mode %s must fail: err=%v status=%s", mode, err, rep.Status)
			}
		})
	}
}

func TestPcbRulesConflictWritesNothing(t *testing.T) {
	h := newFakeRulesHost(t)
	h.cfg = hostConfigFixture(t) // live PWR_Class already owns +5V,+3V3,GND
	rep, err, _ := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture)
	if err == nil || rep.Status != "conflict" || len(h.writes) != 0 {
		t.Fatalf("err=%v status=%s writes=%v", err, rep.Status, h.writes)
	}
	if !strings.Contains(fmt.Sprint(rep.Plan.Conflicts), "PWR_Class") {
		t.Fatalf("conflicts %+v", rep.Plan.Conflicts)
	}
}

func TestIntentRulesAddMissingMembersPreservesExtras(t *testing.T) {
	in := mustIntent(t)
	cfg := cleanHostConfig(t, esp32PcbNets)
	cfg["classes"] = []any{map[string]any{"name": "POWER", "nets": []any{"+5V", "LED"}}}
	entry := fakeNetRuleRow("POWER", "netClass")
	entry["sub"] = []any{fakeNetRuleRow("+5V", "net"), fakeNetRuleRow("LED", "net")}
	cfg["netRules"] = append(cfg["netRules"].([]any), entry)
	p := planIntentRules(in, snapshotOf(t, cfg), netSet(esp32PcbNets), nil)
	var power intentClassPlan
	for _, c := range p.Classes {
		if c.Name == "POWER" {
			power = c
		}
	}
	if power.Action != "add_nets" || strings.Join(power.Add, ",") != "+3V3,5V_TERM,VBUS" || strings.Join(power.ExtraLive, ",") != "LED" {
		t.Fatalf("power plan %+v", power)
	}
	// existing members bound now; new ones pending until add_nets lands
	if len(p.Bindings) == 0 || !strings.Contains(strings.Join(p.PendingBindings, ";"), "bind new members") {
		t.Fatalf("bindings=%v pending=%v", p.Bindings, p.PendingBindings)
	}
}

func TestIntentRulesNetsMissingOnPcbAndDomainPairs(t *testing.T) {
	in := mustIntent(t)
	in.Pairs = []intentPair{{A: "domain:MAINS", B: "domain:SELV_5V", ClearanceMm: 5.5, CreepageMm: 8, SlotRequired: true}}
	pcb := netSet(esp32PcbNets)
	delete(pcb, "5V_TERM")
	delete(pcb, "USB_DM")
	p := planIntentRules(in, snapshotOf(t, cleanHostConfig(t, esp32PcbNets)), pcb, nil)
	if len(p.Unsupported) != 1 || p.Unsupported[0].Status != "planned" || !strings.Contains(p.Unsupported[0].Detail, "creepage ≥ 8.00 mm") {
		t.Fatalf("unsupported %+v", p.Unsupported)
	}
	for _, c := range p.Classes {
		if c.Name == "POWER" && strings.Join(c.MissingOnPcb, ",") != "5V_TERM" {
			t.Fatalf("POWER %+v", c)
		}
	}
	if p.DiffPairs[0].Action != "skip" {
		t.Fatalf("pair with missing net must be skipped: %+v", p.DiffPairs)
	}
}

func TestPcbRulesCheckStrictFailsOnUnsupported(t *testing.T) {
	h := newFakeRulesHost(t)
	if _, err, _ := runRulesCLI(t, h, "apply", "--intent", esp32IntentFixture); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(esp32IntentFixture)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["pairs"] = []any{map[string]any{"a": "domain:MAINS", "b": "domain:SELV_5V", "clearanceMm": 5.5}}
	path := t.TempDir() + "/intent.json"
	b, _ := json.Marshal(m)
	_ = os.WriteFile(path, b, 0o644)
	if _, err, _ := runRulesCLI(t, h, "check", "--intent", path); err != nil {
		t.Fatalf("non-strict check must pass: %v", err)
	}
	if rep, err, _ := runRulesCLI(t, h, "check", "--intent", path, "--strict"); err == nil || rep.Status != "unsupported-items" {
		t.Fatalf("strict: err=%v status=%s", err, rep.Status)
	}
}

func TestLiveDiffPairStates(t *testing.T) {
	in := mustIntent(t)
	snap := snapshotOf(t, cleanHostConfig(t, esp32PcbNets))
	nets := netSet(esp32PcbNets)
	for _, tc := range []struct {
		live   []intentLiveDiffPair
		action string
		clash  bool
	}{
		{[]intentLiveDiffPair{{"USB_D", "USB_DP", "USB_DM"}}, "ok", false},
		{[]intentLiveDiffPair{{"USB0", "USB_DP", "USB_DM"}}, "ok", false},
		{[]intentLiveDiffPair{{"USB_D", "TXD0", "RXD0"}}, "conflict", true},
	} {
		p := planIntentRules(in, snap, nets, tc.live)
		if p.DiffPairs[0].Action != tc.action || (len(p.Conflicts) > 0) != tc.clash {
			t.Fatalf("live=%v got %+v conflicts=%v", tc.live, p.DiffPairs, p.Conflicts)
		}
	}
	if diffPolarity("USB_DM") != -1 || diffPolarity("USB_DP") != 1 || diffPolarity("CAN_H") != 1 || diffPolarity("CAN_L") != -1 {
		t.Fatal("polarity")
	}
}

func TestDesignIntentValidation(t *testing.T) {
	for _, bad := range []string{
		`{}`,
		`{"nets":{"A":{"role":"bogus"}}}`,
		`{"nets":{"A":{"role":"power","widthMil":{"outer":-1}}}}`,
		`{"netClasses":[{"name":"P","viaDrillMil":24,"viaDiaMil":12}]}`,
		`{"netClasses":[{"name":"__proto__"}]}`,
		`{"netClasses":[{"name":"P"},{"name":"P"}]}`,
	} {
		if _, err := parseDesignIntent([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	in, err := parseDesignIntent([]byte(`{"nets":{"A":{"role":"power","netClass":"X"},"B":{"role":"power","netClass":"Y"}},"netClasses":[{"name":"X","nets":["B"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, conflicts := buildIntentClassSpecs(in); len(conflicts) != 1 || !strings.Contains(conflicts[0].Item, "B") {
		t.Fatalf("B in two classes must conflict: %+v", conflicts)
	}
}

// pcb auto run reads the board rules via parsePcbRules (default rule names);
// the intent rules are additive PP_* rules, so the router's baseline must not
// move when they are applied.
func TestIntentRulesLeavePcbAutoBaselineUnchanged(t *testing.T) {
	cfg := cleanHostConfig(t, esp32PcbNets)
	p := planIntentRules(mustIntent(t), snapshotOf(t, cfg), netSet(esp32PcbNets), nil)
	before := parsePcbRules(map[string]any{"rules": map[string]any{"config": cfg["ruleConfiguration"]}})
	after := parsePcbRules(map[string]any{"rules": map[string]any{"config": p.ruleConfiguration}})
	if before != after || before.source != "live" {
		t.Fatalf("pcb auto baseline moved: %+v → %+v", before, after)
	}
}
