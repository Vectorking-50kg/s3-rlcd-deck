#include "deck_setup_response_barrier.h"

#include <memory>
#include <mutex>
#include <new>
#include <cstring>

struct deck_setup_response_barrier {
    struct Slot {
        uint32_t generation = 0;
        uint32_t client_ipv4 = 0;
        uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]{};
        bool complete = false;
    };

    mutable std::mutex mutex;
    size_t capacity;
    std::unique_ptr<Slot[]> slots;
    uint32_t next_generation = 0;
};

namespace {

void clear_slot(deck_setup_response_barrier::Slot *slot)
{
    if (slot == nullptr) {
        return;
    }
    auto *bytes = reinterpret_cast<volatile uint8_t *>(slot);
    for (size_t index = 0; index < sizeof(*slot); ++index) {
        bytes[index] = 0;
    }
}

}  // namespace

deck_setup_response_barrier_t *deck_setup_response_barrier_create(size_t capacity)
{
    if (capacity == 0) {
        return nullptr;
    }
    auto *barrier = new (std::nothrow) deck_setup_response_barrier_t{};
    if (barrier == nullptr) {
        return nullptr;
    }
    barrier->capacity = capacity;
    barrier->slots.reset(new (std::nothrow) deck_setup_response_barrier::Slot[capacity]{});
    if (!barrier->slots) {
        delete barrier;
        return nullptr;
    }
    return barrier;
}

void deck_setup_response_barrier_destroy(deck_setup_response_barrier_t *barrier)
{
    if (barrier != nullptr) {
        for (size_t index = 0; index < barrier->capacity; ++index) {
            clear_slot(&barrier->slots[index]);
        }
    }
    delete barrier;
}

uint32_t deck_setup_response_barrier_issue(
    deck_setup_response_barrier_t *barrier,
    uint32_t client_ipv4,
    const uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]
)
{
    if (barrier == nullptr || response_ack == nullptr) {
        return 0;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation != 0 &&
            std::memcmp(
                barrier->slots[index].response_ack,
                response_ack,
                DECK_SETUP_RESPONSE_ACK_SIZE
            ) == 0) {
            return 0;
        }
    }
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation == 0) {
            ++barrier->next_generation;
            if (barrier->next_generation == 0) {
                ++barrier->next_generation;
            }
            barrier->slots[index].generation = barrier->next_generation;
            barrier->slots[index].client_ipv4 = client_ipv4;
            std::memcpy(
                barrier->slots[index].response_ack,
                response_ack,
                DECK_SETUP_RESPONSE_ACK_SIZE
            );
            return barrier->next_generation;
        }
    }
    return 0;
}

bool deck_setup_response_barrier_acknowledge(
    deck_setup_response_barrier_t *barrier,
    uint32_t client_ipv4,
    const uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]
)
{
    if (barrier == nullptr || response_ack == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        uint8_t difference = 0;
        for (size_t byte = 0; byte < DECK_SETUP_RESPONSE_ACK_SIZE; ++byte) {
            difference |= static_cast<uint8_t>(
                barrier->slots[index].response_ack[byte] ^ response_ack[byte]
            );
        }
        if (barrier->slots[index].generation != 0 &&
            !barrier->slots[index].complete &&
            barrier->slots[index].client_ipv4 == client_ipv4 &&
            difference == 0) {
            barrier->slots[index].complete = true;
            return true;
        }
    }
    return false;
}

bool deck_setup_response_barrier_is_complete(
    const deck_setup_response_barrier_t *barrier,
    uint32_t generation
)
{
    if (barrier == nullptr || generation == 0) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation == generation) {
            return barrier->slots[index].complete;
        }
    }
    return false;
}

void deck_setup_response_barrier_release(
    deck_setup_response_barrier_t *barrier,
    uint32_t generation
)
{
    if (barrier == nullptr || generation == 0) {
        return;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation == generation) {
            clear_slot(&barrier->slots[index]);
            return;
        }
    }
}
