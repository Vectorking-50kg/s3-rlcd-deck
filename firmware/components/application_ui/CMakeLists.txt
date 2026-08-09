idf_component_register(
    SRCS
        "deck_application_ui.cpp"
        "deck_m0_view_model.cpp"
        "assets/lv_font_deck_m0_16.c"
    INCLUDE_DIRS "include"
    REQUIRES display esp_system esp_timer heap lvgl__lvgl
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
set_source_files_properties(
    deck_application_ui.cpp
    deck_m0_view_model.cpp
    PROPERTIES COMPILE_OPTIONS "-Wall;-Wextra;-Werror;-Wconversion;-Wsign-conversion"
)
