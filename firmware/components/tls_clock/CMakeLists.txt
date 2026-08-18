idf_component_register(
    SRCS
        "deck_tls_clock.cpp"
        "deck_tls_clock_policy.cpp"
    INCLUDE_DIRS "include"
    PRIV_INCLUDE_DIRS "."
    REQUIRES mbedtls
)

if(NOT DEFINED DECK_FIRMWARE_BUILD_UNIX OR
   NOT DECK_FIRMWARE_BUILD_UNIX MATCHES "^[0-9]+$")
    set(DECK_FIRMWARE_BUILD_UNIX 1704067200)
endif()

target_compile_features(${COMPONENT_LIB} PRIVATE cxx_std_17)
target_compile_options(
    ${COMPONENT_LIB}
    PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion
)
target_compile_definitions(
    ${COMPONENT_LIB}
    PRIVATE DECK_FIRMWARE_BUILD_UNIX=${DECK_FIRMWARE_BUILD_UNIX}
)
