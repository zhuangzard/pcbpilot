"""Edition re-localization regressions; offline only, never runs the pcbpilot CLI.

The live fact this guards: the international edition (easyeda.com) ships the same
system library uuid as the China edition but DIFFERENT device uuids, so every
deviceUuid curated in standard-parts.json is unknown to the platform there and
`sch block-apply` dies at its first placement with "connector did not respond".
`relocalize` is the pure transform that rewrites those uuids from a
`lib by-lcsc` answer; `extract_json`/`components_of` parse that answer out of
noisy CLI stdout. Both are exercised here with frozen fixtures, no subprocess.
"""

import copy
import importlib.util
import json
from pathlib import Path
import unittest


REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / ".agents/skills/pcbpilot/scripts/parts-relocalize.py"
spec = importlib.util.spec_from_file_location("parts_relocalize", SCRIPT)
relocalizer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relocalizer)

# The real system-library uuid of standard-parts.json (identical on both editions).
LIB = "0819f05c4eef4c71ace90d822a990e87"
# C8678, read live on desktop 3.2.149 international vs. the curated China value.
CN_UUID = "009407eaaa604eb9b6f73cc3868f316d"
INTL_UUID = "804240ef97df427480be2a5281ccea31"

PARTS = {
    "res.10k_0402": {"value": "10kΩ", "lcsc": "C25744", "deviceUuid": "aaa11111111111111111111111111111"},
    "cap.100nF_0402": {"value": "100nF", "lcsc": "C1525", "deviceUuid": "bbb22222222222222222222222222222"},
    "ic.ams1117_33": {"value": "AMS1117-3.3", "lcsc": "C6186", "deviceUuid": CN_UUID},
}


def component(lcsc, uuid, library=LIB, **extra):
    """One entry of the by-lcsc `result.components` array."""
    part = {"lcsc": lcsc, "uuid": uuid, "libraryUuid": library,
            "value": "x", "footprintName": "C0402", "manufacturer": "ACME",
            "manufacturerId": "ACME-1"}
    part.update(extra)
    return part


def index(*components):
    return relocalizer.index_by_lcsc(list(components))


