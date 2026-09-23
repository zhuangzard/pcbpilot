"""Resistance-selection regressions; offline fixtures, never query the catalog."""

import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / ".agents/skills/pcbpilot/scripts/parts-select.py"
spec = importlib.util.spec_from_file_location("parts_select", SCRIPT)
selector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(selector)

# JLCPCB selectSmtComponentList, C52548, read 2026-09-09. Only identity/spec
# fields are retained; stock/price below are deliberately synthetic test inputs.
# https://www.lcsc.com/product-detail/C52548.html
C52548 = {
    "componentCode": "C52548",
    "componentModelEn": "0805W8F330LT5E",
    "componentTypeEn": "Chip Resistor - Surface Mount",
    "secondSortName": "Resistors",
    "componentSpecificationEn": "0805",
    "describe": "-55℃~+155℃ 125mW 150V 330mΩ Thick Film Resistor ±1% ±800ppm/℃ 0805 Chip Resistor - Surface Mount ROHS",
    "attributes": [{"attribute_name_en": "Resistance", "attribute_value_name": "330mΩ"}],
}


def catalog_part(code, resistance, **overrides):
    part = copy.deepcopy(C52548)
    part.update(componentCode=code, describe=f"{resistance} ±1% 0805",
                attributes=[{"attribute_name_en": "Resistance", "attribute_value_name": resistance}],
                stockCount=1000, componentLibraryType="base",
                componentPrices=[{"startNumber": 1, "productPrice": 0.01}])
    part.update(overrides)
    return part


