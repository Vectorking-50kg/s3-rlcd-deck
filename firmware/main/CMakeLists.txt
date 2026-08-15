idf_component_register(
    SRCS "app_main.cpp"
    INCLUDE_DIRS "."
    PRIV_REQUIRES
        application_ui
        board_support
        companion_link
        device_settings
        display
        esp_app_format
        esp_driver_usb_serial_jtag
        esp_psram
        esp_system
        esp_timer
        esp_wifi
        freertos
        health
        heap
        setup_mode
        serial_service
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror)
