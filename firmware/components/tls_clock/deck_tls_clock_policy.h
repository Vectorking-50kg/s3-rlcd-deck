#pragma once

#include <cstdint>

enum class DeckTlsClockDecision : uint8_t {
    reject = 0,
    keep,
    seed,
};

DeckTlsClockDecision deck_tls_clock_plan(
    int64_t current_unix_seconds,
    int64_t firmware_build_unix_seconds,
    int64_t certificate_not_before_unix_seconds,
    int64_t certificate_not_after_unix_seconds,
    int64_t *seed_unix_seconds
);

bool deck_tls_clock_trusted_utc_allowed(
    uint64_t unix_ms,
    int64_t firmware_build_unix_seconds
);
