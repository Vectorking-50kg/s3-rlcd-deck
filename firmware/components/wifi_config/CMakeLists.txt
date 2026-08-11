idf_component_register(
    SRCS
        "deck_wifi_config.cpp"
        "deck_wifi_config_nvs.cpp"
    INCLUDE_DIRS "include"
    REQUIRES nvs_flash
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
