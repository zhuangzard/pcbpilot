package selfupdate

// Multi-Skill sync. Every (client, Skill) destination of one client is staged
// completely before any of them is switched; the switches then run as one
// transaction per client, and a failed switch puts every earlier switch of that
// client back. Clients stay isolated from each other: one client's failure
// never blocks another. With a journal path the replaced and removed trees are
// kept so a later rollback restores exactly what this sync changed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SyncOptions configures SyncSkills.
type SyncOptions struct {
	// TargetVersion is the version to bring skill dirs up to (bare "x.y.z" or
	// "vx.y.z"). Required.
	TargetVersion string
	// Clients filters which clients to touch (nil = all present on disk).
	Clients []string
	// Preserve keeps existing files instead of overwriting (local edits win).
	// It applies to every Skill, and keeps retired Skills in place.
	Preserve bool
	// Force syncs even when a dir's marker already equals the target.
	Force bool
	// CreateMissing installs into a client dir even if it doesn't exist yet
	// (the manual `skill sync` default; the daemon leaves this false). A client
	// that already has the router gets missing focused Skills without it.
	CreateMissing bool
	// Adopt lets a focused Skill replace an existing directory of the same name
	// that pcbpilot did not install (no .version marker). Without it such a
	// directory is reported as "conflict" and left untouched.
	Adopt bool
	// Assets, when set, supplies skills.tar.gz (a local asset dir or a pinned
	// release) instead of the default GitHub download; its Version() is written
	// verbatim as the marker, so X.Y.Z-dev.N local packages keep their suffix.
	Assets AssetSource
	// Journal, when set, is a directory where the sync records every
	// destination it created, replaced or removed (with the previous trees),
	// for RestoreSnapshot. update sets it inside the rollback snapshot.
	Journal string
}

// TargetOutcome is the per-dir result of a sync: one Skill of one client.
type TargetOutcome struct {
	Client string `json:"client"`
	Skill  string `json:"skill"`
	Dir    string `json:"dir"`
	From   string `json:"from"` // installed version before
	To     string `json:"to"`   // target version
	// Status: updated|up-to-date|created|preserved|skipped|linked|conflict|error,
	// or for a retired Skill removed|kept (kept = not pcbpilot-owned, linked,
	// or preserve). conflict = a same-named directory pcbpilot did not install.
	Status string `json:"status"`
	Err    string `json:"err,omitempty"`
	Linked string `json:"linked,omitempty"` // symlink target of a source-install skill
}

// SyncResult is the full sync report.
type SyncResult struct {
	Target   string          `json:"target"`
	Outcomes []TargetOutcome `json:"outcomes"`
	Changed  int             `json:"changed"`
}

const (
	syncKeep = iota // outcome is final
	syncWrite
	syncRemove
)

type syncItem struct {
	out    TargetOutcome
	action int
}

// SyncSkills brings every public Skill of the selected clients up to
// TargetVersion by downloading that release's skills.tar.gz once and
// installing each listed Skill into its own dir. Skills the target release
// retired are removed where pcbpilot installed them. logf may be nil. The
// download only happens if at least one dir needs it.
func SyncSkills(ctx context.Context, opts SyncOptions, logf func(string, ...any)) (SyncResult, error) {
	log := func(format string, a ...any) {
		if logf != nil {
			logf(format, a...)
		}
	}
	target := SemverCore(opts.TargetVersion)
	if opts.Assets != nil {
		target = strings.TrimPrefix(opts.Assets.Version(), "v")
	}
	if target == "" {
		return SyncResult{}, fmt.Errorf("sync: bad target version %q", opts.TargetVersion)
	}
	res := SyncResult{Target: target}

	want := opts.Clients
	if len(want) == 0 {
		want = append(want, clientOrder...)
	}
	var clients []string
	for _, c := range want {
		if skillDir(c) == "" {
			clientErr := ValidateClients([]string{c})
			res.Outcomes = append(res.Outcomes, TargetOutcome{Client: c, Skill: SkillName, Status: "error", Err: clientErr.Error()})
			continue
		}
		clients = append(clients, c)
	}
	if err := syncOutcomeError(res); err != nil {
		return res, err
	}
	if opts.Journal != "" {
		// An (empty) journal says "this update's skill step ran": a rollback then
		// restores exactly what it lists and nothing else.
		if err := journalWrite(opts.Journal, nil); err != nil {
			return res, fmt.Errorf("rollback journal: %w", err)
		}
	}

	// Decide per-dir what needs doing before paying for a download. The known
	// set is this CLI's list plus the manifest each router was installed with,
	// so a focused Skill this CLI has never heard of is still completed. The
	// fetched manifest then decides what is installed.
	known := knownSkills(clients)
	items := planSkillSync(clients, known, retiredSkills(known, known), opts, target)
	if !syncNeedsWork(items) {
		for _, it := range items {
			res.Outcomes = append(res.Outcomes, it.out)
		}
		return res, syncOutcomeError(res)
	}

	log("skill-sync: fetching skills.tar.gz for v%s", target)
	var tree skillTree
	var err error
	if opts.Assets != nil {
		tree, err = assetSkillTree(ctx, opts.Assets)
	} else {
		tree, err = fetchSkillTree(ctx, target)
	}
	if err != nil {
		for _, it := range items {
			if it.action != syncKeep {
				it.out.Status, it.out.Err = "error", err.Error()
			}
			res.Outcomes = append(res.Outcomes, it.out)
		}
		return res, fmt.Errorf("fetch skills v%s: %w", target, err)
	}
	defer tree.cleanup()

	items = planSkillSync(clients, tree.manifest, retiredSkills(tree.manifest, known), opts, target)
	for _, client := range clients {
		var mine []syncItem
		for _, it := range items {
			if it.out.Client == client {
				mine = append(mine, it)
			}
		}
		res.Outcomes = append(res.Outcomes, applyClientSync(tree, mine, opts, target, &res.Changed, log)...)
	}
	return res, syncOutcomeError(res)
}

