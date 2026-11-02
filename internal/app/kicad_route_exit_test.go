package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// routeExitRun is runKicadRoute under the process's exit handling, with the
// routing steps replaced by body; waivers are a --waivers file. It returns the
// exit code, the summary.json as written and stderr.
func routeExitRun(t *testing.T, waivers string, body func(*kicadRun) error) (int, map[string]GateRecord, bool, string) {
	t.Helper()
	dir := t.TempDir()
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"GND":{"role":"ground","widthMil":{"outer":16,"inner":16,"min":10},"clearanceMil":8}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "waivers.json")
	if err := os.WriteFile(wf, []byte(waivers), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "route")
	o := kicadRouteOpts{pcb: filepath.Join(dir, "x.kicad_pcb"), intent: intent, outDir: out, requirements: []string{intent}, waivers: wf, silk: defaultSilkTightOpts(), testRunBody: body}
	var stderr bytes.Buffer
	root := &cobra.Command{Use: "route", SilenceUsage: true, SilenceErrors: true, RunE: func(*cobra.Command, []string) error {
		return runKicadRoute(o, io.Discard, &stderr)
	}}
	root.SetArgs(nil)
	code := execute(root, &stderr)
	data, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum struct {
		Pass  bool         `json:"pass"`
		Gates []GateRecord `json:"gates"`
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		t.Fatal(err)
	}
	gates := map[string]GateRecord{}
	for _, g := range sum.Gates {
		gates[g.ID] = g
	}
	return code, gates, sum.Pass, stderr.String()
}

// The command around the run, from gates to the process's exit: code, summary and
// stderr agree for a run that passes, one that could not check something, and one
// that failed. The gates are the ones the production functions make.
func TestKicadRouteExitCodes(t *testing.T) {
	const reason = "deferred: routing incomplete (1 unrouted connection(s))"
	prov := func(routeVia) string { return viaFromRouter }
	spanless := auditVia("span", "SIG", 600, 300)
	spanless.From, spanless.To = 0, 0
	audit := func(vias ...routeVia) gateResult {
		g := gateResult{Gate: "via-audit"}
		viaAuditVerdict(&g, auditVias(vias, auditRB(), viaLimits{annular: 130000}, prov, viaPolicy{}))
		return g
	}
	okVia := auditVia("ok", "SIG", 600, 300)
	ring := auditVia("ring", "GND", 600, 400)
	drc := kicadDRCGate(&kicad.DRCReport{}, "drc.json")
	clean := func(r *kicadRun) {
		r.add(drc)
		r.add(audit(okVia))
		r.addTailGate(gateResult{Gate: "silkscreen", Pass: true, Detail: "labels clean"})
	}
	routerVias := func(f *fakeViaTools) gateResult {
		_, gates := sizeRouterVias(f, "classed", "imported", func(s string) string { return s }, map[string]any{}, io.Discard)
		if len(gates) != 1 {
			t.Fatalf("%+v", gates)
		}
		return gates[0]
	}
	noClass := &fakeViaTools{after: `{"copper":{"vias":[{"primitiveId":"v1","net":"NOCLASS","diameter":31.4961,"holeDiameter":15.748}]}}`}

	type want struct {
		code   int
		pass   bool
		status map[string]gate.Status
		stderr []string
	}
	for _, c := range []struct {
		name    string
		waivers string
		body    func(*kicadRun) error
		want    want
	}{
		{"pass", "[]", func(r *kicadRun) error { clean(r); return nil },
			want{0, true, map[string]gate.Status{"kicad-drc": gate.Pass, "via-audit": gate.Pass, "silkscreen": gate.Pass}, []string{"PASS"}}},
		{"via-audit cannot check", "[]", func(r *kicadRun) error {
			r.add(drc)
			r.add(audit(spanless))
			return nil
		}, want{5, false, map[string]gate.Status{"kicad-drc": gate.Pass, "via-audit": gate.CannotCheck}, []string{"CANNOT-CHECK"}}},
		{"kicad-cli did not run", "[]", func(r *kicadRun) error {
			r.add(kicadDRCCannotCheck(errors.New("timed out")))
			return nil
		}, want{5, false, map[string]gate.Status{"kicad-drc": gate.CannotCheck}, []string{"CANNOT-CHECK", "timed out"}}},
		{"router-vias through the adapter", "[]", func(r *kicadRun) error {
			r.reg.Add(toRecord(routerVias(noClass), nil, "board", 0))
			return nil
		}, want{5, false, map[string]gate.Status{"router-vias": gate.CannotCheck}, []string{"CANNOT-CHECK"}}},
		{"silk deferred on clean labels", "[]", func(r *kicadRun) error {
			r.silkAutoDeferred = reason
			clean(r)
			return nil
		}, want{5, false, map[string]gate.Status{"silkscreen": gate.CannotCheck}, []string{"CANNOT-CHECK", "routing incomplete"}}},
		{"mixed across gates", "[]", func(r *kicadRun) error {
			r.add(audit(spanless))
			r.add(gateResult{Gate: "route-complete", Items: []string{"1 connection(s) unrouted"}, Detail: "1 unrouted"})
			return nil
		}, want{6, false, map[string]gate.Status{"via-audit": gate.CannotCheck, "route-complete": gate.Fail}, []string{"CANNOT-CHECK", "FAIL"}}},
		{"mixed in one gate", "[]", func(r *kicadRun) error {
			r.add(audit(spanless, ring))
			return nil
		}, want{6, false, map[string]gate.Status{"via-audit": gate.Fail}, []string{"FAIL"}}},
		{"mixed, the failure waived", `[{"gate":"via-audit","match":"annular ring","reason":"reviewed","by":"test"},{"gate":"via-audit","match":"differs from class","reason":"reviewed","by":"test"}]`, func(r *kicadRun) error {
			r.add(audit(spanless, ring))
			return nil
		}, want{5, false, map[string]gate.Status{"via-audit": gate.CannotCheck}, []string{"CANNOT-CHECK", "waived"}}},
		{"everything waived", `[{"gate":"via-audit","match":" via ","reason":"reviewed","by":"test"}]`, func(r *kicadRun) error {
			r.add(audit(spanless, ring))
			return nil
		}, want{0, true, map[string]gate.Status{"via-audit": gate.Pass}, []string{"PASS"}}},
		{"no gate at all", "[]", func(r *kicadRun) error { return nil }, want{5, false, nil, nil}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, gates, pass, stderr := routeExitRun(t, c.waivers, c.body)
			if code != c.want.code || pass != c.want.pass {
				t.Fatalf("exit %d pass %v, want %d %v\n%s", code, pass, c.want.code, c.want.pass, stderr)
			}
			for id, st := range c.want.status {
				if g, ok := gates[id]; !ok || g.Status != st || g.Pass != (st == gate.Pass) {
					t.Errorf("%s: %+v, want %s", id, g, st)
				}
			}
			for _, s := range c.want.stderr {
				if !strings.Contains(stderr, s) {
					t.Errorf("no %q in stderr:\n%s", s, stderr)
				}
			}
		})
	}
	// A tool error preempts the verdict: exit 1, printed, recorded.
	code, _, pass, stderr := routeExitRun(t, "[]", func(r *kicadRun) error {
		clean(r)
		return errors.New("bridge exploded")
	})
	if code != 1 || pass || !strings.Contains(stderr, "bridge exploded") {
		t.Fatalf("tool error: exit %d pass %v\n%s", code, pass, stderr)
	}
}
