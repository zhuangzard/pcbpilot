package app

// cmd_kicad_lcsc_health.go — `pcbpilot kicad lcsc --health` / `--alternatives`
// and the `--set @plan.json` they feed. The catalog is the one the skill's
// parts-select.py already talks to (no second HTTP client): --lookup for exact
// numbers, the --online search for candidates.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// newLcscCatalog builds the catalog client; tests replace it with a fake.
var newLcscCatalog = func(scriptPath string) (kicad.Catalog, error) {
	script, err := partsSelectAsset.resolve(scriptPath)
	if err != nil {
		return nil, err
	}
	return &scriptCatalog{script: script}, nil
}

// scriptCatalog is kicad.Catalog over parts-select.py.
type scriptCatalog struct{ script string }

func (c *scriptCatalog) run(args ...string) ([]byte, error) {
	cmd, err := pythonCommand(c.script, args...)
	if err != nil {
		return nil, err
	}
	// The catalog text carries Ω ± ℃: force UTF-8 whatever the console code page is.
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(out) > 0 {
			return out, nil // parts-select exits 1 with "[]" for a gated query: a result
		}
		return nil, fmt.Errorf("parts-select.py: %w", err)
	}
	return out, nil
}

type lookupRow struct {
	Code   string             `json:"code"`
	Found  bool               `json:"found"`
	Error  string             `json:"error"`
	Record *kicad.CatalogPart `json:"record"`
}

func (c *scriptCatalog) Lookup(codes []string) (map[string]kicad.CatalogLookup, error) {
	res := map[string]kicad.CatalogLookup{}
	for start := 0; start < len(codes); start += 30 {
		end := min(start+30, len(codes))
		out, err := c.run("--lookup", strings.Join(codes[start:end], ","))
		if err != nil {
			return res, err
		}
		var rows []lookupRow
		if err := json.Unmarshal(out, &rows); err != nil {
			return res, fmt.Errorf("%s did not answer --lookup with JSON (%v): an older installed skill? run `pcbpilot update`, or pass --parts-select <this release's parts-select.py>", c.script, err)
		}
		for _, r := range rows {
			code := strings.ToUpper(r.Code)
			switch {
			case r.Error != "":
				res[code] = kicad.CatalogLookup{Err: r.Error}
			case r.Found && r.Record != nil:
				res[code] = kicad.CatalogLookup{Part: r.Record}
			default:
				res[code] = kicad.CatalogLookup{}
			}
		}
	}
	return res, nil
}

func (c *scriptCatalog) Search(query string, qty int) ([]kicad.CatalogPart, error) {
	if qty < 1 {
		qty = 1
	}
	out, err := c.run(query, "--json", "--online", "--n", "30", "--qty", strconv.Itoa(qty))
	if err != nil {
		return nil, err
	}
	var hits []partsSelectHit
	if err := json.Unmarshal(out, &hits); err != nil {
		return nil, fmt.Errorf("parts-select.py: bad JSON: %w", err)
	}
	var parts []kicad.CatalogPart
	for _, h := range hits {
		if h.Record != nil {
			parts = append(parts, *h.Record)
		}
	}
	return parts, nil
}

// readLcscParts reads the placed parts of a design: the board, merged with
// the schematic when one is given (the same merge --check and fab use).
func readLcscParts(pcb, sch string) (*kicad.Board, []kicad.Part, []string, error) {
	if pcb == "" {
		return nil, nil, nil, fmt.Errorf("--pcb is required (placed parts come from the board)")
	}
	bd, err := kicad.ReadBoard(pcb)
	if err != nil {
		return nil, nil, nil, err
	}
	var rows []kicad.SchPart
	if sch != "" {
		cli, err := kicad.ResolveFabCLI()
		if err != nil {
			return nil, nil, nil, err
		}
		if rows, err = kicad.ReadSchematicParts(cli, sch); err != nil {
			return nil, nil, nil, err
		}
	}
	parts, warns := kicad.MergeParts(bd, rows, sch != "")
	return bd, parts, warns, nil
}

func designMPNs(bd *kicad.Board) map[string]string {
	m := map[string]string{}
	for _, f := range bd.Footprints {
		if v := kicad.LookupMPN(f.Fields); v != "" && f.Ref != "" {
			m[f.Ref] = v
		}
	}
	return m
}

// offlineCatalog is --offline for --health / --alternatives: no network, so
// every catalog question is "could not ask" and the verdict is cannot-check.
type offlineCatalog struct{}

var errOfflineCatalog = errors.New("--offline: the catalog was not queried")

func (offlineCatalog) Lookup([]string) (map[string]kicad.CatalogLookup, error) {
	return nil, errOfflineCatalog
}

func (offlineCatalog) Search(string, int) ([]kicad.CatalogPart, error) { return nil, errOfflineCatalog }