class PartsResistanceTests(unittest.TestCase):
    def local(self, query, parts):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "parts.json"
            path.write_text(json.dumps({"parts": parts}), encoding="utf-8")
            with mock.patch.object(selector, "STANDARD_PARTS", str(path)):
                return selector.local_select(query)

    def online(self, query, parts):
        with mock.patch.object(selector, "jlc_search", return_value=parts):
            return selector.select(query)

    def test_online_c52548_matches_decimal_ohms_and_preserves_evidence(self):
        results = self.online("0.33Ω 0805", [C52548])
        self.assertEqual([r["lcsc"] for r in results], ["C52548"])
        self.assertEqual(results[0]["resistance"], {
            "raw": "330mΩ", "ohms": "0.33", "source": "attributes.Resistance",
        })
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            selector.print_online(results, "0.33Ω 0805", 100)
        self.assertIn("330mΩ = 0.33 Ω", output.getvalue())

    def test_online_33_ohms_rejects_milliohms_and_330_ohms_before_price_ranking(self):
        wrong = [catalog_part("C52548", "330mΩ"), catalog_part("C330", "330Ω")]
        right = catalog_part("C33", "33Ω", componentLibraryType="expand",
                             componentPrices=[{"startNumber": 1, "productPrice": 1}])
        self.assertEqual([r["lcsc"] for r in self.online("33Ω 0805", wrong + [right])], ["C33"])

    def test_no_matching_value_does_not_fall_back_to_wrong_recommendation(self):
        results = self.online("33Ω", [C52548])
        self.assertEqual(results, [])
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            selector.print_online(results, "33Ω", 100)
        self.assertNotIn("✅ 推荐", output.getvalue())

    def test_local_milli_mega_and_decimal_points_remain_distinct(self):
        parts = {f"res.{key}": {"value": value, "lcsc": key} for key, value in [
            ("milli", "1mΩ"), ("mega", "1M"), ("decimal", "0.33Ω"),
            ("integer", "33Ω"), ("milliequiv", "330mΩ"),
        ]}
        for query, expected in [("1mΩ", {"milli"}), ("1MΩ", {"mega"}),
                                ("33Ω", {"integer"}), ("330mΩ", {"decimal", "milliequiv"})]:
            with self.subTest(query=query):
                self.assertEqual({r["lcsc"] for r in self.local(query, parts)}, expected)

    def test_local_value_cannot_come_from_key_mpn_or_description(self):
        parts = {
            "res.33_0805": {"mpn": "0805W8F330LT5E", "desc": "33Ω", "lcsc": "C52548"},
            "res.33_wrong": {"value": "330mΩ", "mpn": "33", "desc": "33Ω", "lcsc": "C2"},
            "inductor.33": {"value": "33Ω", "lcsc": "C3"},
        }
        self.assertEqual(self.local("33Ω", parts), [])

    def test_online_mpn_digits_and_other_attributes_are_not_resistance(self):
        missing = catalog_part("C52548", "33Ω", describe="0805W8F330LT5E", attributes=[])
        other = catalog_part("C2", "33Ω", describe="33V 0805", attributes=[
            {"attribute_name_en": "Voltage", "attribute_value_name": "33Ω"}])
        self.assertEqual(self.online("33Ω", [missing, other]), [])

    def test_online_milli_mega_and_ohm_spelling_are_distinct(self):
        parts = [catalog_part("Cm", "1mΩ"), catalog_part("CM", "1MΩ")]
        for query, expected in [("1 mohm", "Cm"), ("1 MOhms", "CM")]:
            with self.subTest(query=query):
                self.assertEqual([r["lcsc"] for r in self.online(query, parts)], [expected])

    def test_ambiguous_or_malformed_explicit_query_fails_closed_without_network(self):
        for query in ["33Ω 330Ω", "-33Ω", "0.33.0Ω", "3e3Ω",
                      "1,000Ω", "0,33Ω", "1/2Ω", "1, 000Ω", "1 / 2Ω",
                      "33Ωx", "33Ω2", "33Ωunknown", "33ohmXYZ",
                      "<33Ω", ">33Ω", "≤33Ω", "≥ 33Ω", "10^3Ω", "−33Ω", "10**3Ω",
                      "3e3ohm 0805", "10Gohm 0805", "10 Gohm 0805", "1uohm 0805"]:
            with self.subTest(query=query), mock.patch.object(selector, "jlc_search") as search:
                self.assertEqual(selector.select(query), [])
                self.assertEqual(selector.local_select(query), [])
                search.assert_not_called()

    def test_chinese_adjacent_resistance_queries_cannot_bypass_quantity_gate(self):
        for query in ["33Ω电阻 0805", "1mΩ电阻 0805", "33ohm电阻 0805", "电阻33Ω 0805"]:
            with self.subTest(query=query):
                self.assertEqual(self.online(query, [C52548]), [])
        for query in ["330mΩ电阻 0805", "0.33ohm电阻 0805", "电阻0.33Ω 0805"]:
            with self.subTest(query=query):
                self.assertEqual([r["lcsc"] for r in self.online(query, [C52548])], ["C52548"])

    def test_non_unit_brand_uniohm_keeps_normal_search_behavior(self):
        part = catalog_part("C52548", "330mΩ", componentBrandEn="UNIOHM",
                            describe="UNIOHM 330mΩ resistor")
        self.assertEqual([r["lcsc"] for r in self.online("UNIOHM", [part])], ["C52548"])
        self.assertEqual([r["lcsc"] for r in self.online("0805 UNIOHM", [part])], ["C52548"])
        self.assertEqual([r["lcsc"] for r in self.local("UNIOHM", {
            "res.uniohm": {"value": "330mΩ", "manufacturer": "UNIOHM", "lcsc": "C52548"},
        })], ["C52548"])

    def test_local_conflicting_explicit_description_is_rejected(self):
        self.assertEqual(self.local("33Ω", {
            "res.conflict": {"value": "33Ω", "desc": "330mΩ 0805", "lcsc": "C52548"},
        }), [])

    def test_online_inductor_dcr_is_not_a_resistor(self):
        inductor = catalog_part("CIND", "33Ω", componentTypeEn="Inductors (SMD)",
                                secondSortName="Inductors", describe="DCR 33Ω 0805")
        self.assertEqual(self.online("33Ω", [inductor]), [])

    def test_online_explicit_description_fallback_requires_resistor_type(self):
        resistor = catalog_part("CR", "330mΩ", attributes=[])
        unknown = catalog_part("CU", "330mΩ", attributes=[], componentTypeEn="", secondSortName="")
        results = self.online("0.33 ohms", [resistor, unknown])
        self.assertEqual([r["lcsc"] for r in results], ["CR"])
        self.assertEqual(results[0]["resistance"]["source"], "describe")

    def test_online_missing_ambiguous_or_conflicting_resistance_is_rejected(self):
        parts = [
            catalog_part("C1", "330mΩ", describe="33Ω"),
            catalog_part("C2", "330mΩ", describe="330mΩ / 33Ω", attributes=[]),
            catalog_part("C3", "330mΩ", attributes=[
                {"attribute_name_en": "Resistance", "attribute_value_name": "unknown"}]),
            catalog_part("C4", "330mΩ", attributes=[
                {"attribute_name_en": "Resistance", "attribute_value_name": "330mΩ"},
                {"attribute_name_en": "Resistance", "attribute_value_name": "33Ω"}]),
        ]
        self.assertEqual(self.online("330mΩ", parts), [])

    def test_equivalent_attribute_spellings_do_not_make_a_conflict(self):
        part = catalog_part("C52548", "330mΩ")
        part["attributes"].append({"attribute_name_en": "Resistance", "attribute_value_name": "0.33Ω"})
        self.assertEqual([r["lcsc"] for r in self.online("0.33Ω", [part])], ["C52548"])

    def test_real_standard_library_does_not_recommend_330_ohms_for_33_ohms(self):
        self.assertEqual(selector.local_select("33Ω"), [])
        self.assertEqual(selector.local_select("1mΩ"), [])
        self.assertEqual([r["lcsc"] for r in selector.local_select("330Ω")], ["C25104"])

    def test_non_resistance_queries_keep_existing_selection(self):
        self.assertIn("C1525", [r["lcsc"] for r in selector.local_select("100nF 0402")])
        self.assertIn("C6186", [r["lcsc"] for r in selector.local_select("AMS1117")])
        for query in ["0805W8F330LT5E", "C52548"]:
            with self.subTest(query=query):
                result = self.online(query, [C52548])
                self.assertEqual([r["lcsc"] for r in result], ["C52548"])
                self.assertEqual(result[0]["resistance"], {
                    "raw": "330mΩ", "ohms": "0.33", "source": "attributes.Resistance",
                })

    def test_local_mpn_query_adds_resistance_evidence_without_changing_identity(self):
        result = selector.local_select("0402WGF3300TCE")
        self.assertEqual([r["lcsc"] for r in result], ["C25104"])
        self.assertEqual(result[0]["resistance"], {
            "raw": "330Ω", "ohms": "330", "source": "standard-parts.value",
        })

    def test_main_no_resistance_match_exits_nonzero_and_never_recommends(self):
        for flags in [[], ["--json"], ["--online"], ["--online", "--json"]]:
            with self.subTest(flags=flags), mock.patch.object(sys, "argv", [str(SCRIPT), "33Ω"] + flags), \
                    mock.patch.object(selector, "jlc_search", return_value=[C52548]):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    self.assertEqual(selector.main(), 1)
                self.assertNotIn("✅ 推荐", output.getvalue())
                if "--json" in flags:
                    self.assertEqual(json.loads(output.getvalue()), [])
                else:
                    self.assertIn("不作模糊推荐", output.getvalue())

    def test_main_unmatched_non_resistance_query_keeps_zero_exit(self):
        with mock.patch.object(sys, "argv", [str(SCRIPT), "no_such_part_xyz", "--json"]), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(selector.main(), 0)


if __name__ == "__main__":
    unittest.main()
