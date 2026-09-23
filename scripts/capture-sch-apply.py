#!/usr/bin/env python3
"""Add official image exports to a guarded SCH Apply queue, without executing it."""

import argparse
import hashlib
import json
from pathlib import Path
import shlex
import struct
import subprocess
import sys


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def reject_constant(value):
    raise ValueError(f"non-JSON number: {value}")


def validate_single_page(queue, steps):
    """Exports inherit the Apply target; reject steps with a different target path."""
    meta = queue.get("meta")
    if not isinstance(meta, dict) or any(
        not isinstance(meta.get(key), str) or not meta[key].strip() or "${" in meta[key]
        for key in ("project", "doc")
    ):
        raise ValueError("capture requires explicit meta.project and meta.doc for one page")
    target_flags = {"project", "doc", "window", "page", "schematic", "host", "ports"}
    target_payload = {"projectUuid", "documentUuid", "windowId", "pageUuid", "schematicPageUuid", "schematicUuid"}
    navigation_actions = {
        "document.open", "schematic.page.open", "schematic.page.create",
        "schematic.page.rename", "schematic.page.delete",
    }
    navigation_commands = {
        ("doc", "open"), ("doc", "switch"), ("doc", "reload"),
        ("sch", "open"), ("sch", "page-new"), ("sch", "page-rename"), ("sch", "page-delete"),
    }

    def check(step, ref):
        if step.get("action") in navigation_actions:
            raise ValueError(f"{ref}: page navigation/management is outside single-page capture")
        payload = step.get("payload") or {}
        flags = step.get("flags") or {}
        if not isinstance(payload, dict) or not isinstance(flags, dict):
            raise ValueError(f"{ref}: payload and flags must be objects")
        if target_payload.intersection(payload) or target_flags.intersection(flags):
            raise ValueError(f"{ref}: step-level target overrides are not supported for capture")
        # Apply uses strings.Fields(run), appends args, then expands flags; it does not run a shell.
        run, args = step.get("run", ""), step.get("args", [])
        if not isinstance(run, str) or not isinstance(args, list) or any(not isinstance(a, str) for a in args):
            raise ValueError(f"{ref}: run must be a string and args must be strings")
        argv = run.split() + args
        if any(token.startswith("--") and token[2:].split("=", 1)[0] in target_flags for token in argv):
            raise ValueError(f"{ref}: target overrides in run/args are not supported for capture")
        command = tuple(token for token in argv if not token.startswith("-"))[:2]
        if command in navigation_commands:
            raise ValueError(f"{ref}: page navigation is outside single-page capture")
        # These generic entry points can hide navigation in scripts or another playbook.
        if step.get("action") == "debug.exec_js" or (command and command[0] in {"debug", "call"}) or command == ("sch", "apply"):
            raise ValueError(f"{ref}: scripts and nested queues are outside single-page capture")
        if "verify" in step:
            if not isinstance(step["verify"], dict):
                raise ValueError(f"{ref}: verify must be an object")
            check(step["verify"], ref + ".verify")

    for index, step in enumerate(steps):
        check(step, step.get("id") or f"s{index + 1}")


def capture_points(steps, budget):
    """Choose actual action boundaries; never capture place before its orient step."""
    phases = {}

    def add(phase, index):
        phases.setdefault(phase, []).append(index)

    for index, step in enumerate(steps):
        action, run = step.get("action"), step.get("run")
        following = steps[index + 1] if index + 1 < len(steps) else {}
        if action == "schematic.component.modify":
            add("placed-components", index)
        elif action == "schematic.component.place":
            if following.get("action") != "schematic.component.modify":
                add("placed-components", index)
        elif action == "schematic.pin.set_no_connect":
            add("nc-markers", index)
        elif action == "schematic.wire.create":
            add("direct-wires", index)
        elif action == "schematic.power.connect_pin":
            add("net-markers", index)
        elif run == "sch frame apply":
            # Prefer the immediately following frame readback when present.
            add("module-frames", index + 1 if following.get("run") == "sch frame check" else index)

    if not phases:
        raise ValueError("no supported schematic drawing stages found in the queue")
    candidates = {index: phase for phase, indices in phases.items() for index in indices}
    # Always preserve each stage's endpoint, then spread extra captures over the queue.
    selected = {indices[-1] for indices in phases.values()}
    if "placed-components" in phases:
        selected.add(phases["placed-components"][0])
    while len(selected) < min(budget, len(candidates)):
        remaining = set(candidates) - selected
        selected.add(max(remaining, key=lambda i: (min(abs(i - j) for j in selected), -i)))
    return [(index, candidates[index]) for index in sorted(selected)]


