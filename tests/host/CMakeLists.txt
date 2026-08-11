cmake_minimum_required(VERSION 3.22)

project(deck_host_tests LANGUAGES CXX)

enable_testing()
find_package(Python3 REQUIRED COMPONENTS Interpreter)

set(DECK_M0_GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
include("${CMAKE_CURRENT_SOURCE_DIR}/../../cmake/DeckGlyphManifest.cmake")
deck_configure_glyph_header(
    "${CMAKE_CURRENT_SOURCE_DIR}/../../firmware/components/application_ui/assets/m0_glyphs.txt"
    "${CMAKE_CURRENT_SOURCE_DIR}/../../firmware/components/application_ui/deck_m0_glyphs.h.in"
    "${DECK_M0_GENERATED_DIR}/deck_m0_glyphs.h"
)

function(deck_add_host_test target)
    cmake_parse_arguments(DECK_TEST "" "" "SOURCES;INCLUDE_DIRS" ${ARGN})
    add_executable(${target} ${DECK_TEST_SOURCES})
    target_compile_features(${target} PRIVATE cxx_std_17)
    target_compile_options(
        ${target}
        PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion
    )
    target_include_directories(${target} PRIVATE ${DECK_TEST_INCLUDE_DIRS})
    string(REGEX REPLACE "^test_" "" test_name "${target}")
    add_test(NAME ${test_name} COMMAND ${target})
endfunction()

deck_add_host_test(
    test_boot_diagnostics
    SOURCES
        test_boot_diagnostics.cpp
        ../../firmware/components/health/deck_boot_diagnostics.cpp
    INCLUDE_DIRS ../../firmware/components/health/include
)
deck_add_host_test(
    test_display_service
    SOURCES
        test_display_service.cpp
        ../../firmware/components/display/deck_display_service.cpp
    INCLUDE_DIRS ../../firmware/components/display/include
)
deck_add_host_test(
    test_m0_view_model
    SOURCES
        test_m0_view_model.cpp
        ../../firmware/components/application_ui/deck_m0_view_model.cpp
    INCLUDE_DIRS
        ../../firmware/components/application_ui/include
        "${DECK_M0_GENERATED_DIR}"
)
deck_add_host_test(
    test_button_input
    SOURCES
        test_button_input.cpp
        ../../firmware/components/board_support/deck_button_input.cpp
    INCLUDE_DIRS ../../firmware/components/board_support/include
)
deck_add_host_test(
    test_pcf85063
    SOURCES
        test_pcf85063.cpp
        ../../firmware/components/board_support/deck_pcf85063.cpp
    INCLUDE_DIRS ../../firmware/components/board_support/include
)
deck_add_host_test(
    test_shtc3
    SOURCES
        test_shtc3.cpp
        ../../firmware/components/board_support/deck_shtc3.cpp
    INCLUDE_DIRS ../../firmware/components/board_support/include
)
deck_add_host_test(
    test_peripheral_monitor
    SOURCES
        test_peripheral_monitor.cpp
        ../../firmware/components/board_support/deck_peripheral_monitor.cpp
        ../../firmware/components/board_support/deck_button_input.cpp
        ../../firmware/components/board_support/deck_pcf85063.cpp
        ../../firmware/components/board_support/deck_shtc3.cpp
    INCLUDE_DIRS ../../firmware/components/board_support/include
)
deck_add_host_test(
    test_setup_mode
    SOURCES
        test_setup_mode.cpp
        ../../firmware/components/setup_mode/deck_setup_mode.cpp
        ../../firmware/components/setup_mode/deck_setup_http.cpp
        ../../firmware/components/wifi_config/deck_wifi_config.cpp
    INCLUDE_DIRS
        ../../firmware/components/setup_mode/include
        ../../firmware/components/wifi_config/include
)
deck_add_host_test(
    test_wifi_config
    SOURCES
        test_wifi_config.cpp
        ../../firmware/components/wifi_config/deck_wifi_config.cpp
    INCLUDE_DIRS ../../firmware/components/wifi_config/include
)

add_test(
    NAME hil_boot_smoke_replay
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_boot_smoke.py
)
add_test(
    NAME hil_wifi_transaction_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_wifi_transaction.py
)
