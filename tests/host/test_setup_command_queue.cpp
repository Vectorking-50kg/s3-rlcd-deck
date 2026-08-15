#include "deck_setup_command_queue.h"

#include <array>
#include <atomic>
#include <cassert>
#include <cstring>
#include <thread>

namespace {

deck_setup_command_t companion_command(size_t index)
{
    deck_setup_command_t command{};
    command.type = index % 4 == 0
                       ? DECK_SETUP_COMMAND_PAIR_COMPANION
                       : index % 4 == 1
                             ? DECK_SETUP_COMMAND_SELECT_COMPANION
                             : index % 4 == 2
                                   ? DECK_SETUP_COMMAND_SET_COMPANION_PRIORITY
                                   : DECK_SETUP_COMMAND_REVOKE_COMPANION;
    const char *profile =
        "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    std::memcpy(command.companion_profile_id, profile, std::strlen(profile) + 1);
    if (command.type == DECK_SETUP_COMMAND_PAIR_COMPANION) {
        std::memcpy(command.companion_pair.code, "123456", 7);
    }
    command.temperature_offset_tenths_c = static_cast<int16_t>(index);
    command.companion_priority = static_cast<int32_t>(index) - 400;
    return command;
}

void concurrent_setup_producers_use_the_production_bounded_queue()
{
    deck_setup_command_queue_t *queue = deck_setup_command_queue_create();
    assert(queue != nullptr);
    constexpr size_t kProducerCount = 4;
    constexpr size_t kCommandsPerProducer = 200;
    constexpr size_t kTotal = kProducerCount * kCommandsPerProducer;
    std::atomic<size_t> consumed{0};
    std::array<std::thread, kProducerCount> producers;
    std::thread consumer([&]() {
        while (consumed.load() != kTotal) {
            deck_setup_command_t command{};
            if (!deck_setup_command_queue_try_receive(queue, &command)) {
                std::this_thread::yield();
                continue;
            }
            assert(command.type >= DECK_SETUP_COMMAND_PAIR_COMPANION);
            assert(command.type <= DECK_SETUP_COMMAND_REVOKE_COMPANION);
            if (command.type == DECK_SETUP_COMMAND_PAIR_COMPANION) {
                assert(std::strcmp(command.companion_pair.code, "123456") == 0);
            }
            if (command.type == DECK_SETUP_COMMAND_SET_COMPANION_PRIORITY) {
                assert(command.companion_priority >= -400);
                assert(command.companion_priority < 400);
            }
            deck_setup_command_clear(&command);
            ++consumed;
        }
    });
    for (size_t producer = 0; producer < kProducerCount; ++producer) {
        producers[producer] = std::thread([&, producer]() {
            for (size_t index = 0; index < kCommandsPerProducer; ++index) {
                deck_setup_command_t command = companion_command(
                    producer * kCommandsPerProducer + index
                );
                while (!deck_setup_command_queue_try_send(queue, &command)) {
                    std::this_thread::yield();
                }
                deck_setup_command_clear(&command);
            }
        });
    }
    for (std::thread &producer : producers) {
        producer.join();
    }
    consumer.join();
    assert(consumed.load() == kTotal);
    deck_setup_command_t empty{};
    assert(!deck_setup_command_queue_try_receive(queue, &empty));
    deck_setup_command_queue_destroy(queue);
}

void full_queue_fails_closed_without_overwriting_secrets()
{
    deck_setup_command_queue_t *queue = deck_setup_command_queue_create();
    for (size_t index = 0; index < DECK_SETUP_COMMAND_QUEUE_CAPACITY; ++index) {
        const deck_setup_command_t command = companion_command(index);
        assert(deck_setup_command_queue_try_send(queue, &command));
    }
    deck_setup_command_t overflow = companion_command(99);
    assert(!deck_setup_command_queue_try_send(queue, &overflow));
    deck_setup_command_clear(&overflow);
    for (size_t index = 0; index < DECK_SETUP_COMMAND_QUEUE_CAPACITY; ++index) {
        deck_setup_command_t command{};
        assert(deck_setup_command_queue_try_receive(queue, &command));
        assert(command.temperature_offset_tenths_c == static_cast<int16_t>(index));
        deck_setup_command_clear(&command);
    }
    deck_setup_command_queue_destroy(queue);
}

}  // namespace

int main()
{
    concurrent_setup_producers_use_the_production_bounded_queue();
    full_queue_fails_closed_without_overwriting_secrets();
    return 0;
}