def concat_quote(path):
    return "'" + str(path).replace("'", "'\\''") + "'"


def ffmpeg_command(concat_path, gif_path):
    return [
        "ffmpeg", "-n", "-f", "concat", "-safe", "0", "-i", str(concat_path),
        "-filter_complex",
        "scale=1440:-2:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse=dither=sierra2_4a:diff_mode=rectangle",
        "-fps_mode", "vfr", "-final_delay", "4", "-loop", "0", str(gif_path),
    ]


def concat_text(frames):
    concat = ["ffconcat version 1.0"]
    for index, frame in enumerate(frames):
        duration = 2.5 if index == len(frames) - 1 else 1.2 if index == 0 else 0.5
        concat.extend(["file " + concat_quote(frame["path"]), f"duration {duration}"])
    # Concat demuxer needs the last file repeated to honor its hold duration.
    concat.append("file " + concat_quote(frames[-1]["path"]))
    return "\n".join(concat) + "\n"


def encode(manifest_path):
    """Encode only a complete capture set, without contacting EDA."""
    manifest = json.loads(manifest_path.read_bytes(), object_pairs_hook=unique_object)
    frames = manifest.get("frames")
    if not isinstance(frames, list) or not frames:
        raise ValueError("capture manifest must contain frames")
    sizes = set()
    for frame in frames:
        path = Path(frame["path"])
        if not path.is_absolute() or "\n" in str(path) or "\r" in str(path):
            raise ValueError("capture paths must be absolute and contain no newlines")
        with path.open("rb") as capture:
            header = capture.read(24)
        if len(header) != 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
            raise ValueError(f"missing or invalid PNG capture: {path}")
        sizes.add(struct.unpack(">II", header[16:24]))
    if len(sizes) != 1 or 0 in next(iter(sizes)):
        raise ValueError("capture canvas sizes differ; inspect the official full-page exports before encoding")
    concat_path = manifest_path.parent / "frames.ffconcat"
    gif_path = manifest_path.parent / "schematic-apply.gif"
    if gif_path.exists():
        raise ValueError(f"GIF already exists: {gif_path}")
    concat_path.write_text(concat_text(frames), encoding="utf-8")
    command = ffmpeg_command(concat_path, gif_path)
    print(f"All {len(frames)} PNG captures exist on the same canvas; encoding accelerated stages.", flush=True)
    subprocess.run(command, check=True)
    print(gif_path)


