idf_component_register(
    SRCS "deck_companion_profiles.cpp" "deck_companion_profiles_nvs.cpp"
    INCLUDE_DIRS "include"
    REQUIRES pthread transaction_store
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
