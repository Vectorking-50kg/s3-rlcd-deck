#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_font="firmware/managed_components/lvgl__lvgl/scripts/built_in_font/SourceHanSansSC-Normal.otf"
output="firmware/components/application_ui/assets/lv_font_deck_m0_16.c"
glyph_manifest="firmware/components/application_ui/assets/m0_glyphs.txt"

cd "$repository_root"

if [[ ! -f "$source_font" ]]; then
    echo "LVGL managed dependency is missing; run ./tools/idf.sh dev reconfigure first." >&2
    exit 1
fi

glyphs="$(tr -d '\r\n' < "$glyph_manifest")"
if [[ -z "$glyphs" ]]; then
    echo "M0 glyph manifest is empty: $glyph_manifest" >&2
    exit 1
fi

npx --yes lv_font_conv@1.5.3 \
    --size 16 \
    --bpp 1 \
    --font "$source_font" \
    -r 0x20-0x7e \
    --symbols "$glyphs" \
    --format lvgl \
    --no-compress \
    --no-prefilter \
    --no-kerning \
    --lv-font-name lv_font_deck_m0_16 \
    --lv-include lvgl.h \
    -o "$output"