// applyClientSync stages every change of one client, then commits them as one
// transaction. Any failure leaves that client exactly as it was.
func applyClientSync(tree skillTree, items []syncItem, opts SyncOptions, target string, changed *int, log func(string, ...any)) []TargetOutcome {
	txn := &skillTxn{}
	fail := func(err error) []TargetOutcome {
		txn.discard()
		out := make([]TargetOutcome, 0, len(items))
		for _, it := range items {
			if it.action != syncKeep {
				it.out.Status, it.out.Err = "error", err.Error()
				log("skill-sync: %s %s FAILED (client left unchanged): %v", SkillLabel(it.out.Client, it.out.Skill), it.out.Dir, err)
			}
			out = append(out, it.out)
		}
		return out
	}
	for _, it := range items {
		var err error
		switch it.action {
		case syncWrite:
			err = txn.stageWrite(tree.dir(it.out.Skill), it.out.Dir, opts.Preserve, target)
		case syncRemove:
			err = txn.stageRemove(it.out.Dir)
		}
		if err != nil {
			return fail(fmt.Errorf("stage %s: %w", it.out.Dir, err))
		}
	}
	if len(txn.changes) == 0 {
		return outcomesOf(items)
	}
	if err := txn.commit(); err != nil {
		return fail(err)
	}
	if err := txn.recordJournal(opts.Journal); err != nil {
		// Without a durable journal this sync could not be rolled back: undo it.
		if undoErr := txn.undo(len(txn.changes)); undoErr != nil {
			return fail(fmt.Errorf("rollback journal: %w; restoring the client failed: %v", err, undoErr))
		}
		return fail(fmt.Errorf("rollback journal: %w (all Skills of this client restored)", err))
	}
	txn.cleanup()
	for _, it := range items {
		switch it.action {
		case syncWrite:
			*changed++
			log("skill-sync: %s %s → %s (%s)", SkillLabel(it.out.Client, it.out.Skill), orQuestion(it.out.From), target, it.out.Dir)
		case syncRemove:
			*changed++
			log("skill-sync: %s retired Skill removed (%s)", SkillLabel(it.out.Client, it.out.Skill), it.out.Dir)
		}
	}
	return outcomesOf(items)
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func outcomesOf(items []syncItem) []TargetOutcome {
	out := make([]TargetOutcome, 0, len(items))
	for _, it := range items {
		out = append(out, it.out)
	}
	return out
}

func orQuestion(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// retiredSkills is what a sync to manifest m removes: the names m retires,
// plus Skills known as pcbpilot's that m no longer ships (a downgrade or a
// pinned older release). Only names that pass isPublicSkillName appear.
func retiredSkills(m, known SkillManifest) []string {
	var out []string
	for _, name := range slices.Concat(m.Retired, known.Skills, known.Retired) {
		if isPublicSkillName(name) && !slices.Contains(m.Skills, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// planSkillSync decides, for every client × Skill of manifest m and every
// retired name, whether to leave it, (re)write it or remove it.
func planSkillSync(clients []string, m SkillManifest, retired []string, opts SyncOptions, target string) []syncItem {
	var items []syncItem
	for _, c := range clients {
		routerPresent := ClientInstalled(c)
		routerLinked := SkillLinkTarget(skillDir(c)) != ""
		for _, skill := range m.Skills {
			dir := SkillDirFor(c, skill)
			present := isDir(dir)
			it := syncItem{out: TargetOutcome{Client: c, Skill: skill, Dir: dir, From: readMarker(dir), To: target}}
			switch link := SkillLinkTarget(dir); {
			case link != "":
				// Source install: the dir is a symlink into a git checkout. Writing
				// release files through it would dirty the checkout; `git pull`
				// (pcbpilot update on a source install) is what updates it.
				it.out.Status, it.out.Linked = "linked", link
			case !present && pathExists(dir):
				// A file (or anything but a directory) already uses the name.
				it.out.Status = "conflict"
				it.out.Err = dir + " exists and is not a directory; move it away"
			case !present && routerLinked && skill != SkillName:
				// The router is a checkout link; a copied focused Skill next to it
				// would drift from that checkout.
				it.out.Status = "skipped"
				it.out.Err = "source install: link it from the checkout (scripts/install-agent-skills.py --scope design)"
			case !present && !routerPresent && !opts.CreateMissing:
				it.out.Status, it.out.Err = "skipped", "not installed"
			case present && skill != SkillName && !ownedSkillDir(dir) && !opts.Adopt:
				// Somebody else's directory under a pcbpilot name: never replace it
				// silently (the router keeps its historical unmarked installs).
				it.out.Status = "conflict"
				it.out.Err = "a directory pcbpilot did not install (no " + versionMarker + " marker) already uses this name; " +
					"move it away, or pass --adopt to replace it"
			case present && !opts.Force && it.out.From == target:
				it.out.Status = "up-to-date"
			default:
				it.action = syncWrite
				switch {
				case !present:
					it.out.Status = "created"
				case opts.Preserve:
					it.out.Status = "preserved"
				default:
					it.out.Status = "updated"
				}
			}
			items = append(items, it)
		}
		for _, name := range retired {
			dir := SkillDirFor(c, name)
			if dir == "" {
				continue
			}
			if _, err := os.Lstat(dir); err != nil {
				continue // nothing installed under the retired name
			}
			it := syncItem{out: TargetOutcome{Client: c, Skill: name, Dir: dir, From: readMarker(dir), Status: "kept"}}
			switch link := SkillLinkTarget(dir); {
			case link != "":
				it.out.Linked, it.out.Err = link, "retired Skill is a checkout link; left alone"
			case !ownedSkillDir(dir):
				it.out.Err = "retired name, but no " + versionMarker + " marker: not installed by pcbpilot; left alone"
			case opts.Preserve:
				it.out.Err = "retired Skill kept (preserve mode)"
			default:
				it.action, it.out.Status = syncRemove, "removed"
			}
			items = append(items, it)
		}
	}
	return items
}

func syncNeedsWork(items []syncItem) bool {
	for _, it := range items {
		if it.action != syncKeep {
			return true
		}
	}
	return false
}

func syncOutcomeError(res SyncResult) error {
	var failures []error
	for _, outcome := range res.Outcomes {
		if outcome.Status == "error" {
			failures = append(failures, fmt.Errorf("%s: %s", SkillLabel(outcome.Client, outcome.Skill), outcome.Err))
		}
	}
	return errors.Join(failures...)
}

// ── transaction ──────────────────────────────────────────────────────────────

// Test seams: tests replace these to inject a failure at a chosen switch,
// stage, journal copy or journal write.
var (
	skillRename  = os.Rename
	stageCopy    = copyTree
	journalCopy  = copyTree
	journalWrite = appendJournal
)

type skillChange struct {
	dir     string // destination (symlinks resolved)
	stage   string // complete new tree; "" for a removal
	backup  string // where the previous tree sits once committed
	existed bool
	applied bool
}

type skillTxn struct{ changes []*skillChange }

// stageWrite prepares a complete sibling directory of dst: the release tree
// (merged over the existing files in preserve mode) plus the marker.
func (t *skillTxn) stageWrite(src, dst string, preserve bool, version string) error {
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		dst = resolved
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".easyeda-skill-stage-*")
	if err != nil {
		return err
	}
	c := &skillChange{dir: dst, stage: stage, existed: isDir(dst)}
	t.changes = append(t.changes, c)
	if preserve && c.existed {
		if err := copyTree(dst, stage, false, false); err != nil {
			return err
		}
	}
	if err := stageCopy(src, stage, preserve, true); err != nil {
		return err
	}
	if !preserve || !c.existed {
		if err := os.WriteFile(filepath.Join(stage, versionMarker), []byte(version+"\n"), 0644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "SKILL.md")); err != nil {
		return fmt.Errorf("staged Skill has no SKILL.md: %w", err)
	}
	return os.Chmod(stage, 0755)
}

// stageRemove checks that a retired dir is still one pcbpilot owns.
func (t *skillTxn) stageRemove(dir string) error {
	if !ownedSkillDir(dir) {
		return fmt.Errorf("%s is not a pcbpilot-installed Skill (no %s marker, or a link)", dir, versionMarker)
	}
	t.changes = append(t.changes, &skillChange{dir: dir, existed: true})
	return nil
}

// commit switches every staged change; on the first failure every earlier
// switch is reversed, so the client ends exactly as it started.
func (t *skillTxn) commit() error {
	for i, c := range t.changes {
		if err := c.apply(); err != nil {
			if undoErr := t.undo(i); undoErr != nil {
				return fmt.Errorf("switch %s: %w; restoring earlier switches failed: %v", c.dir, err, undoErr)
			}
			return fmt.Errorf("switch %s: %w (all Skills of this client restored)", c.dir, err)
		}
	}
	return nil
}

func (c *skillChange) apply() error {
	if c.existed {
		backup, err := os.MkdirTemp(filepath.Dir(c.dir), ".easyeda-skill-backup-*")
		if err != nil {
			return err
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
		if err := skillRename(c.dir, backup); err != nil {
			return err
		}
		c.backup = backup
	}
	if c.stage != "" {
		if err := skillRename(c.stage, c.dir); err != nil {
			if c.backup != "" {
				if restoreErr := os.Rename(c.backup, c.dir); restoreErr != nil {
					return fmt.Errorf("%w; restore failed: %v (original preserved at %s)", err, restoreErr, c.backup)
				}
				c.backup = ""
			}
			return err
		}
	}
	c.applied = true
	return nil
}

func (t *skillTxn) undo(n int) error {
	var errs []error
	for i := n - 1; i >= 0; i-- {
		c := t.changes[i]
		if !c.applied {
			continue
		}
		if c.stage != "" {
			// Move the new tree back aside; discard() deletes it.
			if err := os.Rename(c.dir, c.stage); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if c.backup != "" {
			if err := os.Rename(c.backup, c.dir); err != nil {
				errs = append(errs, fmt.Errorf("%v (original preserved at %s)", err, c.backup))
				continue
			}
			c.backup = ""
		}
		c.applied = false
	}
	return errors.Join(errs...)
}

// discard removes every staged tree that was not switched in.
func (t *skillTxn) discard() {
	for _, c := range t.changes {
		if c.stage != "" && !c.applied {
			_ = os.RemoveAll(c.stage)
		}
	}
}

// recordJournal durably records the committed changes, with copies of the
// previous trees, before any backup is deleted. On failure it removes the
// partial copies and leaves journal.json untouched; the caller then undoes the
// switches, so a sync that cannot be rolled back is never left in place.
func (t *skillTxn) recordJournal(journal string) error {
	if journal == "" {
		return nil
	}
	if err := os.MkdirAll(journal, 0o755); err != nil {
		return err
	}
	var entries []JournalEntry
	var copies []string
	fail := func(err error) error {
		for _, c := range copies {
			_ = os.RemoveAll(c)
		}
		return err
	}
	for _, c := range t.changes {
		e := JournalEntry{Dir: c.dir}
		switch {
		case !c.existed:
			e.Action = "created"
		case c.stage == "":
			e.Action = "removed"
		default:
			e.Action = "replaced"
		}
		if c.backup != "" {
			saved, err := os.MkdirTemp(journal, "tree-*")
			if err != nil {
				return fail(err)
			}
			copies = append(copies, saved)
			if err := journalCopy(c.backup, saved, false, false); err != nil {
				return fail(fmt.Errorf("save previous %s: %w", c.dir, err))
			}
			e.Saved = filepath.Base(saved)
		}
		entries = append(entries, e)
	}
	if err := journalWrite(journal, entries); err != nil {
		return fail(fmt.Errorf("write journal: %w", err))
	}
	return nil
}

// cleanup deletes the backups next to the destinations once the transaction
// (and its journal) is final.
func (t *skillTxn) cleanup() {
	for _, c := range t.changes {
		if c.backup != "" {
			_ = os.RemoveAll(c.backup)
			c.backup = ""
		}
	}
}
