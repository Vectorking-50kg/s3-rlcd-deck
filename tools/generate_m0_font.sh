#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_font="firmware/managed_components/lvgl__lvgl/scripts/built_in_font/SourceHanSansSC-Normal.otf"
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

generate_font() {
    local size="$1"
    local name="$2"
    local output="$3"
    local include_chinese="$4"
    local arguments=(
        --size "$size"
        --bpp 1
        --font "$source_font"
        -r 0x20-0x7e
        --format lvgl
        --no-compress
        --no-prefilter
        --no-kerning
        --lv-font-name "$name"
        --lv-include lvgl.h
        -o "$output"
    )
    if [[ "$include_chinese" == "yes" ]]; then
        arguments+=(--symbols "$glyphs")
    fi
    npx --yes lv_font_conv@1.5.3 "${arguments[@]}"
    perl -0pi -e 's/\n+\z/\n/' "$output"
}

generate_font 16 lv_font_deck_m0_16 \
    firmware/components/application_ui/assets/lv_font_deck_m0_16.c yes
generate_font 20 lv_font_deck_ui_20 \
    firmware/components/application_ui/assets/lv_font_deck_ui_20.c yes
generate_font 32 lv_font_deck_ui_32 \
    firmware/components/application_ui/assets/lv_font_deck_ui_32.c no