func lcscCatalogFor(scriptPath string, offline bool) (kicad.Catalog, error) {
	if offline {
		return offlineCatalog{}, nil
	}
	return newLcscCatalog(scriptPath)
}

func runLcscHealth(stdout, stderr io.Writer, pcb, sch, scriptPath string, boards int, offline bool) error {
	bd, parts, warns, err := readLcscParts(pcb, sch)
	if err != nil {
		return err
	}
	cat, err := lcscCatalogFor(scriptPath, offline)
	if err != nil {
		return err
	}
	rep := kicad.RunHealth(kicad.HealthInput{Parts: parts, DesignMPN: designMPNs(bd), Boards: boards, Catalog: cat, Warnings: warns})
	if err := writeJSON(stdout, rep); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "kicad lcsc --health: %s (%d board(s))\n", strings.ToUpper(string(rep.Verdict)), rep.Boards)
	for _, c := range rep.Checks {
		adv := ""
		if c.Advisory {
			adv = " (advisory)"
		}
		fmt.Fprintf(stderr, "  %-18s %-12s %s%s\n", c.Name, c.Status, c.Detail, adv)
	}
	if code := rep.Verdict.ExitCode(); code != 0 {
		return exitCodeError{code}
	}
	return nil
}

func runLcscAlternatives(stdout, stderr io.Writer, pcb, sch, scriptPath string, boards, limit int, offline bool) error {
	bd, parts, warns, err := readLcscParts(pcb, sch)
	if err != nil {
		return err
	}
	cat, err := lcscCatalogFor(scriptPath, offline)
	if err != nil {
		return err
	}
	rep := kicad.Alternatives(kicad.AltInput{Parts: parts, DesignMPN: designMPNs(bd), Boards: boards, Catalog: cat, Limit: limit, Warnings: warns})
	if err := writeJSON(stdout, rep); err != nil {
		return err
	}
	flagged, proposed := 0, 0
	for _, l := range rep.Lines {
		if len(l.Reasons) > 0 {
			flagged++
		}
		if l.Proposal != nil {
			proposed++
		}
	}
	fmt.Fprintf(stderr, "kicad lcsc --alternatives: %s — %d line(s) flagged, %d with a proposal, %d designator(s) in the plan\n",
		strings.ToUpper(string(rep.Verdict)), flagged, proposed, len(rep.Assignments))
	if code := rep.Verdict.ExitCode(); code != 0 {
		return exitCodeError{code}
	}
	return nil
}

// resolveSetArgs turns the --set values into one assignment map. "@file" is an
// --alternatives plan: it is loaded and merged here, and returned so the caller
// can check it against the design right before writing (verifyPlans).
func resolveSetArgs(pcb string, sets []string) (map[string]string, map[string]*kicad.Plan, error) {
	var plain, plans []string
	for _, v := range sets {
		if strings.HasPrefix(v, "@") && len(v) > 1 {
			plans = append(plans, v[1:])
		} else {
			plain = append(plain, v)
		}
	}
	assign, err := kicad.ParseAssignments(plain)
	if err != nil {
		return nil, nil, err
	}
	loaded := map[string]*kicad.Plan{}
	for _, path := range plans {
		pl, err := kicad.LoadPlan(path)
		if err != nil {
			return nil, nil, err
		}
		if pcb == "" {
			return nil, nil, fmt.Errorf("--set @%s needs --pcb: the plan is checked against the design's current data first", path)
		}
		loaded[path] = pl
		for ref, v := range pl.Assign {
			if prev, dup := assign[ref]; dup && prev != v {
				return nil, nil, fmt.Errorf("%s is assigned both %s and %s", ref, prev, v)
			}
			assign[ref] = v
		}
	}
	return assign, loaded, nil
}

// verifyPlans re-reads the design and refuses when it no longer matches a plan
// (LCSC numbers, and the fingerprint of value / footprint / MPN / placements),
// or when --qty names a different board count than the plan was made for.
// Called immediately before the write, after every other input is resolved.
func verifyPlans(pcb, sch string, plans map[string]*kicad.Plan, qty int, qtySet bool) error {
	if len(plans) == 0 {
		return nil
	}
	bd, parts, _, err := readLcscParts(pcb, sch)
	if err != nil {
		return err
	}
	mpn := designMPNs(bd)
	paths := make([]string, 0, len(plans))
	for p := range plans {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		pl := plans[path]
		if qtySet && qty != pl.Boards {
			return fmt.Errorf("--qty %d differs from the %d board(s) plan %s was made for (nothing was written; regenerate with --alternatives --qty %d)", qty, pl.Boards, path, qty)
		}
		if bad := kicad.VerifyPlan(parts, mpn, pl); len(bad) > 0 {
			sort.Strings(bad)
			return fmt.Errorf("the design no longer matches plan %s (nothing was written; regenerate with --alternatives):\n  %s", path, strings.Join(bad, "\n  "))
		}
	}
	return nil
}