class RelocalizeTransformTests(unittest.TestCase):
    def relocalize(self, resolved, parts=None, library=LIB):
        source = copy.deepcopy(PARTS if parts is None else parts)
        new_parts, summary = relocalizer.relocalize(source, resolved, library)
        # The transform must never mutate its input: the canonical file is the
        # source of truth and the script writes a copy beside it.
        self.assertEqual(source, PARTS if parts is None else parts)
        return new_parts, summary

    def test_all_same_uuids_change_nothing(self):
        resolved = index(*[component(p["lcsc"], p["deviceUuid"]) for p in PARTS.values()])
        new_parts, summary = self.relocalize(resolved)
        self.assertEqual(sorted(summary["same"]), sorted(PARTS))
        self.assertEqual(summary["changed"], [])
        self.assertEqual(summary["unresolved"], [])
        self.assertEqual(summary["libraryMismatch"], [])
        self.assertEqual(new_parts, PARTS)

    def test_changed_uuid_is_applied_and_the_original_is_kept(self):
        resolved = index(
            component("C25744", PARTS["res.10k_0402"]["deviceUuid"]),
            component("C1525", PARTS["cap.100nF_0402"]["deviceUuid"]),
            component("C6186", INTL_UUID),
        )
        new_parts, summary = self.relocalize(resolved)
        self.assertEqual(summary["changed"], ["ic.ams1117_33"])
        self.assertEqual(sorted(summary["same"]), ["cap.100nF_0402", "res.10k_0402"])
        entry = new_parts["ic.ams1117_33"]
        self.assertEqual(entry["deviceUuid"], INTL_UUID)
        self.assertEqual(entry["deviceUuidOrigin"], CN_UUID)
        # Everything else about the curated entry survives untouched.
        self.assertEqual(entry["value"], "AMS1117-3.3")
        self.assertNotIn(relocalizer.MARK, entry)

    def test_rerunning_on_an_output_keeps_the_first_origin(self):
        once, _ = self.relocalize(index(component("C6186", INTL_UUID)))
        again, summary = relocalizer.relocalize(
            {"ic.ams1117_33": once["ic.ams1117_33"]},
            index(component("C6186", "ccc33333333333333333333333333333")), LIB)
        self.assertEqual(summary["changed"], ["ic.ams1117_33"])
        # deviceUuidOrigin still points at the canonical China-edition value, not
        # at the previous run's international uuid.
        self.assertEqual(again["ic.ams1117_33"]["deviceUuidOrigin"], CN_UUID)

    def test_unresolved_parts_keep_their_uuid_and_are_marked(self):
        new_parts, summary = self.relocalize(index(component("C25744", "aaa11111111111111111111111111111")))
        self.assertEqual(summary["same"], ["res.10k_0402"])
        self.assertEqual(sorted(summary["unresolved"]), ["cap.100nF_0402", "ic.ams1117_33"])
        for key in summary["unresolved"]:
            self.assertEqual(new_parts[key]["deviceUuid"], PARTS[key]["deviceUuid"])
            self.assertEqual(new_parts[key][relocalizer.MARK], "unresolved")
            self.assertNotIn("deviceUuidOrigin", new_parts[key])

    def test_an_ambiguous_lcsc_is_unresolved_not_a_coin_flip(self):
        resolved = index(component("C6186", INTL_UUID), component("C6186", "ddd44444444444444444444444444444"))
        new_parts, summary = self.relocalize(resolved)
        self.assertIn("ic.ams1117_33", summary["unresolved"])
        self.assertEqual(new_parts["ic.ams1117_33"]["deviceUuid"], CN_UUID)
        self.assertTrue(any("resolved to 2 devices" in w for w in summary["warnings"]))

    def test_library_uuid_mismatch_warns_and_never_rewrites(self):
        other = "ffffffffffffffffffffffffffffffff"
        new_parts, summary = self.relocalize(index(component("C6186", INTL_UUID, library=other)))
        self.assertEqual(summary["libraryMismatch"], ["ic.ams1117_33"])
        self.assertEqual(summary["changed"], [])
        entry = new_parts["ic.ams1117_33"]
        self.assertEqual(entry["deviceUuid"], CN_UUID)  # NOT applied
        self.assertNotIn("libraryUuid", entry)          # the file's uuid is not rewritten either
        self.assertEqual(entry[relocalizer.MARK], "libraryUuid-mismatch")
        self.assertTrue(any(other in w and "NOT applied" in w for w in summary["warnings"]))

    def test_a_per_part_library_override_selects_the_matching_candidate(self):
        other = "ffffffffffffffffffffffffffffffff"
        parts = {"mcu.x": {"lcsc": "C2980297", "deviceUuid": CN_UUID, "libraryUuid": other}}
        resolved = index(component("C2980297", INTL_UUID, library=LIB),
                         component("C2980297", "eee55555555555555555555555555555", library=other))
        new_parts, summary = self.relocalize(resolved, parts=parts)
        self.assertEqual(summary["changed"], ["mcu.x"])
        self.assertEqual(new_parts["mcu.x"]["deviceUuid"], "eee55555555555555555555555555555")

    def test_a_part_without_a_real_c_number_cannot_be_resolved(self):
        # "(onboard)" is a real placeholder in the curated file (ic.pc817_sop4);
        # it must be reported, never shipped to `lib by-lcsc` as a query.
        parts = {"conn.custom": {"value": "header", "deviceUuid": CN_UUID},
                 "ic.pc817_sop4": {"lcsc": "(onboard)", "deviceUuid": CN_UUID}}
        new_parts, summary = self.relocalize(index(component("C6186", INTL_UUID)), parts=parts)
        self.assertEqual(sorted(summary["unresolved"]), ["conn.custom", "ic.pc817_sop4"])
        for key in parts:
            self.assertEqual(new_parts[key]["deviceUuid"], CN_UUID)
        self.assertTrue(any("is not a C-number" in w for w in summary["warnings"]))

    def test_placeholder_lcsc_values_are_not_queryable_c_numbers(self):
        self.assertEqual(relocalizer.normalize_lcsc(" c6186 "), "C6186")
        for value in ["(onboard)", "", None, "CXYZ", "6186", "C6186-A"]:
            with self.subTest(value=value):
                self.assertEqual(relocalizer.normalize_lcsc(value), "")


