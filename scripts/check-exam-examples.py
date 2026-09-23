#!/usr/bin/env python3
"""Check the 260919 source examples and recorded live evidence.

The checker keeps source transcription, offline checks, representative live
verification, and unfinished work separate.  It validates the summaries that
make an example teachable; it does not infer completion of untested PCB work.
"""

from __future__ import annotations

import json
import math
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EXAMPLE = ROOT / ".agents/skills/pcbpilot/references/examples/260919-at32f415"


def load(name: str) -> dict:
    with (EXAMPLE / name).open(encoding="utf-8") as stream:
        return json.load(stream)


def pin_map(connectivity: dict, reference: str) -> dict[str, dict]:
    component = next(c for c in connectivity["components"] if c["reference"] == reference)
    return {pin["terminal"]: pin for pin in component["pins"]}


def require_net(connectivity: dict, reference: str, terminal: str, net: str) -> None:
    actual = pin_map(connectivity, reference)[terminal].get("net")
    assert actual == net, f"{reference}.{terminal}: {actual!r} != {net!r}"


def require_nc(connectivity: dict, reference: str, terminal: str) -> None:
    actual = pin_map(connectivity, reference)[terminal].get("state")
    assert actual == "nc", f"{reference}.{terminal}: expected NC, got {actual!r}"


def close(actual: float, expected: float, *, tolerance: float = 0.01) -> bool:
    return math.isclose(actual, expected, rel_tol=0, abs_tol=tolerance)


def strings(value: object) -> list[str]:
    """Flatten JSON strings for execution-policy checks."""
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [text for item in value for text in strings(item)]
    if isinstance(value, dict):
        return [text for item in value.values() for text in strings(item)]
    return []


