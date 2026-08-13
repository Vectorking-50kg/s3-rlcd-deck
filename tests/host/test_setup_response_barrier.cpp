#include "deck_setup_response_barrier.h"

#include <cassert>

int main()
{
    deck_setup_response_barrier_t *barrier = deck_setup_response_barrier_create(8);
    assert(barrier != nullptr);
    const uint32_t first = deck_setup_response_barrier_issue(barrier);
    const uint32_t second = deck_setup_response_barrier_issue(barrier);
    assert(first != 0);
    assert(second > first);
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    deck_setup_response_barrier_complete(barrier, second);
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_complete(barrier, first);
    assert(deck_setup_response_barrier_is_complete(barrier, first));
    uint32_t generations[6]{};
    for (uint32_t &generation : generations) {
        generation = deck_setup_response_barrier_issue(barrier);
        assert(generation != 0);
    }
    assert(deck_setup_response_barrier_issue(barrier) == 0);
    deck_setup_response_barrier_release(barrier, first);
    const uint32_t reused = deck_setup_response_barrier_issue(barrier);
    assert(reused != 0);
    deck_setup_response_barrier_complete(barrier, reused);
    assert(deck_setup_response_barrier_is_complete(barrier, reused));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_release(barrier, second);
    assert(!deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_destroy(barrier);
    return 0;
}
