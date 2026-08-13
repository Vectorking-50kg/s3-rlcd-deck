#include "deck_setup_response_barrier.h"

#include <memory>
#include <mutex>
#include <new>

struct deck_setup_response_barrier {
    struct Slot {
        uint32_t generation = 0;
        bool response_sent = false;
        bool complete = false;
    };

    mutable std::mutex mutex;
    size_t capacity;
    std::unique_ptr<Slot[]> slots;
    uint32_t next_generation = 0;
};

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
    delete barrier;
}

uint32_t deck_setup_response_barrier_issue(deck_setup_response_barrier_t *barrier)
{
    if (barrier == nullptr) {
        return 0;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation == 0) {
            ++barrier->next_generation;
            if (barrier->next_generation == 0) {
                ++barrier->next_generation;
            }
            barrier->slots[index] = {barrier->next_generation, false, false};
            return barrier->next_generation;
        }
    }
    return 0;
}

void deck_setup_response_barrier_response_sent(
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
            barrier->slots[index].response_sent = true;
            return;
        }
    }
}

void deck_setup_response_barrier_complete(
    deck_setup_response_barrier_t *barrier,
    uint32_t generation
)
{
    if (barrier == nullptr || generation == 0) {
        return;
    }
    const std::lock_guard<std::mutex> lock(barrier->mutex);
    for (size_t index = 0; index < barrier->capacity; ++index) {
        if (barrier->slots[index].generation == generation &&
            barrier->slots[index].response_sent) {
            barrier->slots[index].complete = true;
            return;
        }
    }
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
            barrier->slots[index] = {};
            return;
        }
    }
}
