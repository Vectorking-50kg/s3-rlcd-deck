idf_component_register(
    SRCS "deck_rlcd_panel.cpp"
    INCLUDE_DIRS "include"
    REQUIRES display esp_driver_gpio esp_driver_spi esp_lcd
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
