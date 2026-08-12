idf_component_register(
    SRCS
        "deck_peripheral_monitor.cpp"
        "deck_peripherals.cpp"
        "deck_button_input.cpp"
        "deck_pcf85063.cpp"
        "deck_rlcd_panel.cpp"
        "deck_shtc3.cpp"
    INCLUDE_DIRS "include"
    REQUIRES display esp_driver_gpio esp_driver_i2c esp_driver_spi esp_lcd esp_timer
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
