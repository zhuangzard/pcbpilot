package app

// cmd_review_panel.go — the "design-review" hard gate: Codex, Kimi and
// Claude Code review the design independently against the project's own
// requirements (user decision 2026-10-09: the design stage must be reviewed
// by several agents; the design must be right and meet the project's design
// requirements). Each reviewer runs as a separate CLI process with the same
// self-contained prompt and returns a JSON verdict per requirement. The gate
// passes only when every reviewer answered and none reports a blocking
// finding or an unmet requirement.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// reviewVerdict is one reviewer's structured answer.
type reviewVerdict struct {
	Reviewer     string              `json:"reviewer"`
	Verdict      string              `json:"verdict"` // pass | fail
	Requirements []reviewRequirement `json:"requirements"`
	Blocking     []string            `json:"blocking"`
	Advisory     []string            `json:"advisory"`
	Error        string              `json:"error,omitempty"`
	RawFile      string              `json:"rawFile,omitempty"`
	Seconds      float64             `json:"seconds"`
}

type reviewRequirement struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // met | not_met | cannot_judge
	Evidence string `json:"evidence"`
}

// reviewRecord is what the gate stores (review.json).
type reviewRecord struct {
	Stage       string   `json:"stage"`
	InputSHA256 string   `json:"inputSha256"`
	Inputs      []string `json:"inputs"`
	// FilesSHA256 is the SHA-256 of every complete input file, in Inputs
	// order. InputSHA256 hashes the prompt, which --max-bytes may cut: a
	// change past the cut keeps the prompt hash, never these.
	FilesSHA256 []string        `json:"filesSha256,omitempty"`
	Reviewers   []reviewVerdict `json:"reviewers"`
	Gate        gateResult      `json:"gate"`
	At          string          `json:"at"`
}

// reviewerRun runs one reviewer CLI on prompt and returns its stdout.
type reviewerRun func(ctx context.Context, prompt string) (string, error)

// reviewerCall is how one reviewer CLI receives the prompt.
//
// The prompt is NEVER put on the command line: a real review prompt is
// hundreds of KB, and Windows caps a whole command line at 32 767 chars (8 191
// through a .cmd shim such as codex.cmd). field report 2026-10-15: a 537 KB prompt
// made every reviewer fail with "fork/exec …: The filename or extension is too
// long". Codex and Claude read the prompt from stdin; Kimi gets a short
// instruction to read prompt.md, which is written into its working directory.
type reviewerCall struct {
	Bin   string
	Args  []string
	Stdin string
	// PromptFile, when set, is the file name (inside the call's working
	// directory) the prompt must be written to before the CLI starts.
	PromptFile string
}

const reviewerPromptFile = "prompt.md"

// reviewerFileAsk is the whole command-line payload for a reviewer that reads
// the prompt from disk.
const reviewerFileAsk = "Read the file " + reviewerPromptFile + " in the current directory completely (it is long; read all of it) " +
	"and perform the design review it describes. Do NOT modify, create or delete any file and do not run commands other than reading files. " +
	"End your reply with exactly ONE JSON object in the format the prompt specifies."