def prepare(source, out_dir, budget):
    raw = source.read_bytes()
    queue = json.loads(raw, object_pairs_hook=unique_object, parse_constant=reject_constant)
    if not isinstance(queue, dict) or queue.get("version") != 1:
        raise ValueError("input must be a version 1 SCH Apply playbook object")
    steps = queue.get("steps")
    if not isinstance(steps, list) or not steps or any(not isinstance(s, dict) for s in steps):
        raise ValueError("input must contain a nonempty steps array of objects")
    if not queue.get("requireFullExecution") and not any("expectedConnectivity" in s for s in steps):
        raise ValueError("input must retain a full-execution or expectedConnectivity guard")
    validate_single_page(queue, steps)
    if any(str(s.get("id", "")).startswith("showcase-capture-") for s in steps):
        raise ValueError("input is already instrumented; use the original generated queue")
    if any("\n" in str(p) or "\r" in str(p) or "${" in str(p) for p in (source, out_dir)):
        raise ValueError("paths must not contain newlines or Apply variable expressions")

    output = out_dir / "capture-apply.json"
    manifest_path = out_dir / "capture-manifest.json"
    concat_path = out_dir / "frames.ffconcat"
    gif_path = out_dir / "schematic-apply.gif"
    if any(path.exists() for path in (output, manifest_path, concat_path, gif_path)) or list(out_dir.glob("frame-*.png")):
        raise ValueError("capture outputs already exist; use a fresh --out-dir to avoid stale frames")
    points = capture_points(steps, budget)
    chosen = dict(points)
    instrumented = []
    frames = []
    for index, step in enumerate(steps):
        instrumented.append(step)
        if index not in chosen:
            continue
        number = len(frames) + 1
        capture_id = f"showcase-capture-{number:03d}"
        path = out_dir / f"frame-{number:03d}.png"
        instrumented.append({
            "id": capture_id,
            "name": f"Export real Apply stage: {chosen[index]}",
            "run": "sch export-image",
            "flags": {"format": "png", "scope": "page", "out": str(path)},
            "retry": 0,
            "continueOnError": True,
        })
        frames.append({
            "path": str(path),
            "afterStep": step.get("id") or str(index + 1),
            "afterStepIndex": index + 1,
            "captureStep": capture_id,
            "phase": chosen[index],
        })
    result = dict(queue, steps=instrumented)
    assert [s for s in instrumented if not str(s.get("id", "")).startswith("showcase-capture-")] == steps
    assert {k: v for k, v in result.items() if k != "steps"} == {k: v for k, v in queue.items() if k != "steps"}

    ffmpeg = ffmpeg_command(concat_path, gif_path)
    manifest = {
        "version": 1,
        "sourcePlaybook": str(source),
        "sourceSha256": hashlib.sha256(raw).hexdigest(),
        "capturePlaybook": str(output),
        "originalStepCount": len(steps),
        "captureCount": len(frames),
        "frames": frames,
        "playback": {"mode": "accelerated-stage-sequence", "stepSeconds": 0.5, "firstSeconds": 1.2, "lastSeconds": 2.5},
        "ffmpegCommand": ffmpeg,
        "notice": "Frames are real official exports at selected Apply stages. Playback timing is edited, not wall-clock time. A final image does not prove validation passed; retain the Apply journal and DRC results.",
    }
    out_dir.mkdir(parents=True, exist_ok=True)
    for path, data in ((output, result), (manifest_path, manifest)):
        path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    concat_path.write_text(concat_text(frames), encoding="utf-8")
    print(f"Prepared {len(frames)} real-stage capture slots; no EDA action was executed.")
    print(f"Manifest: {manifest_path}")
    print(shlex.join(["pcbpilot", "sch", "apply", str(output), "--dry-run"]))
    print(shlex.join(["pcbpilot", "sch", "apply", str(output), "--yes"]))
    print("After execution and journal inspection, validate all captures and encode locally:")
    # sys.executable, not "python3": the printed line is meant to be pasted back,
    # and Windows normally has no python3 on PATH (the copy would exit 9009).
    print(shlex.join([sys.executable, str(Path(__file__).resolve()), "--gif", str(manifest_path)]))
    print("Equivalent ffmpeg command (after capture validation):")
    print(shlex.join(ffmpeg))
    print("Do not bypass a failed guard for recording. A final frame is not an acceptance result.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("playbook", nargs="?", type=Path, help="original complete guarded SCH Apply JSON")
    parser.add_argument("--out-dir", type=Path, help="fresh directory for instrumented queue, manifest and captures")
    parser.add_argument("--gif", type=Path, metavar="MANIFEST", help="validate every existing PNG and encode a GIF locally; does not call EDA")
    parser.add_argument("--frames", type=int, default=12, choices=range(6, 21), metavar="6..20", help="maximum frame count (default: 12; fewer if stages are sparse)")
    args = parser.parse_args()
    if args.gif and (args.playbook or args.out_dir):
        parser.error("--gif cannot be combined with a playbook or --out-dir")
    if not args.gif and (not args.playbook or not args.out_dir):
        parser.error("provide a playbook and --out-dir, or --gif MANIFEST")
    try:
        if args.gif:
            encode(args.gif.resolve())
        else:
            prepare(args.playbook.resolve(), args.out_dir.resolve(), args.frames)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(f"capture-sch-apply: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
