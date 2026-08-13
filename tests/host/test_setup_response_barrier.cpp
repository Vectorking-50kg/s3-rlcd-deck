#include "deck_setup_response_barrier.h"

#include <cassert>

int main()
{
    deck_setup_response_barrier_t *barrier = deck_setup_response_barrier_create(8);
    assert(barrier != nullptr);
    constexpr uint32_t first_client = 0x01020304;
    constexpr uint32_t second_client = 0x05060708;
    const uint32_t first = deck_setup_response_barrier_issue(barrier, first_client);
    const uint32_t second = deck_setup_response_barrier_issue(barrier, second_client);
    assert(first != 0);
    assert(second > first);
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    // Only an explicit client acknowledgement can complete its generation.
    assert(!deck_setup_response_barrier_acknowledge(barrier, 0, first_client));
    assert(!deck_setup_response_barrier_acknowledge(barrier, second + 99, second_client));
    assert(!deck_setup_response_barrier_acknowledge(barrier, second, first_client));
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    assert(!deck_setup_response_barrier_is_complete(barrier, second));
    assert(deck_setup_response_barrier_acknowledge(barrier, first, first_client));
    assert(deck_setup_response_barrier_is_complete(barrier, first));
    assert(deck_setup_response_barrier_acknowledge(barrier, second, second_client));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    uint32_t generations[6]{};
    for (uint32_t &generation : generations) {
        generation = deck_setup_response_barrier_issue(barrier, first_client);
        assert(generation != 0);
    }
    assert(deck_setup_response_barrier_issue(barrier, first_client) == 0);
    deck_setup_response_barrier_release(barrier, first);
    const uint32_t reused = deck_setup_response_barrier_issue(barrier, first_client);
    assert(reused != 0);
    assert(deck_setup_response_barrier_acknowledge(barrier, reused, first_client));
    assert(deck_setup_response_barrier_is_complete(barrier, reused));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_release(barrier, second);
    assert(!deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_destroy(barrier);
    return 0;
}
