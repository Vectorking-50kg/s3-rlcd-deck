idf_component_register(
    SRCS
        "deck_ota_boot_health.cpp"
        "deck_ota_protocol.cpp"
        "deck_ota_signing_keys.cpp"
        "deck_ota_service.cpp"
        "deck_ota_transaction.cpp"
    INCLUDE_DIRS "include"
    REQUIRES
        app_update
        espressif__cjson
        esp_app_format
        esp_partition
        esp_timer
        freertos
        mbedtls
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
