# Clean-room process for the pcbpilot router rewrite (phase 2)

> Status: binding process document, 2026-10-06. Applies to every person and every agent that writes, reviews or
> merges code under `pkg/pcbroute/**`, `pkg/routerbench/**`, `cmd/routerbench/**`, or the specs under
> `docs/router-cleanroom/specs/**`.
>
> Why this exists: pcbpilot is MIT-licensed. The routers we measure ourselves against are GPLv3 (fastroute,
> Freerouting, KiCad's interactive router) or have unclear terms (easyeda-pcb-router). If code, or a close
> paraphrase of that code, reaches pcbpilot, pcbpilot could become a derivative work under GPLv3. Copyright
> protects how something is expressed, not the idea behind it [S1][S2]. So we keep ideas, which we may take from
> papers and public user documentation, apart from expression, which we must never see. The written specs are the
> only thing that passes between the two. This document describes engineering practice. It is not legal advice;
> see open question Q-L1 in PLAN.md.

---

## 1. Roles

| Role | Who (phase 2) | May read | Must not read |
|---|---|---|---|
| **Clean-room officer (CRO)** | the orchestrating lead session plus the human owner (tzhuang) | everything an implementer may read, plus the exposure log | forbidden sources (§2) |
| **Spec writer** | the phase-1 spec agents, and any later agent that edits `specs/*.md` | academic papers, textbooks, standards, Wikipedia, patents, the Specctra DSN/SES language reference, **user-facing** docs of third-party routers (README, docs/*.md that describe features, user manuals), black-box behaviour of the fastroute binary (§4), pcbpilot's own repository | forbidden sources (§2) |
| **Implementer** | one agent per milestone in PLAN.md | the specs in `docs/router-cleanroom/specs/`, `PLAN.md`, this file, `gap-analysis.md`, pcbpilot's own repository (MIT), papers, textbooks and standards **cited in a spec**, Go standard-library and general Go documentation | forbidden sources (§2), **and** third-party router user documentation (fastroute README/IMPROVEMENTS, Freerouting manual, KiCad router manual). Implementers get those ideas through the specs only |
| **Bench operator** | the M5 implementer, then whoever runs `make router-bench` | as implementer, plus the fastroute binary's `--help` text, its JSON report and its SES output (§4) | fastroute source, disassembly, decompiled code, symbol or string dumps |
| **Reviewer** | a different agent from the implementer, at the same clearance as an implementer | as implementer | as implementer |

Rules that apply to every role:

1. **Nobody reads GPL router source.** There is no "dirty room" in this project. Spec writers are clean too. They
   only read descriptive material, which is a stricter rule than the classic two-team clean room [S3].
2. **Specs are the only bridge.** An idea from a third-party router enters the code base only as text in a spec,
   written in our own words, with the source cited. Implementers do not open the cited third-party user docs
   themselves. If a spec is unclear, they ask (§5).
3. **No verbatim copying of descriptive text either.** Specs may quote one short phrase with attribution. Anything
   longer is paraphrased.
4. **pcbpilot's own MIT code may be reused freely.** This includes `pkg/pcbauto`, `internal/pcb/specctra`,
   `pkg/intent` and `internal/app/pcb_route_gates.go`. There is one exception: `internal/app/cmd_pcb_fastroute.go`
   may be read only to learn how the binary is invoked and how its report is parsed.

## 2. Forbidden sources

### 2.1 Local paths (never open, `cat`, `grep`, `rg`, `find -exec`, index, or attach to an agent context)

| Path | Content |
|---|---|
| `/Users/taisen/Github_Working/_ext/` (entire tree, every sub-directory) | third-party checkouts, including router sources |
| `/Users/taisen/github/easyeda-upstream/` (entire tree) | easyeda-pcb-router and related upstream code |
| any local clone, vendored copy, tarball, `~/.cargo/registry`, `~/.m2`, Go module cache or IDE index entry of fastroute, Freerouting, KiCad (`pcbnew/router/**` and the rest of the tree), easyeda-pcb-router | same code, other location |
| the fastroute binary and the Freerouting `.jar` **as code** | no disassembly, decompilation, `strings`/`nm`/`objdump`, debugger stepping or memory inspection |

Tool hygiene: workspace-wide searches must run from `/Users/taisen/Github_Working/pcbpilot` with explicit paths.
Never search from `/Users/taisen/Github_Working` or `~`, because a recursive search there would enter `_ext/`.
Agents must not add the forbidden directories to `--add-dir`, MCP file roots or editor workspaces.

### 2.2 Online

- Any source file, `blob`/`raw`/`tree` view, diff, commit, pull-request code tab or release source archive of:
  `github.com/parisxmas/fastroute` (except `README.md` and the `docs/*.md` prose, which spec writers only may
  read), `github.com/freerouting/freerouting` (except the user manual and the site at freerouting.org),
  `gitlab.com/kicad/code/kicad` and its mirrors (the KiCad *documentation* repository `kicad-doc` is allowed for
  spec writers), and every easyeda-pcb-router repository and mirror.
- Forks, mirrors and code-search engines that show the same code (Sourcegraph, grep.app, searchcode, GitHub code
  search) and Q&A posts or blog posts that paste their code.
- LLM prompts that ask for the code or internal structure of these projects ("how does Freerouting's
  `X` class work", "show fastroute's shove function"). Ask for the **published algorithm** by its paper instead.

### 2.3 Agent memory and injected context

Persistent memory, such as claude-mem observations, `MEMORY.md` and earlier session transcripts, may contain notes
from sessions that worked inside `_ext/`. Implementer and reviewer agents must:

- ignore any injected memory entry that mentions files, classes, functions or line numbers of a forbidden project;
- not run memory or transcript search tools with router-internal queries;
- report such an entry to the CRO if it appeared in their context. The CRO then decides under §7.

## 3. Allowed sources

Papers, textbooks, standards (IPC-2221B, IPC-2152), Wikipedia, patents, the Cadence *SPECCTRA Design Language
Reference* (format description), vendor and fab application notes, and the sources each spec lists in its
"Sources" section. For implementers this is limited to sources **cited in a spec**. If an implementer needs a new
source, they ask the CRO, who adds it to the spec first (§5).

## 4. Black-box comparison with the fastroute binary

Allowed, and required by the benchmark (`gap-analysis.md` §4):

- running `fastroute` (installed by `scripts/install-fastroute.sh`, path in `FASTROUTE_BIN`) on DSN files we
  produce, with any documented command-line option;
- reading its `--help` output, its JSON report and its SES output **to compute metrics** with pcbpilot's own
  referee (completion, DRC, vias, wire length, runtime);
- using fastroute's output as the *input* of our optimizer in a bench row ("optimize fastroute's result"), to
  measure the optimizer.

Not allowed:

- committing fastroute output (SES, reports beyond the metric rows) to the repository. Bench artefacts live under
  `out/router-bench/` (git-ignored). Only metric rows (`results.jsonl`, `baseline.jsonl`) are committed;
- using fastroute geometry as a **golden expected result** in unit tests, or tuning our code by copying its
  geometric choices (for example "place the via exactly where fastroute does");
- anything listed under §2.1 for the binary.

Background: the GPL FAQ states that a program's output is generally not covered by the program's licence unless
the output contains parts of the program [S4]. We still keep fastroute output out of the repository, because the
extra safety costs us nothing.

## 5. Specs as the only bridge: change procedure

1. An implementer who finds a spec gap, contradiction or ambiguity writes a question in the milestone PR
   description or a `docs/router-cleanroom/questions/<milestone>.md` note. The note says what is unclear and which
   spec section it concerns. It does not propose code taken from anywhere.
2. A spec writer (or the CRO acting as spec writer) answers by **editing the spec**. The edit gets a dated
   changelog line at the end of the spec and a citation for any new source. The PLAN.md "decisions" table (§2.6)
   records cross-spec decisions.
3. The implementer implements from the edited spec and cites the new section.
4. An implementer's own design choices are allowed and expected, because the specs leave freedom. They are
   written down in the package `doc.go` or the PR description, never attributed to a third-party router.

## 6. Provenance per commit

Every commit that touches `pkg/pcbroute/**`, `pkg/routerbench/**` or `cmd/routerbench/**` carries trailers:

```
Clean-room: spec docs/router-cleanroom/specs/01-core-search.md §3.3-§3.4
Clean-room-sources: Hart-Nilsson-Raphael 1968; Ousterhout 1984
Clean-room-attest: author has not viewed forbidden sources (CLEANROOM.md §2)
Co-Authored-By: ...
```

- `Clean-room:` names one or more spec files and sections (or `PLAN.md §x` / `none (infrastructure)` for build,
  bench and test plumbing).
- `Clean-room-sources:` lists external academic or standard sources used directly. Use `-` when the spec alone
  was enough.
- `Clean-room-attest:` is mandatory. An agent that cannot truthfully attest must not commit (see §7).
- Spec edits use `Clean-room: spec-edit <file> §x` and list every new source.

M0 adds `scripts/cleanroom-check.sh` (run in CI and by reviewers). For the commit range, it checks that:

1. every commit touching the guarded paths has the three trailers;
2. guarded files contain no GPL licence headers ("GNU General Public License", "SPDX-License-Identifier: GPL")
   and no `import` of a non-MIT-compatible module (`go list -m -json all` licence allowlist);
3. guarded code has no identifiers or comments that name forbidden projects' internal classes, files or
   functions. The check is a deny-list of the project names in code comments, except in `pkg/routerbench`, which
   legitimately says "fastroute". It is a tripwire, not proof;
4. bulk drops are flagged: a commit that adds more than 1 000 lines to guarded paths at once is marked for
   manual CRO review (the script never opens anything outside the pcbpilot repository).

## 7. Review checklist (reviewer and CRO, per PR)

- [ ] Every commit has the three trailers; the cited spec sections exist and match what the code does.
- [ ] The PR description lists design choices that are not in the spec, with a reason.
- [ ] No forbidden-project names in code or comments outside `pkg/routerbench` (bench rows may name fastroute).
- [ ] No identifiers, comments or structure that point to a source other than the cited spec or paper. Example:
      constants with unexplained "magic" values that are not in the spec. Ask where each constant comes from.
- [ ] Algorithms follow the cited paper or spec pseudo-code. Where the code differs, the PR says why.
- [ ] No fastroute output committed; no unit test compares against fastroute geometry.
- [ ] Tests from the spec's "Test scenarios" section exist and pass; bench acceptance from PLAN.md is shown with
      the `results.jsonl` row hashes.
- [ ] `scripts/cleanroom-check.sh` passes.
- [ ] Determinism: the PR runs the package tests with `GOMAXPROCS=1` and `GOMAXPROCS=8` and the outputs are equal
      where the spec requires it.
- [ ] Imports: only the standard library, pcbpilot's own packages, and modules on the MIT/BSD/Apache-2.0 allowlist.

## 8. If someone was exposed to forbidden code

"Exposed" means the person or agent read any part of a forbidden source (§2), however briefly, including through
injected memory or a pasted snippet.

1. **Stop and report.** Stop working on guarded paths at once. Write an entry in
   `docs/router-cleanroom/EXPOSURE-LOG.md` with the date, the agent or person, what was seen (project and
   area, **not** the content), how long, and which milestones and commits they touched since.
2. **Quarantine.** The CRO labels the agent's open PRs `cleanroom-hold`. Commits by that author after the exposure
   are reverted from `dev` unless the CRO records that they cannot be affected, for example pure bench plumbing
   or test data.
3. **Reassign.** An unexposed implementer re-implements the reverted work from the spec. The exposed author may
   not be consulted about that code. They may still work outside the guarded paths, for example on CLI wiring,
   documentation that is not a spec, or EasyEDA connector work.
4. **Spec check.** If the exposed party was a spec writer, the CRO re-reviews every spec section they wrote after
   the exposure. A different spec writer rewrites any section whose wording or structure might reflect the
   exposure.
5. **Model-memory caveat.** LLM agents may reproduce code they saw in training. This cannot be logged as an
   exposure. PLAN.md (risk R7) handles it with an optional similarity audit run by a separate, non-implementing
   auditor.
6. **Pre-existing exposure.** Forbidden checkouts already exist on this machine (§2.1). Before starting, every
   phase-2 agent states in its first PR that it has not opened them. The human owner records in the exposure log
   whether any earlier session that will take part in phase 2 did open them. Until that record exists, those
   sessions act only as CRO or bench operator, never as implementer (PLAN.md Q-L2).

## 9. Sources

- [S1] U.S. Copyright Office, Circular 33, *Works Not Protected by Copyright* (ideas, procedures, methods and
  systems are not protected). https://www.copyright.gov/circs/circ33.pdf
- [S2] *Baker v. Selden*, 101 U.S. 99 (1879), the idea/expression distinction, summarised at
  https://en.wikipedia.org/wiki/Baker_v._Selden
- [S3] Wikipedia, "Clean-room design" (two-team method; specification as the only channel).
  https://en.wikipedia.org/wiki/Clean-room_design
- [S4] Free Software Foundation, *Frequently Asked Questions about the GNU Licenses*, entry "Is there some way that
  I can GPL the output people get from use of my program?". https://www.gnu.org/licenses/gpl-faq.html#GPLOutput
- [S5] GNU General Public License v3. https://www.gnu.org/licenses/gpl-3.0.html
- pcbpilot `docs/router-cleanroom/gap-analysis.md` §4 (benchmark protocol, black-box use of fastroute).
