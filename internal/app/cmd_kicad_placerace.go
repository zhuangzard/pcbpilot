package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// placeVariant is one way of drawing placement candidates (v0.9.2 E2).
type placeVariant struct {
	Name    string  `json:"name"`
	Refine  bool    `json:"refine"`            // start from the board's own placement
	HaloMil float64 `json:"haloMil,omitempty"` // keep-clear added around every fine-pitch IC
}

// parsePlaceVariants reads "scratch", "halo:40", "refine", "refine-halo:40".
func parsePlaceVariants(spec string) ([]placeVariant, error) {
	var out []placeVariant
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v := placeVariant{Name: f}
		kind, arg, hasArg := strings.Cut(f, ":")
		switch kind {
		case "scratch":
		case "halo":
		case "refine":
			v.Refine = true
		case "refine-halo":
			v.Refine = true
		default:
			return nil, fmt.Errorf("unknown placement variant %q (scratch | halo:MIL | refine | refine-halo:MIL)", f)
		}
		if strings.HasSuffix(kind, "halo") {
			if !hasArg {
				return nil, fmt.Errorf("variant %q needs a halo in mil (%s:40)", f, kind)
			}
			m, err := strconv.ParseFloat(arg, 64)
			if err != nil || m <= 0 {
				return nil, fmt.Errorf("variant %q: bad halo %q", f, arg)
			}
			v.HaloMil = m
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--variants is empty")
	}
	return out, nil
}

// raceCandidate is one placement with its cheap proxy.
type raceCandidate struct {
	ID      string         `json:"id"`
	Variant string         `json:"variant"`
	Seed    int64          `json:"seed"`
	Illegal int            `json:"illegal"`
	Score   float64        `json:"placeScore"`
	WireIn  float64        `json:"wirelengthIn"`
	Proxy   placementProxy `json:"proxy"`
	ProxyMs int64          `json:"proxyMs"`
	Rank    int            `json:"rank"`
	File    string         `json:"file,omitempty"`
	board   *pcbauto.Board
}

type placeRaceReport struct {
	PCB        string             `json:"pcb"`
	OutDir     string             `json:"outDir"`
	Baseline   raceCandidate      `json:"baseline"`
	Candidates []raceCandidate    `json:"candidates"`
	Seconds    map[string]float64 `json:"seconds"`
}

