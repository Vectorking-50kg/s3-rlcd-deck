idf_component_register(
    SRCS "deck_device_settings.cpp" "deck_device_settings_nvs.cpp"
    INCLUDE_DIRS "include"
    REQUIRES transaction_store
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
