idf_component_register(
    SRCS "deck_ai_snapshot.cpp"
    INCLUDE_DIRS "include"
)

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(${COMPONENT_LIB} PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
