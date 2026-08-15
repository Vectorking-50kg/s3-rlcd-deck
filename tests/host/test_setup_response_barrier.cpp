#include "deck_setup_response_barrier.h"

#include <cassert>
#include <cstring>

int main()
{
    deck_setup_response_barrier_t *barrier = deck_setup_response_barrier_create(8);
    assert(barrier != nullptr);
    constexpr uint32_t first_client = 0x01020304;
    constexpr uint32_t second_client = 0x05060708;
    uint8_t first_ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
    uint8_t second_ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
    std::memset(second_ack, 0x5a, sizeof(second_ack));
    const uint32_t first = deck_setup_response_barrier_issue(
        barrier,
        first_client,
        first_ack
    );
    const uint32_t second = deck_setup_response_barrier_issue(
        barrier,
        second_client,
        second_ack
    );
    assert(first != 0);
    assert(second > first);
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    // Only an explicit client acknowledgement can complete its generation.
    uint8_t wrong_ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
    std::memset(wrong_ack, 0xa5, sizeof(wrong_ack));
    assert(!deck_setup_response_barrier_acknowledge(barrier, first_client, wrong_ack));
    assert(!deck_setup_response_barrier_acknowledge(barrier, first_client, second_ack));
    assert(!deck_setup_response_barrier_is_complete(barrier, first));
    assert(!deck_setup_response_barrier_is_complete(barrier, second));
    assert(deck_setup_response_barrier_acknowledge(barrier, first_client, first_ack));
    assert(deck_setup_response_barrier_is_complete(barrier, first));
    // A capability is consumed atomically by its first successful acknowledgement.
    assert(!deck_setup_response_barrier_acknowledge(barrier, first_client, first_ack));
    assert(deck_setup_response_barrier_acknowledge(barrier, second_client, second_ack));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    uint32_t generations[6]{};
    for (size_t index = 0; index < sizeof(generations) / sizeof(generations[0]); ++index) {
        uint8_t ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
        ack[0] = static_cast<uint8_t>(index + 1);
        generations[index] = deck_setup_response_barrier_issue(
            barrier,
            first_client,
            ack
        );
        const uint32_t generation = generations[index];
        assert(generation != 0);
    }
    assert(deck_setup_response_barrier_issue(barrier, first_client, wrong_ack) == 0);
    deck_setup_response_barrier_release(barrier, first);
    const uint32_t reused = deck_setup_response_barrier_issue(
        barrier,
        first_client,
        wrong_ack
    );
    assert(reused != 0);
    assert(deck_setup_response_barrier_acknowledge(barrier, first_client, wrong_ack));
    assert(deck_setup_response_barrier_is_complete(barrier, reused));
    assert(deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_release(barrier, second);
    assert(!deck_setup_response_barrier_is_complete(barrier, second));
    deck_setup_response_barrier_destroy(barrier);
    return 0;
}
