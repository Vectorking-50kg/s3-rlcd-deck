idf_component_register(
    SRCS
        "deck_lan_pairing.cpp"
        "deck_pairing_v2_contract.cpp"
        "deck_pairing_v2_transaction.cpp"
    INCLUDE_DIRS "include"
    REQUIRES
        companion_link
        companion_profiles
        esp_hw_support
        esp_timer
        espressif__esp_websocket_client
        freertos
        mbedtls
        mdns
        protocomm
        tls_clock
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(
    ${COMPONENT_LIB}
    PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion -Wframe-larger-than=1536
)