def main() -> None:
    manifest = load("source-manifest.json")
    bom = load("bom-instances.json")
    connectivity = load("source-connectivity.json")
    catalog = load("example-catalog.json")
    placement = load("initial-placement.json")
    live = load("live-validation.json")
    crystal_placement = load("crystal-placement-live.json")
    crystal_route = load("crystal-route-live.json")
    crystal_guard = load("crystal-guard-requirement.json")
    can_placement = load("can-placement-iteration-live.json")
    can_route = load("can-route-live.json")
    usb_route = load("usb-route-live.json")
    can_route_negative = load("can-route-plan-negative.json")
    can_route_report = load("can-route-plan-negative-report.json")
    can_pair_negative = load("can-route-plan-pair-negative.json")
    can_pair_report = load("can-route-plan-pair-negative-report.json")
    can_layout_candidate = load("layout-candidates-can-current-board/candidate-01.json")
    browser_reopen = load("layout-after-browser-reopen-verification-live.json")
    u3_binding = load("u3-existing-model-binding-live.json")
    ldo_candidate = load("ldo-layout-candidate.json")
    ldo_route = load("ldo-route-live.json")

    refs = [item["reference"] for item in bom["instances"]]
    assert bom["counts"] == {"bomRows": 27, "instances": 69, "uniqueReferences": 69}
    assert len(refs) == len(set(refs)) == 69
    assert sum(row["quantity"] for row in bom["rows"]) == 69
    for row in bom["rows"]:
        assert row["quantity"] == len(row["references"]), row

    zone_refs = [ref for zone in manifest["functionalZones"] for ref in zone["refs"]]
    assert len(zone_refs) == len(set(zone_refs)) == 69
    assert set(zone_refs) == set(refs)
    assert all(item["functionalZone"] for item in bom["instances"])

    zone_by_ref = {
        ref: zone["id"]
        for zone in manifest["functionalZones"]
        for ref in zone["refs"]
    }
    bom_by_ref = {item["reference"]: item for item in bom["instances"]}
    for ref, item in bom_by_ref.items():
        assert item["functionalZone"] == zone_by_ref[ref], ref

    conn_refs = [item["reference"] for item in connectivity["components"]]
    assert connectivity["status"] == "source-only"
    assert len(conn_refs) == len(set(conn_refs)) == 69
    assert set(conn_refs) == set(refs)
    defined_nets = set(connectivity["netDefinitions"])
    used_nets: set[str] = set()
    pin_count = 0
    for component in connectivity["components"]:
        reference = component["reference"]
        source = bom_by_ref[reference]
        assert component["functionalZone"] == zone_by_ref[reference], reference
        assert component["value"] == source["value"], reference
        assert component["footprint"] == source["footprint"], reference
        assert isinstance(component["pinNumbersFromSource"], bool), reference
        terminals: set[str] = set()
        for pin in component["pins"]:
            pin_count += 1
            terminal = pin["terminal"]
            assert terminal not in terminals, f"{reference}: duplicate terminal {terminal}"
            terminals.add(terminal)
            has_net = "net" in pin
            has_state = "state" in pin
            assert has_net != has_state, f"{reference}.{terminal}: need exactly one of net/state"
            if has_net:
                used_nets.add(pin["net"])
            else:
                assert pin["state"] == "nc", f"{reference}.{terminal}: unsupported state"
    assert pin_count == 233
    assert len(defined_nets) == 46
    assert used_nets == defined_nets
    explicit_nc = sum(
        pin.get("state") == "nc"
        for component in connectivity["components"]
        for pin in component["pins"]
    )
    assert connectivity["counts"]["explicitNC"] == explicit_nc == 13

    # High-risk transcription points named by the exam source review.
    for terminal in ("2", "4"):
        require_net(connectivity, "U2", terminal, "+3V3")
    for terminal in ("5", "8"):
        require_net(connectivity, "U4", terminal, "+3V3")
    require_nc(connectivity, "U4", "4")
    for terminal in ("B6", "A6"):
        require_net(connectivity, "USB1", terminal, "USB_D+")
    for terminal in ("A7", "B7"):
        require_net(connectivity, "USB1", terminal, "USB_D-")
    for terminal in ("A8", "B8"):
        require_nc(connectivity, "USB1", terminal)
    require_nc(connectivity, "BUZZER1", "NC")
    for terminal in ("1", "2", "9"):
        require_nc(connectivity, "U3", terminal)
    require_nc(connectivity, "U5", "5")
    for terminal in ("10", "11", "12", "13"):
        require_net(connectivity, "CARD1", terminal, "GND")
    for ref in ("SCREW1", "SCREW2", "SCREW3", "SCREW4"):
        require_nc(connectivity, ref, "1")

    fixed = {item["ref"]: item for item in manifest["fixedPlacementsMm"]}
    expected = {
        "SCREW1": (3, 47, 0), "SCREW2": (87, 47, 0),
        "SCREW3": (3, 3, 0), "SCREW4": (87, 3, 0),
        "U6": (45, 25, 0), "CARD1": (79.5, 25, 90),
    }
    for ref, (x, y, rotation) in expected.items():
        assert (fixed[ref]["x"], fixed[ref]["y"], fixed[ref]["rotationDeg"]) == (x, y, rotation)
        assert fixed[ref]["locked"] is True
    assert fixed["CN1"]["x"] is None and fixed["CN1"]["y"] == 42
    assert fixed["CN1"]["rotationDeg"] == 180 and fixed["CN1"]["freeAxis"] == "x"

    # Same-value, same-net capacitors still have distinct physical ownership.
    # Keep these mappings machine-readable so proximity heuristics cannot silently
    # swap them (the first independent placement review did exactly that for C1/C2).
    assert manifest["decouplingOwnership"] == {
        "C1": "U1.5 VCC",
        "C2": "LED1.1 VDD",
        "C3": "U2.3 VIN input bulk",
        "C4": "U2.3 VIN input high-frequency",
        "C5": "U2.2/U2.4 VOUT output bulk",
        "C6": "U2.2/U2.4 VOUT output high-frequency",
        "C9": "U4.8 V3",
        "C10": "U4.5 VCC",
        "C12": "U5.3 VCC bulk",
        "C13": "U5.3 VCC high-frequency",
        "C14": "U6.1 VDD",
        "C15": "U6.5 VDDA",
        "C16": "U6.17 VDD",
        "C18": "CARD1.4 VDD bulk",
        "C19": "CARD1.4 VDD high-frequency",
    }

    assert ldo_candidate["status"] == "live-verified"
    assert ldo_candidate["coordinateSemantic"] == "rendered-bbox-center"
    assert ldo_candidate["units"] == "mil"
    assert ldo_candidate["fixedCore"] == {
        "ref": "U2", "xMil": 1950, "yMil": 400, "rotationDeg": 180,
    }
    ldo_placements = {item["ref"]: item for item in ldo_candidate["placements"]}
    assert set(ldo_placements) == {"C3", "C4", "C5", "C6"}
    assert all(item["rotationDeg"] == 90 for item in ldo_placements.values())
    assert all("actualPads" in item for item in ldo_placements.values())
    assert ldo_candidate["routeIntent"] == {
        "inputPower": "source -> C3.1 -> C4.1 -> U2.3",
        "outputPower": "U2.2/U2.4 continuous copper -> C5.1 -> C6.1 -> load",
        "inputGround": "C3.2 -> C4.2 -> top-layer return corridor -> U2.1",
        "outputGround": "C5.2 -> C6.2 -> top-layer return corridor -> U2.1",
        "preferredLayer": "TOP",
        "preferredViaCount": 0,
        "powerWidthMil": 20,
        "minimumAllowedPowerWidthMil": 8,
        "turns": "straight or 45-degree; derive endpoints from fresh pad boundaries",
    }
    assert ldo_route["status"] == "live-verified"
    assert ldo_route["documentUuid"] == "2e719e9419653c72"
    assert ldo_route["rulesUsed"] == {
        "layer": 1,
        "layerName": "TOP",
        "trackWidthMil": 20,
        "clearanceMil": 6,
        "viaCount": 0,
    }
    assert len(ldo_route["segments"]) == 15
    assert {item["net"] for item in ldo_route["segments"]} == {"+5V", "+3V3", "GND"}
    assert len({item["primitiveId"] for item in ldo_route["segments"]}) == 15
    assert [item["orderedPads"] for item in ldo_route["semanticPaths"]] == [
        ["C3.1", "C4.1", "U2.3"],
        ["U2.2", "U2.4", "C5.1", "C6.1"],
        ["C3.2", "C4.2", "U2.1"],
        ["C5.2", "C6.2", "U2.1"],
    ]
    route_readback = ldo_route["postReloadReadback"]
    assert route_readback["trackCount"] == 15
    assert route_readback["allTop"] is True
    assert route_readback["allWidth20Mil"] is True
    assert route_readback["viaCount"] == 0
    for field in (
        "duplicateSegmentCount", "danglingEndCount", "acuteAngleCount", "nonOrthogonalCount",
        "trackOverForeignPadCount", "clearanceFindingCount",
    ):
        assert route_readback[field] == 0, field
    assert ldo_route["pathEvidence"]["allTargetPathsFound"] is True
    assert ldo_route["independentVerification"]["status"] == "completed-with-findings"
    assert ldo_route["independentVerification"]["findings"]
    assert all(len(value) == 64 for value in ldo_route["evidence"].values())
    assert ldo_route["notClaimed"]

    assert crystal_placement["status"] == "live-verified"
    assert crystal_placement["documentUuid"] == "2e719e9419653c72"
    assert crystal_placement["electricalOwnership"] == {
        "OSC_IN": ["U6.2", "X1.1", "C21.1"],
        "OSC_OUT": ["U6.3", "X1.3", "C20.1"],
        "GND": ["X1.2", "X1.4", "C20.2", "C21.2"],
    }
    assert crystal_placement["before"]["u6ToX1RatlinesCross"] is True
    crystal_after = crystal_placement["afterReload"]
    assert crystal_after["u6ToX1RatlinesCross"] is False
    assert crystal_after["layoutLint"] == {
        "componentCount": 69,
        "allTop": True,
        "overlaps": 0,
        "outsideOutline": 0,
        "tightSpacingAt6Mil": 0,
        "crossings": 57,
        "ratsnestMil": 27045.82,
    }
    assert all(
        route == {"tracks": 0, "arcs": 0, "vias": 0}
        for route in crystal_after["routing"].values()
    )
    assert crystal_after["officialDrc"]["oscSignalConnectionErrors"] == 6
    assert crystal_placement["independentVerification"]["status"] == "completed-with-findings"
    assert all(len(value) == 64 for value in crystal_placement["evidence"].values())
    assert crystal_placement["notClaimed"]

    assert crystal_route["status"] == "live-verified"
    assert crystal_route["designDisposition"] == "superseded-as-final-design"
    assert crystal_route["supersededBy"] == "crystal-guard-requirement.json"
    assert crystal_guard["status"] == "source-only"
    assert crystal_guard["module"] == {
        "id": "crystal-guard",
        "members": ["X1", "C20", "C21"],
        "ownerPads": ["U6.2", "U6.3"],
        "signalNets": ["OSC_IN", "OSC_OUT"],
        "guardNet": "GND",
        "movePolicy": "先在局部坐标计算X1/C20/C21、OSC、GND护环/导线、no-pours与过孔，再整体平移到板内；旧模块对象精确清理，不能只移动器件",
    }
    assert crystal_guard["signalRouting"]["topology"] == {
        "OSC_IN": ["C21.1", "X1.1", "U6.2"],
        "OSC_OUT": ["C20.1", "X1.3", "U6.3"],
    }
    assert crystal_guard["signalRouting"]["layer"] == 1
    assert crystal_guard["signalRouting"]["maxViasPerNet"] == 0
    assert crystal_guard["groundGuard"]["net"] == "GND"
    assert crystal_guard["groundGuard"]["primitive"] == "track"
    assert crystal_guard["copperKeepout"] == {
        "primitive": "pcb region",
        "rule": "no-pours",
        "layers": [1, 2],
        "geometry": "覆盖 X1、C20、C21 与 OSC_IN/OSC_OUT 敏感铜的参数化包络；不靠截图猜范围",
        "requirements": [
            "TOP 与 BOTTOM 分别创建并回读 no-pours region；两层板不以 no-inner-electrical 代替外层 region",
            "敏感包络联合 X1/C20/C21 实测 bbox 与最终两条 signal-main 的 stroke bbox；stroke 外扩量为实际线宽一半加 live 铜净距，再叠加 keepout margin，不能只框器件",
            "护环 owner 侧为两条 OSC 保留合并入口，入口之间不得留下没有真实 GND 路径的孤立护环段",
            "no-pours 只禁止自动铺铜进入；显式 OSC 信号与 GND 护环仍按设计写入",
            "铺铜重建后确认禁铺区内没有 pour/fill 铜残留",
        ],
    }
    assert crystal_guard["groundViaFence"]["generator"].startswith("pcb via-fence ")
    assert crystal_guard["toolContract"]["status"] == "offline-verified"
    assert crystal_guard["toolContract"]["layoutSchemaVersion"] == 3
    assert crystal_guard["groundImplementation"] == {
        "mode": "tracks-vias",
        "allowLocalPours": False,
        "requirements": [
            "晶振敏感区和护环区域不创建局部或环形GND铺铜",
            "GND回流使用显式TOP/BOTTOM导线与接地过孔",
            "U6.33 通过 existingViasOnly 复用声明的既有 EP 地孔；逐孔核对 PID、GND 网络和 U6.33 焊盘归属，不创建新孔",
            "候选pours/unreservedPours/affectedBaselinePours为空，apply不含pour.create或pour.rebuild",
            "旧晶振局部铺铜按fresh对象身份和旧journal证明归属后删除",
        ],
    }
    assert crystal_guard["currentBoardDisposition"]["finalDesignAccepted"] is False
    assert can_route["status"] == "live-verified"
    assert can_route["contract"]["orderedMainPaths"] == {
        "CANH": ["U5.7", "R12.1", "CN1.2"],
        "CANL": ["U5.6", "R12.2", "CN1.1"],
    }
    assert can_route["persistence"] == {
        "sequence": ["pcb save", "doc reload", "fresh track readback", "four fresh pcb net-path proofs", "fresh pcb check", "fresh pcb drc", "pcb track-lock", "pcb save", "doc reload", "fresh lock readback"],
        "lockedTracks": 33,
        "lockPersisted": True,
    }
    assert usb_route["status"] == "live-verified"
    assert usb_route["freshReadbackAfterSaveReload"]["actualTracks"] == {
        "USB_D+": 5,
        "USB_D-": 9,
    }
    assert usb_route["freshReadbackAfterSaveReload"]["officialDrc"] == {
        "connectionErrors": 196,
        "otherViolationTypes": 0,
        "deltaFromCanMilestone": -6,
    }
    assert usb_route["persistence"]["lockedTracks"] == 14
    assert usb_route["persistence"]["lockPersisted"] is True

    assert can_placement["status"] == "live-verified"
    assert can_placement["outcome"] == "rejected-as-positive-example"
    assert can_placement["documentUuid"] == "2e719e9419653c72"
    assert can_placement["electricalTopology"]["orderedMainPaths"] == {
        "CANH": ["U5.7", "R12.1", "CN1.2"],
        "CANL": ["U5.6", "R12.2", "CN1.1"],
    }
    can_after = can_placement["afterReload"]
    assert can_after["d1ToCn1SignalDistanceMil"] == {
        "CANH": 168.937,
        "CANL": 168.937,
    }
    assert close(can_after["bboxGapMil"]["D1-CN1"], 10.056)
    assert can_after["layoutLint"]["canCrossings"] == [
        {"x": 2751.9, "y": 1498.45},
        {"x": 2634.81, "y": 1505.24},
    ]
    assert all(
        route == {"tracks": 0, "arcs": 0, "vias": 0}
        for route in can_after["routing"].values()
    )
    assert can_after["officialDrc"]["canSignalConnectionErrors"] == 8
    assert can_placement["nextSearch"]["status"] == "paused-this-layout-round"
    assert can_placement["laterLayoutDecision"] == {
        "status": "layout-pose-accepted",
        "candidate": "layout-candidates-can-current-board/candidate-01.json",
        "reason": "通用rigid候选保持当前R12/D1位置方向，6mil几何间隙合法且H左/L右出口顺序一致；不再用MST相交、长度比或段数比拒绝Layout",
        "routingStillPending": "U5→R12→CN1有序主路径、D1近端支路和实际铜净距仍须在布线阶段证明",
    }
    assert can_layout_candidate["strategy"] == "rigid"
    assert {
        item["ref"]: (item["xMil"], item["yMil"], item["rotationDeg"])
        for item in can_layout_candidate["placements"]
    } == {
        "R12": (2630, 1510, 0),
        "D1": (2791.5, 1528.151, 270),
    }
    assert [item["action"] for item in can_layout_candidate["actions"]] == ["pcb.save"]
    assert can_route_negative["schema"] == "260919-can-candidate/v1"
    assert can_route_negative["status"] == "candidate-unverified"
    snapshot_hash_keys = {
        "boardSha256", "tracksSha256", "viasSha256", "poursSha256",
        "fillsSha256", "regionsSha256",
    }
    assert set(can_route_negative["sourceSnapshot"]) == {"documentUuid", *snapshot_hash_keys}
    assert can_route_negative["sourceSnapshot"]["documentUuid"] == "2e719e9419653c72"
    assert all(len(can_route_negative["sourceSnapshot"][key]) == 64 for key in snapshot_hash_keys)
    assert len(can_route_negative["actions"]) == 8
    assert all(item["action"] == "pcb.line.create" for item in can_route_negative["actions"])
    assert can_route_report["verdict"] == "rejected"
    expected_can_plan_codes = {
        "cross_net_intersection", "forbidden_body_gap", "foreign_net_collinear_overlap",
        "foreign_pad_clearance", "non_45_bend", "non_45_segment", "pad_end_escape",
        "same_net_role_violation", "track_clearance", "branch_pad_inner_entry",
    }
    assert {item["code"] for item in can_route_report["findings"]} == expected_can_plan_codes
    assert can_pair_negative["status"] == "candidate-rejected"
    assert can_pair_negative["review"]["result"] == "rejected-before-write"
    assert can_pair_negative["review"]["notExecuted"] is True
    assert len(can_pair_negative["actions"]) == 36
    assert can_pair_report["verdict"] == "rejected"
    assert {item["code"] for item in can_pair_report["findings"]} == {"branch_pad_inner_entry"}
    assert can_pair_report["facts"]["topology"]["CANH"]["mainLengthMil"] == 606.0087
    assert can_pair_report["facts"]["topology"]["CANH"]["mainSegments"] == 11
    assert can_pair_report["facts"]["topology"]["CANL"]["mainLengthMil"] == 1040.5245
    assert can_pair_report["facts"]["topology"]["CANL"]["mainSegments"] == 21
    assert can_pair_report["facts"]["existingCanViaCount"] == 0
    assert can_placement["independentVerification"]["status"] == "completed-with-findings"
    assert all(len(value) == 64 for value in can_placement["evidence"].values())
    assert can_placement["negativeFindings"] and can_placement["notClaimed"]

    expected_catalog_ids = {
        *(f"SCH-{i:02d}" for i in range(1, 11)),
        *(f"PCB-{i:02d}" for i in range(1, 9)),
        *(f"LAY-{i:02d}" for i in range(1, 7)),
        *(f"RTE-{i:02d}" for i in range(1, 9)),
        *(f"FIN-{i:02d}" for i in range(1, 5)),
    }
    catalog_entries = {entry["id"]: entry for entry in catalog["entries"]}
    assert set(catalog_entries) == expected_catalog_ids
    assert catalog["status"] == "partial-live-verified"
    assert catalog["counts"] == {
        "entries": 36, "SCH": 10, "PCB": 8, "LAY": 6, "RTE": 8, "FIN": 4,
    }
    assert catalog["executionPolicy"] == {
        "allowed": ["pcbpilot Cobra subcommand", "typed action", "pcbpilot apply"],
        "forbidden": [
            "GUI/CUA", "mouse/keyboard/canvas", "property panel/project tree",
            "manual design repair", "debug.exec_js design mutation",
        ],
        "missingCapability": (
            "mark planned/unsupported; implement and validate a typed interface "
            "before live mutation"
        ),
    }
    assert catalog_entries["RTE-05"]["livePlacementIteration"] == {
        "status": "live-verified",
        "outcome": "layout-pose-accepted-routing-negative-retained",
        "data": "can-placement-iteration-live.json",
        "verified": (
            "D1/CN1 的 H/L 保护支路由约217/295mil改为约169/169mil；"
            "R12=(2630,1510)@0°、D1=(2791.5,1528.151)@270° 的当前位置由通用rigid候选接受；69件仍全TOP、"
            "0 overlap、0 off-board、0 tight-spacing；CANH/CANL仍为0 track/0 arc/0 via"
        ),
        "rejected": (
            "纯MST仍有两处H/L相交，R12 y=1488.8简单对齐会异网共线穿越；"
            "这些只作为实际布线反例，不再拒绝当前位置与方向"
        ),
        "layoutCandidate": "layout-candidates-can-current-board/candidate-01.json",
    }
    assert catalog_entries["RTE-05"]["historicalObservation"]["candidate"] == "can-route-plan-pair-negative.json"
    assert catalog_entries["RTE-05"]["historicalObservation"]["notExecuted"] is True
    forbidden_execution_terms = (
        "gui", "cua", "属性面板", "工程树", "刷新浏览器", "刷新整个内置浏览器",
        "mouse", "keyboard", "canvas", "property panel", "project tree",
    )
    executable_text = "\n".join(strings({
        "stepTemplates": catalog["stepTemplates"],
        "entries": catalog["entries"],
    })).lower()
    for term in forbidden_execution_terms:
        assert term.lower() not in executable_text, (
            f"example execution path must not contain interactive fallback: {term}"
        )
    assert {
        "initial-placement.json", "crystal-placement-live.json",
        "crystal-route-live.json", "crystal-guard-requirement.json",
        "can-route-live.json", "usb-route-live.json",
        "can-placement-iteration-live.json",
        "can-route-plan-negative.json", "can-route-plan-negative-report.json",
        "can-route-plan-pair-negative.json", "can-route-plan-pair-negative-report.json",
        "ldo-layout-candidate.json", "ldo-route-live.json",
        "live-validation.json",
    } <= set(catalog["sourceData"])
    required_example_fields = {
        "source", "problem", "startState", "parameters", "steps", "commands",
        "observations", "rationale", "knownErrorsAndFixes", "verificationStatus", "pending",
    }
    allowed_statuses = {"source-only", "offline-verified", "live-verified", "unsupported"}
    for example_id, entry in catalog_entries.items():
        assert required_example_fields <= entry.keys(), example_id
        status = entry["verificationStatus"]
        assert status in allowed_statuses, example_id
        for field in ("source", "parameters", "steps", "commands", "observations", "knownErrorsAndFixes"):
            assert entry[field], f"{example_id}: empty {field}"

        if status == "live-verified":
            assert entry["pending"] == [], f"{example_id}: live-verified still has pending work"
            assert isinstance(entry.get("liveEvidence"), str) and entry["liveEvidence"].strip(), (
                f"{example_id}: live-verified requires concrete liveEvidence"
            )
        else:
            assert entry["pending"], f"{example_id}: unfinished status needs explicit pending work"

    assert placement["status"] == "partial-live-verified"
    for field in (
        "source", "startState", "parameters", "commands", "observations",
        "knownErrorsAndFixes", "evidence", "notClaimed",
    ):
        assert placement[field], f"initial-placement.json: empty {field}"
    assert placement["units"] == "mil"
    assert placement["coordinateSemantic"] == "footprint-anchor"
    assert placement["board"] == {
        "widthMil": 3543.31, "heightMil": 1968.5, "widthMm": 90, "heightMm": 50,
    }
    placements = {item["ref"]: item for item in placement["placements"]}
    assert len(placements) == len(placement["placements"]) == 69
    assert set(placements) == set(refs)
    for ref, (x_mm, y_mm, rotation) in expected.items():
        item = placements[ref]
        assert close(item["xMil"], x_mm / 0.0254), ref
        assert close(item["yMil"], y_mm / 0.0254), ref
        assert item["rotationDeg"] == rotation, ref
        assert item["locked"] is True, ref
    cn1 = placements["CN1"]
    assert close(cn1["yMil"], 42 / 0.0254)
    assert cn1["rotationDeg"] == 180 and cn1["locked"] is False
    placement_verification = placement["verification"]
    assert placement_verification["componentCount"] == 69
    assert placement_verification["uniqueDesignators"] == 69
    for field in (
        "placementDiffCountAfterFullBrowserReload", "bboxCollisionCount",
        "sub6MilBBoxPairCount", "layoutLintOutsideOutlineCount",
        "layoutLintTightSpacingCount",
    ):
        assert placement_verification[field] == 0, field

    assert browser_reopen["status"] == "live-verified"
    assert browser_reopen["checks"]["componentCount"] == 69
    assert browser_reopen["checks"]["ldoTrackCount"] == 15
    assert browser_reopen["checks"]["primitiveIdSetUnchanged"] is True
    assert browser_reopen["checks"]["componentGeometryUnchanged"] is True
    latest_reopen = browser_reopen["rechecks"][-1]
    assert latest_reopen["trigger"] == "user-reopened-in-app-browser-again"
    assert latest_reopen["componentCount"] == 69
    assert latest_reopen["routedLineCount"] == 15
    assert latest_reopen["componentsEqualToSavedBaseline"] is True
    assert latest_reopen["outlineEqualToSavedBaseline"] is True
    assert latest_reopen["tracksEqualToSavedBaseline"] is True
    for field in ("componentsSha256", "outlineSha256", "tracksResultSha256"):
        assert len(latest_reopen[field]) == 64

    assert u3_binding["status"] == "live-verified-read-only"
    assert u3_binding["schematicU3"]["lcsc"] == "C2890616"
    assert u3_binding["schematicU3"]["footprintName"] == "OLED-SMD_ST7735S"
    assert u3_binding["schematicU3"]["model3dUuid"] == "55cc08024bd249a298d835f2dd067767"
    assert u3_binding["pcbU3"]["padCount"] == 13
    assert u3_binding["previousWork"]["personalFixture"]["boundToU3"] is False
    assert u3_binding["boardTopLevelRegions"]["count"] == 0

    assert live["status"] == "partial-live-verified"
    assert live["purpose"].endswith("不是完成态整板答案")
    schematic = live["schematic"]
    assert (schematic["components"], schematic["terminals"], schematic["explicitNc"], schematic["nets"]) == (
        69, 233, 13, 46,
    )
    assert schematic["sourceEndpointDiffs"] == 0
    assert schematic["officialDrc"]["total"] == 0
    mechanics = live["pcbMechanics"]
    assert mechanics["components"] == 69
    outline = mechanics["outline"]
    assert close(outline["widthMil"], 90 / 0.0254)
    assert close(outline["heightMil"], 50 / 0.0254)
    assert close(outline["radiusMil"], 3 / 0.0254)
    assert outline["nativeArcs"] == 4 and outline["locked"] is True
    rules = live["pcbRules"]
    assert rules["signalTrackMil"] == {"min": 8.0, "default": 8.0}
    assert rules["clearanceMil"] == 6.0
    assert rules["viaMil"] == {"outerMin": 24.0, "holeMin": 12.0}
    assert rules["powerTrackMil"] == {"min": 8.0, "default": 20.0}
    assert set(rules["netClass"]["nets"]) == {"+5V", "+3V3", "GND"}
    assert rules["verifiedAfterFullBrowserReload"] is True
    ldo_live = live["pcbLdo"]
    assert ldo_live["status"] == "live-verified"
    assert ldo_live["placement"] == {
        "refs": ["U2", "C3", "C4", "C5", "C6"],
        "allTop": True,
        "overlaps": 0,
        "outsideOutline": 0,
        "tightSpacingAt6Mil": 0,
    }
    assert ldo_live["routing"]["trackCount"] == 15
    assert ldo_live["routing"]["viaCount"] == 0
    assert ldo_live["routing"]["danglingEnds"] == 0
    assert ldo_live["officialDrc"]["counts"] == {"Connection Error": 216}
    assert ldo_live["independentReview"]["status"] == "completed-with-findings"
    crystal_live = live["pcbCrystalPlacement"]
    assert crystal_live["status"] == "live-verified"
    assert crystal_live["placement"]["u6StillLocked"] is True
    assert crystal_live["placement"]["u6ToX1RatlinesCrossBefore"] is True
    assert crystal_live["placement"]["u6ToX1RatlinesCrossAfter"] is False
    assert crystal_live["routing"]["OSC_IN"] == {"tracks": 0, "arcs": 0, "vias": 0}
    assert crystal_live["routing"]["OSC_OUT"] == {"tracks": 0, "arcs": 0, "vias": 0}
    assert crystal_live["independentReview"]["status"] == "completed-with-findings"
    can_live = live["pcbCanPlacementIteration"]
    assert can_live["status"] == "live-verified"
    assert can_live["outcome"] == "layout-pose-accepted-routing-negative-retained"
    assert can_live["placement"]["canCrossings"] == 2
    assert can_live["routing"]["CANH"] == {"tracks": 0, "arcs": 0, "vias": 0}
    assert can_live["routing"]["CANL"] == {"tracks": 0, "arcs": 0, "vias": 0}
    assert can_live["officialDrc"]["canSignalConnectionErrors"] == 8
    assert can_live["independentReview"]["status"] == "completed-with-findings"
    assert any(
        issue["id"] == "same-window-parallel-doc-guard"
        for issue in live["observedToolIssues"]
    )
    assert live["notClaimed"], "partial live validation must list unverified work"

    live_count = sum(entry["verificationStatus"] == "live-verified" for entry in catalog_entries.values())
    print(
        "260919 examples: 27 BOM rows, 69 instances, 69 connectivity records, "
        f"233 terminals, 46 nets, 15 zones, 36 catalog entries, {live_count} live-verified — consistent"
    )


if __name__ == "__main__":
    main()
