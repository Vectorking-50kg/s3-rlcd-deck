idf_component_register(
    SRCS
        "deck_setup_mode.cpp"
        "deck_setup_http.cpp"
        "deck_setup_service.cpp"
    INCLUDE_DIRS "include"
    REQUIRES esp_event esp_http_server esp_netif esp_system esp_timer esp_wifi nvs_flash
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
