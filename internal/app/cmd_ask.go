package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/console"
)

// Exit codes of `pcbpilot ask` (besides 0 = answered / default applied).
const (
	askExitUnavailable = 2 // no console and no terminal to ask on
	askExitExpired     = 3 // timed out without a default
	askExitCancelled   = 4
)

// newAskCmd is `pcbpilot ask`: post a decision card and wait for the user.
func newAskCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var question, contextText, contextFile, def, agent, runID, project, step, fallback string
	var options []string
	var free bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "ask",
		Short: "Ask the user a decision question (console card, terminal fallback) and print the choice",
		Long: `Posts a decision card to the console (Decisions page) and blocks until the user answers,
the card is cancelled, or --timeout passes. Prints JSON {id,status,choice,note,by}.

Use it for design trade-offs the user owns: Layout confirmation (P6), schematic value changes
from sim analog plans, feedback.json pin swaps, skipping a step, choosing between candidates.
It is NOT an approval channel for EDA writes the Skill forbids, and it never replaces the typed
readback evidence that goes with the question.

Fallback (--fallback auto): when no daemon/console is reachable and stdin is a terminal, the
question is asked in the terminal instead. Exit codes: 0 answered (or --default applied on
timeout), 2 nobody to ask, 3 expired without default, 4 cancelled.`,
		Example: `  pcbpilot ask --question "Layout 回读版本 v3 可以进入布线吗？" --option ok="确认，进入 P7" --option adjust="还要调整" --timeout 2h --step P6
  pcbpilot ask --question "U3 散热：加过孔还是换封装？" --option vias="加 6 个散热过孔" --option pkg="换 SOT-223" --default vias --timeout 30m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if contextFile != "" {
				b, err := os.ReadFile(contextFile)
				if err != nil {
					return err
				}
				contextText = string(b)
			}
			req := console.AskRequest{Question: question, Context: contextText, Default: def, AllowFree: free,
				Agent: agent, RunID: runID, Project: project, Step: step, TimeoutSec: int(timeout.Seconds())}
			for _, o := range options {
				id, label, _ := strings.Cut(o, "=")
				detail := ""
				if l, d, ok := strings.Cut(label, "::"); ok {
					label, detail = l, d
				}
				req.Options = append(req.Options, console.Option{ID: strings.TrimSpace(id), Label: strings.TrimSpace(label), Detail: strings.TrimSpace(detail)})
			}
			q, err := askViaConsole(cmd.Context(), cfg, req, timeout, stderr)
			if err != nil {
				var unreachable *askUnreachable
				if !errors.As(err, &unreachable) || fallback == "none" || !stdinIsTerminal() {
					if errors.As(err, &unreachable) {
						fmt.Fprintf(stderr, "ask: %v\n", err)
						return exitCodeError{askExitUnavailable}
					}
					return err
				}
				fmt.Fprintf(stderr, "ask: console unavailable (%v) — asking in the terminal\n", unreachable.err)
				q, err = askInTerminal(req, os.Stdin, stderr)
				if err != nil {
					return err
				}
			}
			if err := writeJSON(stdout, askResult(q)); err != nil {
				return err
			}
			switch q.Status {
			case "answered":
				return nil
			case "cancelled":
				return exitCodeError{askExitCancelled}
			default:
				return exitCodeError{askExitExpired}
			}
		},
	}
	f := c.Flags()
	f.StringVar(&question, "question", "", "the question (required)")
	f.StringArrayVar(&options, "option", nil, `choice as id=label or id=label::detail (repeatable)`)
	f.StringVar(&def, "default", "", "option id applied when the card expires (otherwise exit 3)")
	f.BoolVar(&free, "free", false, "allow a free-text answer")
	f.StringVar(&contextText, "context", "", "evidence shown with the card (facts, paths, numbers)")
	f.StringVar(&contextFile, "context-file", "", "read --context from a file (markdown)")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait (max 24h)")
	f.StringVar(&agent, "agent", os.Getenv("PCBPILOT_CLIENT_LABEL"), "asking agent label")
	f.StringVar(&runID, "run", "", "run id this question belongs to")
	f.StringVar(&project, "project-name", "", "project the decision is about")
	f.StringVar(&step, "step", "", "design-flow step (e.g. P6)")
	f.StringVar(&fallback, "fallback", "auto", "auto (terminal when the console is unreachable) | none")
	_ = c.MarkFlagRequired("question")
	return c
}

type askUnreachable struct{ err error }

func (e *askUnreachable) Error() string { return "console unreachable: " + e.err.Error() }

func askResult(q *console.Question) map[string]any {
	out := map[string]any{"id": q.ID, "status": q.Status, "question": q.Question}
	if q.Answer != nil {
		out["choice"], out["note"], out["by"], out["at"] = q.Answer.Choice, q.Answer.Note, q.Answer.By, q.Answer.At
	}
	return out
}

// askViaConsole posts the card and long-polls until it settles. On expiry
// with a default, the default is applied and reported as by=default.
func askViaConsole(ctx context.Context, cfg *appConfig, req console.AskRequest, timeout time.Duration, stderr io.Writer) (*console.Question, error) {
	cl, err := newConsoleClient(cfg, 70*time.Second)
	if err != nil {
		return nil, &askUnreachable{err}
	}
	var q console.Question
	if err := cl.do(ctx, "POST", "/api/ask", req, &q); err != nil {
		var he *consoleHTTPError
		if errors.As(err, &he) && he.status == 400 {
			return nil, err // a bad question is the caller's bug, not a fallback case
		}
		return nil, &askUnreachable{err}
	}
	if u, err := consoleURL(cfg); err == nil {
		fmt.Fprintf(stderr, "ask: decision card %s posted — answer in the console: %s (Decisions)\n", q.ID, strings.SplitN(u, "#", 2)[0]+"#/decisions")
	}
	deadline := time.Now().Add(timeout + 5*time.Second)
	for q.Status == "pending" && time.Now().Before(deadline) {
		var next console.Question
		if err := cl.do(ctx, "GET", "/api/ask/"+q.ID+"?wait=25", nil, &next); err != nil {
			var he *consoleHTTPError
			if errors.As(err, &he) && he.status == 404 {
				return nil, fmt.Errorf("decision %s vanished (daemon restarted?) — ask again", q.ID)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			time.Sleep(2 * time.Second) // daemon restarting: keep waiting
			continue
		}
		q = next
	}
	if q.Status == "expired" && req.Default != "" {
		q.Status = "answered"
		q.Answer = &console.Answer{Choice: req.Default, By: "default", At: time.Now().UTC()}
	}
	return &q, nil
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// askInTerminal is the no-console fallback.
func askInTerminal(req console.AskRequest, in io.Reader, out io.Writer) (*console.Question, error) {
	fmt.Fprintf(out, "\n%s\n", req.Question)
	if req.Context != "" {
		fmt.Fprintf(out, "%s\n", req.Context)
	}
	for i, o := range req.Options {
		id := o.ID
		if id == "" {
			id = fmt.Sprint(i + 1)
		}
		fmt.Fprintf(out, "  [%s] %s", id, o.Label)
		if o.Detail != "" {
			fmt.Fprintf(out, " — %s", o.Detail)
		}
		fmt.Fprintln(out)
	}
	sc := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "choice> ")
		if !sc.Scan() {
			return nil, errors.New("no answer (stdin closed)")
		}
		ans := strings.TrimSpace(sc.Text())
		if ans == "" && req.Default != "" {
			ans = req.Default
		}
		for i, o := range req.Options {
			if ans == o.ID || (o.ID == "" && ans == fmt.Sprint(i+1)) {
				return &console.Question{ID: "terminal", Question: req.Question, Status: "answered",
					Answer: &console.Answer{Choice: ans, By: "cli", At: time.Now().UTC()}}, nil
			}
		}
		if req.AllowFree && ans != "" {
			return &console.Question{ID: "terminal", Question: req.Question, Status: "answered",
				Answer: &console.Answer{Choice: ans, By: "cli", At: time.Now().UTC()}}, nil
		}
		fmt.Fprintln(out, "not an option — type one of the ids in [brackets]")
	}
}