// reviewerInvocation builds the call for one reviewer. dir is the reviewer's
// private working directory ("" = inherit).
func reviewerInvocation(name, dir, prompt string) (reviewerCall, error) {
	switch name {
	case "codex":
		// `codex exec -` reads the prompt from stdin; -C pins its workspace to
		// the private directory and the sandbox is read-only.
		return reviewerCall{Bin: "codex", Args: []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-C", dir, "-"}, Stdin: prompt}, nil
	case "claude":
		// `claude -p` with no prompt argument reads it from stdin. The prompt
		// is self-contained, so the reviewer gets no tools at all.
		return reviewerCall{Bin: "claude", Args: []string{"-p", "--tools", ""}, Stdin: prompt}, nil
	case "kimi":
		// kimi -p has no stdin mode: hand it the prompt as a file in its own
		// directory (--add-dir grants the read). Not --auto: kimi refuses -p
		// combined with --auto (field workflow note, 2026-10-15).
		return reviewerCall{Bin: "kimi", Args: []string{"--add-dir", dir, "-p", reviewerFileAsk}, PromptFile: reviewerPromptFile}, nil
	}
	return reviewerCall{}, fmt.Errorf("unknown reviewer %q", name)
}

// reviewerRunner runs reviewer name in a private, otherwise empty temp
// directory. External review CLIs must never get the project repository as
// cwd / -C / --add-dir: field report 2026-10-19 had a review CLI read confidential
// files from the working directory, and codex used to inherit the caller's
// cwd (the project) here. The prompt already carries every input it needs.
func reviewerRunner(name string) reviewerRun {
	return func(ctx context.Context, prompt string) (string, error) {
		dir, err := os.MkdirTemp("", "pcbpilot-review-"+name+"-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		call, err := reviewerInvocation(name, dir, prompt)
		if err != nil {
			return "", err
		}
		if call.PromptFile != "" {
			if err := os.WriteFile(filepath.Join(dir, call.PromptFile), []byte(prompt), 0o644); err != nil {
				return "", err
			}
		}
		return runReviewerCLI(ctx, dir, call)
	}
}

var reviewerRunners = map[string]reviewerRun{
	"codex":  reviewerRunner("codex"),
	"kimi":   reviewerRunner("kimi"),
	"claude": reviewerRunner("claude"),
}

func runReviewerCLI(ctx context.Context, dir string, call reviewerCall) (string, error) {
	cmd := exec.CommandContext(ctx, call.Bin, call.Args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if call.Stdin != "" {
		cmd.Stdin = strings.NewReader(call.Stdin)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("%s timed out", call.Bin)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", call.Bin, err, tail(string(out), 300))
	}
	return string(out), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// lastVerdictJSON finds the last balanced {...} object in out that has a
// "verdict" key (reviewers wrap JSON in prose, bullets or code fences).
func lastVerdictJSON(out string) (reviewVerdict, bool) {
	var best reviewVerdict
	found := false
	for i := 0; i < len(out); i++ {
		if out[i] != '{' {
			continue
		}
		depth, inStr, esc := 0, false, false
		for j := i; j < len(out); j++ {
			c := out[j]
			switch {
			case esc:
				esc = false
			case c == '\\' && inStr:
				esc = true
			case c == '"':
				inStr = !inStr
			case !inStr && c == '{':
				depth++
			case !inStr && c == '}':
				depth--
				if depth == 0 {
					var v reviewVerdict
					// Only a real verdict counts: an echoed prompt carries the
					// "pass|fail" template, which must never be read as an answer.
					if json.Unmarshal([]byte(out[i:j+1]), &v) == nil && (v.Verdict == "pass" || v.Verdict == "fail") {
						best, found = v, true
					}
					j = len(out)
				}
			}
		}
	}
	return best, found
}

// reviewPrompt builds the one self-contained prompt every reviewer gets.
func reviewPrompt(stage string, reqs, evidence map[string]string, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are an independent senior hardware design reviewer (electronics, PCB, medical / ISO safety: IEC 60601-1 MOOP/MOPP, IEC 60664-1, IEC 61010-1, IPC-2221B). Review stage: %s.

Judge whether the DESIGN below is correct and meets EVERY project requirement. Be critical and specific; do not validate by default. For each requirement ID in the requirements documents, decide met / not_met / cannot_judge from the evidence, with a one-line evidence pointer. Add a "blocking" item for anything that makes the design wrong or unsafe (electrical error, safety distance, wrong part, missing function, requirement not met). "advisory" holds improvements that are not errors.

Answer with ONE JSON object at the end of your reply, exactly this shape:
{"verdict":"pass|fail","requirements":[{"id":"R-01","status":"met|not_met|cannot_judge","evidence":"..."}],"blocking":["..."],"advisory":["..."]}
verdict is "fail" when any requirement is not_met or any blocking item exists.

`, stage)
	keys := func(m map[string]string) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	clip := func(s string) string {
		if limit > 0 && len(s) > limit {
			return s[:limit] + fmt.Sprintf("\n…[truncated: %d of %d bytes shown]", limit, len(s))
		}
		return s
	}
	b.WriteString("=== PROJECT REQUIREMENTS ===\n")
	for _, k := range keys(reqs) {
		fmt.Fprintf(&b, "--- %s ---\n%s\n", k, clip(reqs[k]))
	}
	b.WriteString("\n=== DESIGN EVIDENCE ===\n")
	for _, k := range keys(evidence) {
		fmt.Fprintf(&b, "--- %s ---\n%s\n", k, clip(evidence[k]))
	}
	return b.String()
}

// reviewGate turns the verdicts into the design-review gate.
func reviewGate(vs []reviewVerdict, want []string) gateResult {
	g := gateResult{Gate: "design-review", Pass: true}
	got := map[string]bool{}
	for _, v := range vs {
		got[v.Reviewer] = true
		switch {
		case v.Error != "":
			g.Pass = false
			g.Items = append(g.Items, fmt.Sprintf("%s: no verdict (%s)", v.Reviewer, v.Error))
			continue
		case v.Verdict != "pass":
			g.Pass = false
		}
		for _, r := range v.Requirements {
			if r.Status == "not_met" {
				g.Pass = false
				g.Items = append(g.Items, fmt.Sprintf("%s: %s not met — %s", v.Reviewer, r.ID, r.Evidence))
			}
		}
		for _, bl := range v.Blocking {
			g.Pass = false
			g.Items = append(g.Items, fmt.Sprintf("%s: blocking — %s", v.Reviewer, bl))
		}
		if v.Verdict != "pass" && len(v.Blocking) == 0 && !hasNotMet(v) {
			g.Items = append(g.Items, fmt.Sprintf("%s: verdict %q", v.Reviewer, v.Verdict))
		}
	}
	for _, w := range want {
		if !got[w] {
			g.Pass = false
			g.Items = append(g.Items, w+": did not run")
		}
	}
	n := 0
	for _, v := range vs {
		if v.Error == "" {
			n++
		}
	}
	g.Detail = fmt.Sprintf("%d of %d reviewers (%s) answered; every requirement met and no blocking finding required", n, len(want), strings.Join(want, ", "))
	return g
}

func hasNotMet(v reviewVerdict) bool {
	for _, r := range v.Requirements {
		if r.Status == "not_met" {
			return true
		}
	}
	return false
}

// runReviewPanel runs the reviewers in parallel and writes outDir/review.json
// and one raw transcript per reviewer.
func runReviewPanel(stage string, reqFiles, evFiles, reviewers []string, outDir string, timeout time.Duration, limit int, waivers []gateWaiver, stderr io.Writer) (*reviewRecord, error) {
	read := func(files []string) (map[string]string, error) {
		m := map[string]string{}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			m[filepath.Base(f)] = string(b)
		}
		return m, nil
	}
	if len(reqFiles) == 0 {
		return nil, fmt.Errorf("--requirements is required: the review judges the design against the project's own requirements")
	}
	reqs, err := read(reqFiles)
	if err != nil {
		return nil, err
	}
	ev, err := read(evFiles)
	if err != nil {
		return nil, err
	}
	prompt := reviewPrompt(stage, reqs, ev, limit)
	sum := sha256.Sum256([]byte(prompt))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "prompt.md"), []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	vs := make([]reviewVerdict, len(reviewers))
	var wg sync.WaitGroup
	for i, name := range reviewers {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			start := time.Now()
			v := reviewVerdict{Reviewer: name}
			run, ok := reviewerRunners[name]
			if !ok {
				v.Error = "unknown reviewer"
				vs[i] = v
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			out, err := run(ctx, prompt)
			// reviewer_<name>.md, not <name>.md: on a case-insensitive filesystem
			// (Windows, macOS) claude.md is the same file as a CLAUDE.md agent memo.
			raw := filepath.Join(outDir, reviewerRawFile(name))
			_ = os.WriteFile(raw, []byte(out), 0o644)
			v.RawFile = raw
			if err != nil {
				v.Error = err.Error()
			} else if parsed, ok := lastVerdictJSON(out); ok {
				parsed.Reviewer, parsed.RawFile = name, raw
				v = parsed
			} else {
				v.Error = "no JSON verdict in the reply"
			}
			v.Seconds = time.Since(start).Round(time.Second).Seconds()
			fmt.Fprintf(stderr, "design-review: %s %s in %.0f s\n", name, map[bool]string{true: "answered", false: "FAILED"}[v.Error == ""], v.Seconds)
			vs[i] = v
		}(i, name)
	}
	wg.Wait()
	g := reviewGate(vs, reviewers)
	applyWaivers(&g, waivers)
	files, err := reviewFilesSHA(reqFiles, evFiles)
	if err != nil {
		return nil, err
	}
	rec := &reviewRecord{Stage: stage, InputSHA256: fmt.Sprintf("%x", sum), Inputs: append(append([]string{}, reqFiles...), evFiles...),
		FilesSHA256: files, Reviewers: vs, Gate: g, At: time.Now().UTC().Format(time.RFC3339)}
	blob, _ := json.MarshalIndent(rec, "", "  ")
	return rec, os.WriteFile(filepath.Join(outDir, "review.json"), append(blob, '\n'), 0o644)
}

// reapplyReviewWaivers recomputes the gate of an existing review.json with a new
// waiver set, without running the reviewers again. The requirement and evidence
// files must produce exactly the prompt that was reviewed (same input SHA-256);
// the reviewers' verdicts are taken as recorded, never edited.
func reapplyReviewWaivers(stage string, reqFiles, evFiles []string, outDir string, limit int, waivers []gateWaiver) (*reviewRecord, error) {
	path := filepath.Join(outDir, "review.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--reapply-waivers: %w", err)
	}
	var rec reviewRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("--reapply-waivers: %s: %w", path, err)
	}
	read := func(files []string) (map[string]string, error) {
		m := map[string]string{}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			m[filepath.Base(f)] = string(b)
		}
		return m, nil
	}
	reqs, err := read(reqFiles)
	if err != nil {
		return nil, err
	}
	ev, err := read(evFiles)
	if err != nil {
		return nil, err
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(reviewPrompt(stage, reqs, ev, limit))))
	if rec.Stage != stage || rec.InputSHA256 != sum {
		return nil, fmt.Errorf("--reapply-waivers: the requirements/evidence differ from the reviewed input (sha256 %s, review.json %s) - run the review again", sum, rec.InputSHA256)
	}
	if why := reviewFilesChanged(&rec, reqFiles, evFiles); why != "" {
		return nil, fmt.Errorf("--reapply-waivers: %s - run the review again", why)
	}
	var names []string
	for _, v := range rec.Reviewers {
		names = append(names, v.Reviewer)
	}
	g := reviewGate(rec.Reviewers, names)
	applyWaivers(&g, waivers)
	rec.Gate = g
	blob, _ := json.MarshalIndent(&rec, "", "  ")
	return &rec, os.WriteFile(path, append(blob, '\n'), 0o644)
}

func newReviewPanelCmd(stdout, stderr io.Writer) *cobra.Command {
	var reqFiles, evFiles []string
	var reviewersCSV, outDir, stage, waiverPath string
	var timeout time.Duration
	var limit int
	var reapply bool
	c := &cobra.Command{
		Use:   "review-panel",
		Short: "Hard gate: Codex, Kimi and Claude Code review the design against the project requirements",
		Long: `Runs the design-review gate. Every reviewer (default codex, kimi, claude —
their CLIs must be installed and logged in) gets the same self-contained prompt:
the project's requirement documents (--requirements, e.g. 03_Requirement/*.md,
00_Project_Scope/SCOPE.md) and the design evidence (--evidence: intent.json,
schematic connectivity / netlist, sim results, gate summary, design report …).
Each returns a JSON verdict per requirement ID. The gate PASSES only when all
reviewers answered and none reports a not_met requirement or a blocking
finding; a missing, timed-out or unparsable reviewer FAILS it. Results:
--out-dir/{review.json, prompt.md, reviewer_<name>.md}. Signed waivers
({gate:"design-review", match, reason, by}) are the only override.

--reapply-waivers recomputes the gate of --out-dir/review.json with --waivers
without running the reviewers again; the requirements and evidence must be
byte-identical to the reviewed input (the prompt SHA-256 and the SHA-256 of
every complete input file are checked, so a change past --max-bytes counts).`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot review-panel --stage design \
    --requirements 03_Requirement/REQUIREMENTS.md --requirements 03_Requirement/CONNECTOR_PINOUT.md \
    --requirements 00_Project_Scope/SCOPE.md \
    --evidence intent.json --evidence sch-connectivity.json --evidence sim.json --out-dir review/design`,
		RunE: func(cmd *cobra.Command, args []string) error {
			waivers, err := loadWaivers(waiverPath)
			if err != nil {
				return err
			}
			var reviewers []string
			for _, r := range strings.Split(reviewersCSV, ",") {
				if r = strings.TrimSpace(r); r != "" {
					reviewers = append(reviewers, r)
				}
			}
			var rec *reviewRecord
			if reapply {
				rec, err = reapplyReviewWaivers(stage, reqFiles, evFiles, outDir, limit, waivers)
			} else {
				rec, err = runReviewPanel(stage, reqFiles, evFiles, reviewers, outDir, timeout, limit, waivers, stderr)
			}
			if err != nil {
				return err
			}
			if err := writeJSON(stdout, rec.Gate); err != nil {
				return err
			}
			if !rec.Gate.Pass {
				return fmt.Errorf("design-review gate failed: %s", rec.Gate.Detail)
			}
			return nil
		},
	}
	c.Flags().StringArrayVar(&reqFiles, "requirements", nil, "project requirement document (repeatable, required)")
	c.Flags().StringArrayVar(&evFiles, "evidence", nil, "design evidence file (repeatable): intent, connectivity/netlist, sim, gates summary, report")
	c.Flags().StringVar(&reviewersCSV, "reviewers", "codex,kimi,claude", "reviewer CLIs, comma-separated (all must pass)")
	c.Flags().StringVar(&stage, "stage", "design", "design | layout | release")
	c.Flags().StringVar(&outDir, "out-dir", "design-review", "where review.json and the transcripts go")
	c.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "per-reviewer time limit")
	c.Flags().IntVar(&limit, "max-bytes", 120000, "per-file byte limit in the prompt (truncation is stated in the prompt)")
	c.Flags().StringVar(&waiverPath, "waivers", "", "signed waivers JSON")
	c.Flags().BoolVar(&reapply, "reapply-waivers", false, "recompute the gate of --out-dir/review.json with --waivers (same input, reviewers not run again)")
	return c
}

// reviewerRawFile is the file a reviewer's raw reply is saved to.
func reviewerRawFile(name string) string { return "reviewer_" + name + ".md" }

// reviewFilesSHA hashes every complete input file (requirements, then
// evidence), independent of the prompt and its --max-bytes cut.
func reviewFilesSHA(reqFiles, evFiles []string) ([]string, error) {
	var out []string
	for _, f := range append(append([]string{}, reqFiles...), evFiles...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%x", sha256.Sum256(b)))
	}
	return out, nil
}

// reviewFilesChanged says why rec's verdicts do not belong to these files:
// a file differs from the reviewed one (also past the prompt's cut), the
// file list differs, or rec predates the per-file hashes. "" = unchanged.
func reviewFilesChanged(rec *reviewRecord, reqFiles, evFiles []string) string {
	if len(rec.FilesSHA256) == 0 {
		return "review.json has no per-file hashes (an older review), so a change past the prompt's --max-bytes cut cannot be ruled out"
	}
	got, err := reviewFilesSHA(reqFiles, evFiles)
	if err != nil {
		return err.Error()
	}
	files := append(append([]string{}, reqFiles...), evFiles...)
	if len(got) != len(rec.FilesSHA256) {
		return fmt.Sprintf("%d input file(s), the review had %d", len(got), len(rec.FilesSHA256))
	}
	for i := range got {
		if got[i] != rec.FilesSHA256[i] {
			return fmt.Sprintf("%s differs from the reviewed file (sha256 %s, review.json %s)", files[i], got[i], rec.FilesSHA256[i])
		}
	}
	return ""
}
