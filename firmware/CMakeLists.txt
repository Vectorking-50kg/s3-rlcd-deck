cmake_minimum_required(VERSION 3.22)

include($ENV{IDF_PATH}/tools/cmake/project.cmake)

idf_build_set_property(MINIMAL_BUILD ON)
set(PROJECT_VER "0.2.0-dev")
project(s3_rlcd_deck)

# Device Link carries a bearer credential in its WebSocket upgrade request. ESP-IDF's
# tcp_transport WebSocket implementation logs that complete request on an ordinary
# handshake write error, so tag filtering is too late to form a credential boundary.
# Compile every tcp_transport log call out; Deck-owned connection diagnostics remain
# available and are deliberately redacted.
idf_component_get_property(deck_tcp_transport tcp_transport COMPONENT_LIB)
target_compile_definitions(${deck_tcp_transport} PRIVATE LOG_LOCAL_LEVEL=ESP_LOG_NONE)