class CliOutputParsingTests(unittest.TestCase):
    # Shape verified live: `pcbpilot lib by-lcsc --lcsc C1525 --project <name>`.
    RESPONSE = {
        "ok": True,
        "result": {
            "components": [component("C1525", "2eaf9ba5000000000000000000000000", value="100nF")],
            "count": 1,
            "requested": ["C1525"],
        },
    }

    def test_json_is_extracted_from_noise_before_and_after_it(self):
        noisy = ("connecting to daemon on 61832...\n"
                 "warning: connector version 1.5.1 vs cli 1.5.2\n"
                 + json.dumps(self.RESPONSE) + "\n"
                 "done in 412ms\n")
        components = relocalizer.components_of(relocalizer.extract_json(noisy))
        self.assertEqual([c["uuid"] for c in components], ["2eaf9ba5000000000000000000000000"])

    def test_pretty_printed_multiline_json_survives_trailing_noise(self):
        noisy = "[info] resolving 1 C-number\n" + json.dumps(self.RESPONSE, indent=2) + "\nsaved.\n"
        self.assertEqual(len(relocalizer.components_of(relocalizer.extract_json(noisy))), 1)

    def test_output_without_any_json_object_is_an_error(self):
        for text in ["", None, "Error: daemon not found\n"]:
            with self.subTest(text=text), self.assertRaises(ValueError):
                relocalizer.extract_json(text)

    def test_a_reported_failure_is_surfaced_not_read_as_zero_components(self):
        doc = {"ok": False, "error": {"message": "connector did not respond"}}
        with self.assertRaises(ValueError) as caught:
            relocalizer.components_of(doc)
        self.assertIn("connector did not respond", str(caught.exception))

    def test_lcsc_keys_are_matched_case_and_whitespace_insensitively(self):
        resolved = relocalizer.index_by_lcsc([component(" c6186 ", INTL_UUID)])
        self.assertEqual(list(resolved), ["C6186"])
        new_parts, summary = relocalizer.relocalize(
            copy.deepcopy({"ic.ams1117_33": PARTS["ic.ams1117_33"]}), resolved, LIB)
        self.assertEqual(summary["changed"], ["ic.ams1117_33"])
        self.assertEqual(new_parts["ic.ams1117_33"]["deviceUuid"], INTL_UUID)


class BatchingTests(unittest.TestCase):
    def test_batches_never_exceed_the_per_call_ceiling(self):
        items = ["C%d" % i for i in range(143)]
        chunks = list(relocalizer.batches(items, relocalizer.BATCH_MAX))
        self.assertEqual([len(c) for c in chunks], [20] * 7 + [3])
        self.assertEqual([c for chunk in chunks for c in chunk], items)

    def test_the_command_passes_project_and_window_through(self):
        cmd = relocalizer.build_command("pcbpilot", ["C1", "C2"], project="demo", window="w1")
        self.assertEqual(cmd, ["pcbpilot", "lib", "by-lcsc", "--lcsc", "C1,C2",
                               "--project", "demo", "--window", "w1"])
        self.assertEqual(relocalizer.build_command("pcbpilot", ["C1"]),
                         ["pcbpilot", "lib", "by-lcsc", "--lcsc", "C1"])


if __name__ == "__main__":
    unittest.main()
