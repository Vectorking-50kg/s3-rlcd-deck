cmake_minimum_required(VERSION 3.22)

project(deck_host_tests LANGUAGES CXX)

enable_testing()
find_package(Python3 REQUIRED COMPONENTS Interpreter)

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
add_test(
    NAME hil_boot_smoke_replay
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_boot_smoke.py
)