func newKicadPlaceRaceCmd(stdout, stderr io.Writer) *cobra.Command {
	var o struct {
		pcb, outDir, intent, power, sim, variants string
		seeds, top                                int
		seed                                      int64
		keepRefs                                  bool
	}
	c := &cobra.Command{
		Use:   "place-race",
		Short: "Staged placement race: many seeds/variants, cheap escape-channel proxy, write the best K (experiment)",
		Long: `Draws placement candidates (--variants × --seeds), scores every one with the
escape-channel proxy (milliseconds, no router) and writes the best --top boards
to --out-dir/cand-N.kicad_pcb with a report; the input placement is scored too
(baseline). Candidates the placer itself calls illegal never rank. Only the
written few go on to a full 'kicad route' + every gate — the proxy only
orders them, it is not a gate. The input board is never modified.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := runPlaceRace(o.pcb, o.outDir, o.intent, o.power, o.sim, o.variants, o.seeds, o.top, o.seed, kicad.Snapshot, kicad.Place, stderr)
			if err != nil {
				return err
			}
			printPlaceRace(stdout, rep)
			return nil
		},
	}
	c.Flags().StringVar(&o.pcb, "pcb", "", "input .kicad_pcb (required; never modified)")
	c.Flags().StringVar(&o.outDir, "out-dir", "place-race", "output directory")
	c.Flags().StringVar(&o.intent, "intent", "", "intent.json (weights the placement)")
	c.Flags().StringVar(&o.power, "power", "", "power spec JSON")
	c.Flags().StringVar(&o.sim, "sim", "", "sim power JSON")
	c.Flags().StringVar(&o.variants, "variants", "scratch,halo:40,refine-halo:40", "scratch | halo:MIL | refine | refine-halo:MIL, comma-separated")
	c.Flags().IntVar(&o.seeds, "seeds", 8, "seeds per variant")
	c.Flags().IntVar(&o.top, "top", 3, "boards to write")
	c.Flags().Int64Var(&o.seed, "seed", 1, "first seed")
	return c
}

func runPlaceRace(pcb, outDir, intent, power, sim, variantSpec string, seeds, top int, seed0 int64,
	snapshot func(string) ([]byte, error), place func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error), stderr io.Writer) (*placeRaceReport, error) {
	if pcb == "" {
		return nil, fmt.Errorf("--pcb is required")
	}
	variants, err := parsePlaceVariants(variantSpec)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	raw, err := snapshot(pcb)
	if err != nil {
		return nil, err
	}
	var snap boardSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	b, _, err := kicadBoard(raw)
	if err != nil {
		return nil, err
	}
	pw, err := kicadPlacePower(kicadPlaceOpts{power: power, sim: sim, intent: intent})
	if err != nil {
		return nil, err
	}
	clr, viaDia := 5.9, 23.622
	if snap.Rules != nil {
		if snap.Rules.ClearanceMil > 0 {
			clr = snap.Rules.ClearanceMil
		}
		if snap.Rules.ViaDiameterMil > 0 {
			viaDia = snap.Rules.ViaDiameterMil
		}
	}
	prep := func(bc *pcbauto.Board) (*pcbauto.Analysis, *pcbauto.Circuit, error) {
		a := pcbauto.Analyze(bc, pw, nil)
		return a, pcbauto.Understand(bc, a), nil
	}
	rep := &placeRaceReport{PCB: pcb, OutDir: outDir, Seconds: map[string]float64{}}
	score := func(id, variant string, bd *pcbauto.Board) (raceCandidate, *escapeEnv) {
		t := time.Now()
		env := envOfBoard(bd, clr, viaDia)
		cp := defaultChannelParams(env)
		pr, _ := env.scorePlacement(cp)
		return raceCandidate{ID: id, Variant: variant, Proxy: pr, ProxyMs: time.Since(t).Milliseconds()}, env
	}
	rep.Baseline, _ = score("baseline", "input", b)
	denseRefs := func() map[string]float64 {
		env := envOfBoard(b, clr, viaDia)
		m := map[string]float64{}
		for ref := range env.denseICs() {
			m[ref] = 1
		}
		return m
	}()
	fmt.Fprintf(stderr, "place-race: %d fine-pitch IC(s); baseline trapped %d tight %d meanViaDirs %.2f\n",
		len(denseRefs), len(rep.Baseline.Proxy.Trapped), len(rep.Baseline.Proxy.Tight), rep.Baseline.Proxy.MeanViaDirs)
	label := pcbauto.LabelSpec{}
	if ls := kicadLabelSpec(raw); ls != nil {
		label = *ls
	}
	tPlace := time.Now()
	var cands []raceCandidate
	for vi, v := range variants {
		popt := pcbauto.PlaceOptions{Seed: seed0 + int64(vi)*1000, Refine: v.Refine}
		if label.Height > 0 {
			l := label
			popt.Labels = &l
		}
		if v.HaloMil > 0 {
			popt.Halo = map[string]float64{}
			for ref := range denseRefs {
				popt.Halo[ref] = v.HaloMil
			}
		}
		outs, _, err := pcbauto.PlaceSeeds(b, prep, nil, popt, seeds)
		if err != nil {
			fmt.Fprintf(stderr, "place-race: variant %s: %v\n", v.Name, err)
			continue
		}
		for _, oc := range outs {
			id := fmt.Sprintf("%s#%d", v.Name, oc.Trial.Seed)
			rc, _ := score(id, v.Name, oc.Board)
			rc.Seed, rc.Illegal, rc.Score, rc.WireIn, rc.board = oc.Trial.Seed, oc.Trial.Illegal, oc.Trial.Score, oc.Trial.Metrics.WirelengthIn, oc.Board
			cands = append(cands, rc)
		}
		fmt.Fprintf(stderr, "place-race: variant %s: %d placement(s)\n", v.Name, len(outs))
	}
	rep.Seconds["placement+proxy"] = time.Since(tPlace).Seconds()
	// Rank: legal first, then the proxy, then the placer's own score.
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if (a.Illegal == 0) != (b.Illegal == 0) {
			return a.Illegal == 0
		}
		if a.Proxy.Better(b.Proxy) != b.Proxy.Better(a.Proxy) {
			return a.Proxy.Better(b.Proxy)
		}
		return a.Score < b.Score
	})
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	written := 0
	for i := range cands {
		cands[i].Rank = i + 1
		if written < top && cands[i].Illegal == 0 {
			f := filepath.Join(outDir, fmt.Sprintf("cand-%d.kicad_pcb", written+1))
			if _, err := place(pcb, posesFromBoard(cands[i].board), f); err != nil {
				return nil, err
			}
			if err := copyKicadPro(pcb, f); err != nil {
				return nil, err
			}
			cands[i].File = f
			written++
		}
	}
	// A deliberately worst legal candidate is the negative control for the
	// proxy: route it too and the proxy order should be visible in the result.
	for i := len(cands) - 1; i >= 0; i-- {
		if cands[i].Illegal == 0 && cands[i].File == "" {
			f := filepath.Join(outDir, "worst.kicad_pcb")
			if _, err := place(pcb, posesFromBoard(cands[i].board), f); err != nil {
				return nil, err
			}
			if err := copyKicadPro(pcb, f); err != nil {
				return nil, err
			}
			cands[i].File = f
			break
		}
	}
	for i := range cands {
		cands[i].board = nil
	}
	rep.Candidates = cands
	rep.Seconds["total"] = time.Since(t0).Seconds()
	blob, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "place-race.json"), append(blob, '\n'), 0o644); err != nil {
		return nil, err
	}
	return rep, nil
}

func printPlaceRace(w io.Writer, r *placeRaceReport) {
	row := func(c raceCandidate) {
		fmt.Fprintf(w, "%-22s %4d %7.1f %5.1f %4d %5d %5.2f %7.1f  %s\n", c.ID, c.Rank, c.Score, c.WireIn, c.Illegal,
			len(c.Proxy.Trapped)*100+len(c.Proxy.Tight), c.Proxy.MeanViaDirs, c.Proxy.MinGapMil, filepath.Base(c.File))
	}
	fmt.Fprintf(w, "%-22s %4s %7s %5s %4s %5s %5s %7s  %s\n", "candidate", "rank", "score", "wire", "ill", "T*100+t", "viaD", "minGap", "file")
	row(r.Baseline)
	for i, c := range r.Candidates {
		if i < 12 || c.File != "" {
			row(c)
		}
	}
	fmt.Fprintf(w, "placement+proxy %.1fs, total %.1fs, report %s\n", r.Seconds["placement+proxy"], r.Seconds["total"], filepath.Join(r.OutDir, "place-race.json"))
}
