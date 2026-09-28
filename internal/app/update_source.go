package app

// Source installs: the CLI was built from a git checkout by
// scripts/setup-agent.sh, which records the checkout in ~/.pcbpilot/install.json
// ({"kind":"source","repo":...}). `pcbpilot update` then fast-forwards the
// checkout and re-runs setup (rebuild, relink skills, re-register MCP, sim
// tools, restart the service) instead of downloading release assets. A dirty
// or diverged checkout is never touched — the developer decides.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

var errSourceRefused = errors.New("source checkout not updated")

// sourceSetupCommand runs setup-agent.sh; swapped in tests.
var sourceSetupCommand = func(ctx context.Context, repo string, out io.Writer, detached bool) error {
	script := filepath.Join(repo, "scripts", "setup-agent.sh")
	cmd := exec.CommandContext(ctx, "bash", script, "--restart-daemon")
	if detached {
		// The daemon runs setup detached (own session) so a service restart
		// of the daemon cannot kill the setup mid-way.
		cmd = exec.Command("bash", script, "--restart-daemon")
		detachProcess(cmd)
	}
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = out, out
	if detached {
		return cmd.Start()
	}
	return cmd.Run()
}

func gitOut(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sourceStatus is the read-only verdict for a source checkout.
type sourceStatus struct {
	Repo     string `json:"repo"`
	Dirty    bool   `json:"dirty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Upstream string `json:"upstream,omitempty"`
	Error    string `json:"error,omitempty"`
}

func inspectSource(ctx context.Context, repo string, fetch bool) sourceStatus {
	st := sourceStatus{Repo: repo}
	if _, err := gitOut(ctx, repo, "rev-parse", "--is-inside-work-tree"); err != nil {
		st.Error = repo + " is not a git checkout"
		return st
	}
	if out, err := gitOut(ctx, repo, "status", "--porcelain", "--untracked-files=no"); err != nil {
		st.Error = "git status: " + out
		return st
	} else if out != "" {
		st.Dirty = true
		lines := strings.Split(out, "\n")
		if len(lines) > 5 {
			lines = append(lines[:5], "…")
		}
		st.Error = "uncommitted changes:\n    " + strings.Join(lines, "\n    ")
	}
	if fetch {
		if out, err := gitOut(ctx, repo, "fetch", "--quiet"); err != nil {
			if st.Error == "" {
				st.Error = "git fetch failed (offline?): " + out
			}
			return st
		}
	}
	up, err := gitOut(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		if st.Error == "" {
			st.Error = "the current branch has no upstream (git branch --set-upstream-to …)"
		}
		return st
	}
	st.Upstream = up
	counts, err := gitOut(ctx, repo, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err == nil {
		f := strings.Fields(counts)
		if len(f) == 2 {
			st.Ahead, st.Behind = selfupdate.Atoi(f[0]), selfupdate.Atoi(f[1])
		}
	}
	return st
}

// runSourceUpdate pulls (fast-forward only) and re-runs setup-agent.sh.
func runSourceUpdate(ctx context.Context, repo string, force, detached bool, out io.Writer) ([]stepRow, error) {
	st := inspectSource(ctx, repo, true)
	row := stepRow{Component: "source", Where: repo}
	switch {
	case st.Dirty:
		row.Status, row.Detail = "skipped", "refused: "+st.Error+"\n  commit or stash them, then run `pcbpilot update` again (nothing was changed)"
		return []stepRow{row}, errSourceRefused
	case st.Error != "":
		row.Status, row.Detail = "failed", st.Error
		return []stepRow{row}, errSourceRefused
	case st.Ahead > 0 && st.Behind > 0:
		row.Status = "skipped"
		row.Detail = fmt.Sprintf("refused: local branch diverged from %s (%d local / %d upstream commits) — rebase or merge yourself, then rerun (nothing was changed)", st.Upstream, st.Ahead, st.Behind)
		return []stepRow{row}, errSourceRefused
	case st.Behind == 0 && !force:
		row.Status, row.Detail = "current", "checkout is at "+st.Upstream+" (use --force to re-run setup anyway)"
		return []stepRow{row}, nil
	}
	rows := []stepRow{}
	if st.Behind > 0 {
		if o, err := gitOut(ctx, repo, "pull", "--ff-only"); err != nil {
			row.Status, row.Detail = "failed", "git pull --ff-only: "+o
			return []stepRow{row}, err
		}
		rows = append(rows, stepRow{Component: "source", Status: "updated", Where: repo, Detail: fmt.Sprintf("git pull --ff-only (%d commits from %s)", st.Behind, st.Upstream)})
	}
	selfupdate.AppendLog("source update: %s pulled %d commits; running setup-agent.sh (detached=%v)", repo, st.Behind, detached)
	if err := sourceSetupCommand(ctx, repo, out, detached); err != nil {
		rows = append(rows, stepRow{Component: "setup", Status: "failed", Where: filepath.Join(repo, "scripts", "setup-agent.sh"), Detail: err.Error()})
		return rows, err
	}
	status := "updated"
	if detached {
		status = "started"
	}
	rows = append(rows, stepRow{Component: "setup", Status: status, Where: filepath.Join(repo, "scripts", "setup-agent.sh"),
		Detail: "rebuilt CLI, relinked skills, re-registered MCP, sim tools, restarted the daemon service"})
	return rows, nil
}

// sourceInstall returns the checkout path when this is a source install:
// ~/.pcbpilot/install.json says so, or — for machines set up before
// install.json existed — a client skill dir is a symlink into
// <checkout>/.agents/skills/pcbpilot of a git checkout.
func sourceInstall() (string, bool) {
	isRepo := func(p string) bool {
		_, err := os.Stat(filepath.Join(p, ".git"))
		return err == nil
	}
	info := selfupdate.ReadInstallInfo()
	if info.Kind == "source" && info.Repo != "" && isRepo(info.Repo) {
		return info.Repo, true
	}
	if info.Kind == "release" {
		return "", false
	}
	for _, t := range selfupdate.Targets(true) {
		if t.Linked == "" {
			continue
		}
		repo := filepath.Dir(filepath.Dir(filepath.Dir(t.Linked))) // <repo>/.agents/skills/pcbpilot
		if filepath.Base(filepath.Dir(t.Linked)) == "skills" && filepath.Base(filepath.Dir(filepath.Dir(t.Linked))) == ".agents" && isRepo(repo) {
			return repo, true
		}
	}
	return "", false
}
