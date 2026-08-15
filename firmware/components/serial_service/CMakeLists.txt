idf_component_register(
    SRCS
        "deck_serial_session.cpp"
        "deck_serial_router.cpp"
        "deck_serial_service.cpp"
    INCLUDE_DIRS "include"
    REQUIRES esp_driver_gpio esp_driver_uart esp_timer freertos heap
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
