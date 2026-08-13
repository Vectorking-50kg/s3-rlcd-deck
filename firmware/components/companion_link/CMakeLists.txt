idf_component_register(
    SRCS
        "deck_companion_link.cpp"
        "deck_companion_link_timing.cpp"
        "deck_device_protocol.cpp"
    INCLUDE_DIRS "include"
    REQUIRES
        companion_profiles
        esp_timer
        espressif__esp_websocket_client
        freertos
        mbedtls
        pthread
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
