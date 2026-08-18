idf_component_register(
    SRCS
        "deck_lan_pairing.cpp"
        "deck_pairing_v2_contract.cpp"
    INCLUDE_DIRS "include"
    REQUIRES esp_hw_support freertos mdns protocomm
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(
    ${COMPONENT_LIB}
    PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion -Wframe-larger-than=1536
)
