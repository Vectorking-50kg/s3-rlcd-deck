idf_component_register(
    SRCS
        "deck_application_ui.cpp"
        "deck_m0_view_model.cpp"
        "assets/lv_font_deck_m0_16.c"
    INCLUDE_DIRS "include"
    REQUIRES display esp_system esp_timer heap lvgl__lvgl
)

file(READ "${CMAKE_CURRENT_SOURCE_DIR}/assets/m0_glyphs.txt" DECK_M0_GLYPHS)
string(STRIP "${DECK_M0_GLYPHS}" DECK_M0_GLYPHS)
set(DECK_M0_GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
file(MAKE_DIRECTORY "${DECK_M0_GENERATED_DIR}")
configure_file(
    "${CMAKE_CURRENT_SOURCE_DIR}/deck_m0_glyphs.h.in"
    "${DECK_M0_GENERATED_DIR}/deck_m0_glyphs.h"
    @ONLY
)
target_include_directories(${COMPONENT_LIB} PRIVATE "${DECK_M0_GENERATED_DIR}")
target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
set_source_files_properties(
    deck_application_ui.cpp
    deck_m0_view_model.cpp
    PROPERTIES COMPILE_OPTIONS "-Wall;-Wextra;-Werror;-Wconversion;-Wsign-conversion"
)
