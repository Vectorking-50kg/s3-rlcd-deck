#include "deck_tls_clock_policy.h"

#include <climits>

DeckTlsClockDecision deck_tls_clock_plan(
    int64_t current_unix_seconds,
    int64_t firmware_build_unix_seconds,
    int64_t certificate_not_before_unix_seconds,
    int64_t certificate_not_after_unix_seconds,
    int64_t *seed_unix_seconds
)
{
    if (seed_unix_seconds != nullptr) {
        *seed_unix_seconds = 0;
    }
    if (seed_unix_seconds == nullptr || firmware_build_unix_seconds <= 0 ||
        certificate_not_before_unix_seconds <= 0 ||
        certificate_not_after_unix_seconds < certificate_not_before_unix_seconds ||
        firmware_build_unix_seconds > certificate_not_after_unix_seconds) {
        return DeckTlsClockDecision::reject;
    }

    if (current_unix_seconds >= firmware_build_unix_seconds) {
        return current_unix_seconds >= certificate_not_before_unix_seconds &&
                       current_unix_seconds <= certificate_not_after_unix_seconds
                   ? DeckTlsClockDecision::keep
                   : DeckTlsClockDecision::reject;
    }

    const int64_t seed = firmware_build_unix_seconds > certificate_not_before_unix_seconds
                             ? firmware_build_unix_seconds
                             : certificate_not_before_unix_seconds;
    if (seed > certificate_not_after_unix_seconds) {
        return DeckTlsClockDecision::reject;
    }
    *seed_unix_seconds = seed;
    return DeckTlsClockDecision::seed;
}

bool deck_tls_clock_trusted_utc_allowed(
    uint64_t unix_ms,
    int64_t firmware_build_unix_seconds
)
{
    if (firmware_build_unix_seconds <= 0 || unix_ms > static_cast<uint64_t>(INT64_MAX)) {
        return false;
    }
    return unix_ms / 1'000U >= static_cast<uint64_t>(firmware_build_unix_seconds);
}
