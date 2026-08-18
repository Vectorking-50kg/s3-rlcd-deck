#include "deck_tls_clock_policy.h"

#include <cassert>
#include <cstdint>

int main()
{
    constexpr int64_t build = 1'777'000'000;
    constexpr int64_t not_before = 1'776'000'000;
    constexpr int64_t not_after = 2'092'000'000;
    int64_t seed = 0;

    assert(deck_tls_clock_plan(0, build, not_before, not_after, &seed) ==
           DeckTlsClockDecision::seed);
    assert(seed == build);

    seed = 0;
    assert(deck_tls_clock_plan(0, build, build + 60, not_after, &seed) ==
           DeckTlsClockDecision::seed);
    assert(seed == build + 60);

    assert(deck_tls_clock_plan(build + 120, build, not_before, not_after, &seed) ==
           DeckTlsClockDecision::keep);
    assert(deck_tls_clock_plan(build + 120, build, build + 180, not_after, &seed) ==
           DeckTlsClockDecision::reject);
    assert(deck_tls_clock_plan(not_after + 1, build, not_before, not_after, &seed) ==
           DeckTlsClockDecision::reject);
    assert(deck_tls_clock_plan(0, not_after + 1, not_before, not_after, &seed) ==
           DeckTlsClockDecision::reject);
    assert(deck_tls_clock_plan(0, build, not_after, not_before, &seed) ==
           DeckTlsClockDecision::reject);

    assert(deck_tls_clock_trusted_utc_allowed(
        static_cast<uint64_t>(build) * 1'000U,
        build
    ));
    assert(!deck_tls_clock_trusted_utc_allowed(
        static_cast<uint64_t>(build - 1) * 1'000U,
        build
    ));
    return 0;
}
