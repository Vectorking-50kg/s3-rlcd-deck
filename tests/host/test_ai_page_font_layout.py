#!/usr/bin/env python3

import re
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
FONT_SOURCE = REPOSITORY_ROOT / "firmware/components/application_ui/assets/lv_font_deck_m0_16.c"
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


def lvgl_line_width(text: str) -> int:
    # LVGL rounds every glyph's 1/16 px advance independently.
    return sum((ADVANCES[character] + 8) >> 4 for character in text)


class AiPageFontLayoutContract(unittest.TestCase):
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
