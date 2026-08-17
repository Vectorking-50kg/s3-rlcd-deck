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
        esp_hal_usb
        esp_hw_support
        esp_psram
        esp_rom
        esp_system
        esp_timer
        esp_wifi
        freertos
        health
        heap
        lan_pairing
        ota_service
        setup_mode
        serial_service
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(
    ${COMPONENT_LIB}
    PRIVATE
        -Wall
        -Wextra
        -Werror
        -Wframe-larger-than=2048
)

if(DEFINED DECK_FIRMWARE_COMMIT)
    string(LENGTH "${DECK_FIRMWARE_COMMIT}" DECK_FIRMWARE_COMMIT_LENGTH)
endif()
if(NOT DEFINED DECK_FIRMWARE_COMMIT OR
   NOT DECK_FIRMWARE_COMMIT_LENGTH EQUAL 40 OR
   NOT DECK_FIRMWARE_COMMIT MATCHES "^[0-9a-f]+$")
    set(DECK_FIRMWARE_COMMIT "unknown")
endif()
target_compile_definitions(
    ${COMPONENT_LIB}
    PRIVATE DECK_FIRMWARE_COMMIT="${DECK_FIRMWARE_COMMIT}"
)
