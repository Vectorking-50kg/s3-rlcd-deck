idf_component_register(
    SRCS
        "deck_application_ui.cpp"
        "deck_ai_page_view_model.cpp"
        "deck_m0_view_model.cpp"
        "deck_ui_layout.cpp"
        "deck_ui_preview.cpp"
        "deck_ui_renderer.cpp"
        "deck_ui_scene.cpp"
        "assets/lv_font_deck_m0_16.c"
        "assets/lv_font_deck_ui_20.c"
        "assets/lv_font_deck_ui_32.c"
    INCLUDE_DIRS "include"
    REQUIRES ai_snapshot display esp_common esp_system esp_timer heap lvgl__lvgl
)

set(DECK_M0_GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
include("${CMAKE_CURRENT_SOURCE_DIR}/../../../cmake/DeckGlyphManifest.cmake")
deck_configure_glyph_header(
    "${CMAKE_CURRENT_SOURCE_DIR}/assets/m0_glyphs.txt"
    "${CMAKE_CURRENT_SOURCE_DIR}/deck_m0_glyphs.h.in"
    "${DECK_M0_GENERATED_DIR}/deck_m0_glyphs.h"
)
target_include_directories(${COMPONENT_LIB} PRIVATE "${DECK_M0_GENERATED_DIR}")
target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
set_source_files_properties(
    deck_application_ui.cpp
    deck_ai_page_view_model.cpp
    deck_m0_view_model.cpp
    deck_ui_layout.cpp
    deck_ui_preview.cpp
    deck_ui_renderer.cpp
    deck_ui_scene.cpp
    PROPERTIES COMPILE_OPTIONS "-Wall;-Wextra;-Werror;-Wconversion;-Wsign-conversion"
)
