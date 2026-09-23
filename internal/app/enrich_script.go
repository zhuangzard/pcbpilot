package app

// enrich_script.go — locating bom-enrich.py, cwd-independently (issue #115).
//
// `bom export` enriches by default, so the script must be findable from
// WHEREVER the agent happens to run the CLI (a project dir, /tmp, $HOME). The
// old resolver only reached the repo checkout by walking up from cwd / the
// binary, so a run outside the repo died with a bare "bom-enrich.py not found".
// The installed SKILL dir — the copy every non-repo user actually has, kept
// current by `pcbpilot skill sync` — was never probed.
//
// The search ladder itself now lives in skill_asset.go: standard-parts.json
// (which `sch block-apply` needs) has the same cwd-independence problem, so the
// ladder is shared rather than copied per asset.

// enrichScriptName is the script this resolver hunts for.
const enrichScriptName = "bom-enrich.py"

// enrichScriptAsset describes bom-enrich.py's location in a skills tree. The
// second rel keeps pre-merge installs working. searchPath is on because, unlike
// a JSON data file, a script legitimately can live on $PATH.
var enrichScriptAsset = skillAsset{
	name: enrichScriptName,
	rels: []string{
		"pcbpilot/scripts/" + enrichScriptName,
		"easyeda-schematic/scripts/" + enrichScriptName, // pre-merge skill name
	},
	searchPath: true,
	flagHint:   "--script",
}

// resolveEnrichScript resolves bom-enrich.py through the shared skill-asset
// ladder (explicit → $PCBPILOT_SKILLS_DIR → installed skill dirs → up from the
// binary → up from cwd → $PATH). See skillAsset.resolve for the full contract.
func resolveEnrichScript(explicit string) (string, error) {
	return enrichScriptAsset.resolve(explicit)
}
