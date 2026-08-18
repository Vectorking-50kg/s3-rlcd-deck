#!/usr/bin/env python3

import re
import unicodedata
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
FONT_SOURCE = REPOSITORY_ROOT / "firmware/components/application_ui/assets/lv_font_deck_m0_16.c"
FONT_SOURCE_20 = REPOSITORY_ROOT / "firmware/components/application_ui/assets/lv_font_deck_ui_20.c"
FONT_SOURCE_32 = REPOSITORY_ROOT / "firmware/components/application_ui/assets/lv_font_deck_ui_32.c"
GLYPH_MANIFEST = REPOSITORY_ROOT / "firmware/components/application_ui/assets/m0_glyphs.txt"
SNAPSHOT_DIRECTORY = REPOSITORY_ROOT / "tests/host/snapshots"
LABEL_WIDTH_PX = 384
DISPLAY_HEIGHT_PX = 300
TOP_OFFSET_PX = 6
LINE_HEIGHT_PX = 19
LINE_SPACING_PX = 1
MAXIMUM_LINES = 13


def ascii_advances() -> dict[str, int]:
    source = FONT_SOURCE.read_text(encoding="utf-8")
    descriptors = source.split(
        "static const lv_font_fmt_txt_glyph_dsc_t glyph_dsc[] = {", 1
    )[1].split("};", 1)[0]
    advances = [int(value) for value in re.findall(r"\.adv_w = (\d+)", descriptors)]
    if len(advances) < 96:
        raise AssertionError("generated font does not contain the complete printable ASCII range")
    return {chr(codepoint): advances[codepoint - 31] for codepoint in range(32, 127)}


ADVANCES = ascii_advances()


def generated_bitmap_size(source_path: Path) -> int:
    source = source_path.read_text(encoding="utf-8")
    bitmap = source.split(
        "static LV_ATTRIBUTE_LARGE_CONST const uint8_t glyph_bitmap[] = {", 1
    )[1].split("};", 1)[0]
    bitmap = re.sub(r"/\*.*?\*/", "", bitmap, flags=re.DOTALL)
    return len(re.findall(r"0x[0-9a-fA-F]+", bitmap))


def lvgl_line_width(text: str) -> int:
    # LVGL rounds every glyph's 1/16 px advance independently.
    return sum((ADVANCES[character] + 8) >> 4 for character in text)


class AiPageFontLayoutContract(unittest.TestCase):
    def test_glyph_manifest_is_canonical_and_duplicate_free(self) -> None:
        raw = GLYPH_MANIFEST.read_text(encoding="utf-8")
        glyphs = raw.rstrip("\n")
        self.assertNotIn("\r", raw)
        self.assertEqual(unicodedata.normalize("NFC", glyphs), glyphs)
        self.assertEqual(len(glyphs), len(set(glyphs)))

    def test_generated_font_metrics_and_bitmap_budgets(self) -> None:
        expected = (
            (FONT_SOURCE, 19, 5, 7_000),
            (FONT_SOURCE_20, 23, 6, 11_000),
            (FONT_SOURCE_32, 37, 9, 5_000),
        )
        for source_path, line_height, base_line, bitmap_budget in expected:
            source = source_path.read_text(encoding="utf-8")
            self.assertIn(f".line_height = {line_height}", source, source_path.name)
            self.assertIn(f".base_line = {base_line}", source, source_path.name)
            self.assertLessEqual(
                generated_bitmap_size(source_path), bitmap_budget, source_path.name
            )

    def test_production_non_ascii_copy_is_in_the_font_manifest(self) -> None:
        manifest = set(GLYPH_MANIFEST.read_text(encoding="utf-8"))
        production_sources = (
            REPOSITORY_ROOT / "firmware/components/application_ui/deck_m0_view_model.cpp",
            REPOSITORY_ROOT / "firmware/components/application_ui/deck_ui_preview.cpp",
            REPOSITORY_ROOT / "firmware/components/application_ui/deck_ui_scene.cpp",
        )
        required = {
            character
            for source in production_sources
            for character in source.read_text(encoding="utf-8")
            if ord(character) > 0x7F
        }
        self.assertEqual(required - manifest, set())

    def test_golden_snapshots_fit_the_real_generated_font(self) -> None:
        for snapshot in sorted(SNAPSHOT_DIRECTORY.glob("ai-page-*.txt")):
            lines = snapshot.read_text(encoding="utf-8").splitlines()
            self.assertLessEqual(len(lines), MAXIMUM_LINES, snapshot.name)
            for line in lines:
                self.assertLessEqual(lvgl_line_width(line), LABEL_WIDTH_PX, (snapshot.name, line))

    def test_dynamic_extremes_fit_without_lvgl_auto_wrap(self) -> None:
        widest_rows = (
            "WWWWWWWW[##########] R100% @999d+",
            "WWWWWWWW[##########] U100% @999d+",
            "W" * 27,
            "WAITING APPROVAL / UNAVAILABLE / 999d+",
            "999.9B TOK / CTX 100% / +15 SESS",
        )
        for row in widest_rows:
            self.assertLessEqual(lvgl_line_width(row), LABEL_WIDTH_PX, row)

    def test_thirteen_lines_fit_the_physical_height(self) -> None:
        used_height = (
            TOP_OFFSET_PX
            + MAXIMUM_LINES * LINE_HEIGHT_PX
            + (MAXIMUM_LINES - 1) * LINE_SPACING_PX
        )
        self.assertLessEqual(used_height, DISPLAY_HEIGHT_PX)


if __name__ == "__main__":
    unittest.main()
