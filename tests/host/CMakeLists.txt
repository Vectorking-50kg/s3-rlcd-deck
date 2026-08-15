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
    test_diagnostic_ring
    SOURCES
        test_diagnostic_ring.cpp
        ../../firmware/components/health/deck_diagnostic_ring.cpp
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
        ../../firmware/components/application_ui/deck_ai_page_view_model.cpp
        ../../firmware/components/application_ui/deck_m0_view_model.cpp
    INCLUDE_DIRS
        ../../firmware/components/ai_snapshot/include
        ../../firmware/components/application_ui/include
        "${DECK_M0_GENERATED_DIR}"
)
deck_add_host_test(
    test_ai_page_view_model
    SOURCES
        test_ai_page_view_model.cpp
        ../../firmware/components/application_ui/deck_ai_page_view_model.cpp
    INCLUDE_DIRS
        ../../firmware/components/ai_snapshot/include
        ../../firmware/components/application_ui/include
)
target_compile_definitions(
    test_ai_page_view_model
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
)
deck_add_host_test(
    test_button_input
    SOURCES
        test_button_input.cpp
        ../../firmware/components/board_support/deck_button_input.cpp
    INCLUDE_DIRS ../../firmware/components/board_support/include
)
deck_add_host_test(
    test_serial_session
    SOURCES
        test_serial_session.cpp
        ../../firmware/components/serial_service/deck_serial_session.cpp
    INCLUDE_DIRS ../../firmware/components/serial_service/include
)
deck_add_host_test(
    test_serial_router
    SOURCES
        test_serial_router.cpp
        ../../firmware/components/serial_service/deck_serial_router.cpp
    INCLUDE_DIRS ../../firmware/components/serial_service/include
)
deck_add_host_test(
    test_serial_usb_bridge
    SOURCES
        test_serial_usb_bridge.cpp
        ../../firmware/components/serial_service/deck_serial_router.cpp
        ../../firmware/components/serial_service/deck_serial_usb_bridge.cpp
    INCLUDE_DIRS ../../firmware/components/serial_service/include
)
deck_add_host_test(
    test_serial_frame
    SOURCES
        test_serial_frame.cpp
        ../../firmware/components/companion_link/deck_serial_frame.cpp
        ../../firmware/components/companion_link/deck_serial_request_tracker.cpp
    INCLUDE_DIRS ../../firmware/components/companion_link/include
)
target_compile_definitions(
    test_serial_frame
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
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
        ../../firmware/components/companion_profiles/deck_companion_profiles.cpp
        ../../firmware/components/device_settings/deck_device_settings.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
        ../../firmware/components/wifi_config/deck_wifi_config.cpp
    INCLUDE_DIRS
        ../../firmware/components/device_settings/include
        ../../firmware/components/companion_profiles/include
        ../../firmware/components/setup_mode/include
        ../../firmware/components/transaction_store/include
        ../../firmware/components/wifi_config/include
)
deck_add_host_test(
    test_wifi_config
    SOURCES
        test_wifi_config.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
        ../../firmware/components/wifi_config/deck_wifi_config.cpp
    INCLUDE_DIRS
        ../../firmware/components/transaction_store/include
        ../../firmware/components/wifi_config/include
)
deck_add_host_test(
    test_device_settings
    SOURCES
        test_device_settings.cpp
        ../../firmware/components/device_settings/deck_device_settings.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
    INCLUDE_DIRS
        ../../firmware/components/device_settings/include
        ../../firmware/components/transaction_store/include
)
deck_add_host_test(
    test_companion_profiles
    SOURCES
        test_companion_profiles.cpp
        ../../firmware/components/companion_profiles/deck_companion_profiles.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
    INCLUDE_DIRS
        ../../firmware/components/companion_profiles/include
        ../../firmware/components/transaction_store/include
)
deck_add_host_test(
    test_device_protocol
    SOURCES
        test_device_protocol.cpp
        ../../firmware/components/companion_link/deck_device_protocol.cpp
    INCLUDE_DIRS ../../firmware/components/companion_link/include
)
target_compile_definitions(
    test_device_protocol
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
)
deck_add_host_test(
    test_ai_snapshot
    SOURCES
        test_ai_snapshot.cpp
        ../../firmware/components/ai_snapshot/deck_ai_snapshot.cpp
    INCLUDE_DIRS ../../firmware/components/ai_snapshot/include
)
target_compile_definitions(
    test_ai_snapshot
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
)
deck_add_host_test(
    test_ai_snapshot_store
    SOURCES
        test_ai_snapshot_store.cpp
        ../../firmware/components/ai_snapshot/deck_ai_snapshot.cpp
        ../../firmware/components/ai_snapshot/deck_ai_snapshot_store.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
    INCLUDE_DIRS
        ../../firmware/components/ai_snapshot/include
        ../../firmware/components/transaction_store/include
)
deck_add_host_test(
    test_companion_link_timing
    SOURCES
        test_companion_link_timing.cpp
        ../../firmware/components/companion_link/deck_companion_link_timing.cpp
    INCLUDE_DIRS ../../firmware/components/companion_link/include
)
deck_add_host_test(
    test_ota_transaction
    SOURCES
        test_ota_transaction.cpp
        ../../firmware/components/ota_service/deck_ota_transaction.cpp
    INCLUDE_DIRS ../../firmware/components/ota_service/include
)
deck_add_host_test(
    test_ota_transport_fence
    SOURCES
        test_ota_transport_fence.cpp
        ../../firmware/components/ota_service/deck_ota_transport_fence.cpp
    INCLUDE_DIRS ../../firmware/components/ota_service
)
deck_add_host_test(
    test_ota_boot_health
    SOURCES
        test_ota_boot_health.cpp
        ../../firmware/components/ota_service/deck_ota_boot_health.cpp
    INCLUDE_DIRS ../../firmware/components/ota_service/include
)
deck_add_host_test(
    test_ota_signing_keys
    SOURCES
        test_ota_signing_keys.cpp
        ../../firmware/components/ota_service/deck_ota_signing_keys.cpp
    INCLUDE_DIRS ../../firmware/components/ota_service/include
)
target_compile_definitions(
    test_ota_signing_keys
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
)
deck_add_host_test(
    test_companion_failover
    SOURCES
        test_companion_failover.cpp
        ../../firmware/components/companion_link/deck_companion_failover.cpp
        ../../firmware/components/companion_link/deck_companion_transport_authority.cpp
    INCLUDE_DIRS
        ../../firmware/components/companion_link/include
        ../../firmware/components/companion_profiles/include
        ../../firmware/components/transaction_store/include
)
deck_add_host_test(
    test_companion_link_messages
    SOURCES
        test_companion_link_messages.cpp
        ../../firmware/components/ai_snapshot/deck_ai_snapshot.cpp
        ../../firmware/components/ai_snapshot/deck_ai_snapshot_store.cpp
        ../../firmware/components/companion_link/deck_companion_link_frame.cpp
        ../../firmware/components/companion_link/deck_companion_link_message.cpp
        ../../firmware/components/companion_link/deck_device_protocol.cpp
        ../../firmware/components/companion_link/deck_serial_frame.cpp
        ../../firmware/components/transaction_store/deck_transaction_store.cpp
    INCLUDE_DIRS
        ../../firmware/components/ai_snapshot/include
        ../../firmware/components/companion_link/include
        ../../firmware/components/transaction_store/include
)
target_compile_definitions(
    test_companion_link_messages
    PRIVATE DECK_REPOSITORY_ROOT="${CMAKE_CURRENT_SOURCE_DIR}/../.."
)
deck_add_host_test(
    test_setup_confirmation
    SOURCES
        test_setup_confirmation.cpp
        ../../firmware/components/setup_mode/deck_setup_confirmation.cpp
    INCLUDE_DIRS ../../firmware/components/setup_mode/include
)
deck_add_host_test(
    test_setup_command_queue
    SOURCES
        test_setup_command_queue.cpp
        ../../firmware/components/setup_mode/deck_setup_command_queue.cpp
    INCLUDE_DIRS
        ../../firmware/components/companion_profiles/include
        ../../firmware/components/setup_mode/include
        ../../firmware/components/transaction_store/include
        ../../firmware/components/wifi_config/include
)
deck_add_host_test(
    test_setup_response_barrier
    SOURCES
        test_setup_response_barrier.cpp
        ../../firmware/components/setup_mode/deck_setup_response_barrier.cpp
    INCLUDE_DIRS ../../firmware/components/setup_mode/include
)

add_test(
    NAME hil_boot_smoke_replay
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_boot_smoke.py
)
add_test(
    NAME ai_page_font_layout_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_ai_page_font_layout.py
)
add_test(
    NAME hil_wifi_transaction_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_wifi_transaction.py
)
add_test(
    NAME hil_recovery_settings_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_recovery_settings.py
)
add_test(
    NAME hil_smoke_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_smoke.py
)
add_test(
    NAME m0_acceptance_report_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_m0_acceptance_report.py
)
add_test(
    NAME hil_app_flash_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_hil_app_flash.py
)
add_test(
    NAME m1_acceptance_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_m1_acceptance.py
)
add_test(
    NAME m1_setup_client_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_m1_setup_client.py
)
add_test(
    NAME companion_native_smoke_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_smoke_companion_artifact.py
)
add_test(
    NAME serial_release_config_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_serial_release_config.py
)
add_test(
    NAME sign_ota_bundle_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_sign_ota_bundle.py
)
add_test(
    NAME companion_package_contract
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/test_package_companion.py
)
