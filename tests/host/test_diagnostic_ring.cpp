#include "deck_diagnostic_ring.h"

#include <cstdlib>
#include <cstring>
#include <iostream>

namespace {

void require(bool condition, const char *message)
{
    if (!condition) {
        std::cerr << "FAIL: " << message << '\n';
        std::exit(1);
    }
}

}  // namespace

int main()
{
    deck_diagnostic_ring_reset();
    require(
        !deck_diagnostic_ring_record(
            0,
            static_cast<deck_diagnostic_level_t>(99),
            DECK_DIAGNOSTIC_COMPONENT_SYSTEM,
            DECK_DIAGNOSTIC_CODE_BOOT,
            0
        ),
        "invalid enum must fail closed"
    );
    require(
        deck_diagnostic_ring_record(
            100,
            DECK_DIAGNOSTIC_LEVEL_INFO,
            DECK_DIAGNOSTIC_COMPONENT_SYSTEM,
            DECK_DIAGNOSTIC_CODE_BOOT,
            0
        ) &&
            deck_diagnostic_ring_record(
                50,
                DECK_DIAGNOSTIC_LEVEL_WARNING,
                DECK_DIAGNOSTIC_COMPONENT_SYSTEM,
                DECK_DIAGNOSTIC_CODE_TIMEOUT,
                0
            ),
        "concurrent late observations must remain accepted"
    );
    deck_diagnostic_snapshot_t concurrent_snapshot{};
    deck_diagnostic_ring_snapshot(&concurrent_snapshot);
    require(
        concurrent_snapshot.count == 2U &&
            concurrent_snapshot.events[1].monotonic_ms == 100U,
        "late observation must be clamped under the ring lock"
    );
    char concurrent_document[1024]{};
    size_t concurrent_document_size = 0;
    require(
        deck_diagnostic_snapshot_format(
            &concurrent_snapshot,
            1,
            concurrent_document,
            sizeof(concurrent_document),
            &concurrent_document_size
        ),
        "every accepted concurrent ring must remain formattable"
    );

    deck_diagnostic_ring_reset();
    for (size_t index = 0; index < DECK_DIAGNOSTIC_RING_CAPACITY + 7U; ++index) {
        require(
            deck_diagnostic_ring_record(
                static_cast<uint64_t>(index),
                index % 2U == 0 ? DECK_DIAGNOSTIC_LEVEL_INFO
                                : DECK_DIAGNOSTIC_LEVEL_WARNING,
                DECK_DIAGNOSTIC_COMPONENT_WIFI,
                DECK_DIAGNOSTIC_CODE_READY,
                static_cast<uint32_t>(index)
            ),
            "valid event was rejected"
        );
    }
    deck_diagnostic_snapshot_t snapshot{};
    deck_diagnostic_ring_snapshot(&snapshot);
    require(snapshot.count == DECK_DIAGNOSTIC_RING_CAPACITY, "ring count must be fixed");
    require(snapshot.dropped == 7U, "overwrites must be counted");
    require(snapshot.events[0].monotonic_ms == 7U, "snapshot must be chronological");
    require(
        snapshot.events[DECK_DIAGNOSTIC_RING_CAPACITY - 1U].value ==
            DECK_DIAGNOSTIC_RING_CAPACITY + 6U,
        "snapshot must retain newest event"
    );
    require(
        std::strcmp(deck_diagnostic_component_name(DECK_DIAGNOSTIC_COMPONENT_DEVICE_LINK),
                    "device_link") == 0,
        "component wire name changed"
    );
    require(
        std::strcmp(deck_diagnostic_code_name(DECK_DIAGNOSTIC_CODE_PROTOCOL_ERROR),
                    "protocol_error") == 0,
        "code wire name changed"
    );
    char document[16 * 1024]{};
    size_t document_size = 0;
    require(
        deck_diagnostic_snapshot_format(
            &snapshot,
            17,
            document,
            sizeof(document),
            &document_size
        ),
        "snapshot formatting failed"
    );
    require(document_size < sizeof(document), "snapshot exceeded Device Link bound");
    require(
        std::strstr(document, "\"type\":\"diagnostics.snapshot\"") != nullptr &&
            std::strstr(document, "Authorization") == nullptr,
        "snapshot wire document is not fixed and redacted"
    );
    std::cout << "diagnostic ring tests passed\n";
    return 0;
}
