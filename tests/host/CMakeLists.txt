cmake_minimum_required(VERSION 3.22)

project(deck_host_tests LANGUAGES CXX)

enable_testing()
find_package(Python3 REQUIRED COMPONENTS Interpreter)

file(READ "${CMAKE_CURRENT_SOURCE_DIR}/../../firmware/components/application_ui/assets/m0_glyphs.txt" DECK_M0_GLYPHS)
string(STRIP "${DECK_M0_GLYPHS}" DECK_M0_GLYPHS)
set(DECK_M0_GENERATED_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
file(MAKE_DIRECTORY "${DECK_M0_GENERATED_DIR}")
configure_file(
    "${CMAKE_CURRENT_SOURCE_DIR}/../../firmware/components/application_ui/deck_m0_glyphs.h.in"
    "${DECK_M0_GENERATED_DIR}/deck_m0_glyphs.h"
    @ONLY
)

add_executable(
    test_boot_diagnostics
    test_boot_diagnostics.cpp
    ../../firmware/components/health/deck_boot_diagnostics.cpp
)
target_compile_features(test_boot_diagnostics PRIVATE cxx_std_17)
target_include_directories(
    test_boot_diagnostics
    PRIVATE ../../firmware/components/health/include
)

add_test(NAME boot_diagnostics COMMAND test_boot_diagnostics)

add_executable(
    test_display_service
    test_display_service.cpp
    ../../firmware/components/display/deck_display_service.cpp
)
target_compile_features(test_display_service PRIVATE cxx_std_17)
target_compile_options(test_display_service PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
target_include_directories(
    test_display_service
    PRIVATE ../../firmware/components/display/include
)
add_test(NAME display_service COMMAND test_display_service)

add_executable(
    test_m0_view_model
    test_m0_view_model.cpp
    ../../firmware/components/application_ui/deck_m0_view_model.cpp
)
target_compile_features(test_m0_view_model PRIVATE cxx_std_17)
target_compile_options(test_m0_view_model PRIVATE -Wall -Wextra -Werror -Wconversion -Wsign-conversion)
target_include_directories(
    test_m0_view_model
    PRIVATE
        ../../firmware/components/application_ui/include
        "${DECK_M0_GENERATED_DIR}"
)
add_test(NAME m0_view_model COMMAND test_m0_view_model)

add_test(
    NAME hil_boot_smoke_replay
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_boot_smoke.py
)
