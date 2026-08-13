#include "deck_setup_command_queue.h"

#include <mutex>
#include <new>

struct deck_setup_command_queue {
    std::mutex mutex;
    deck_setup_command_t items[DECK_SETUP_COMMAND_QUEUE_CAPACITY]{};
    size_t head = 0;
    size_t tail = 0;
    size_t count = 0;
};

void deck_setup_command_clear(deck_setup_command_t *command)
{
    if (command == nullptr) {
        return;
    }
    auto *bytes = reinterpret_cast<volatile uint8_t *>(command);
    for (size_t index = 0; index < sizeof(*command); ++index) {
        bytes[index] = 0;
    }
}

deck_setup_command_queue_t *deck_setup_command_queue_create(void)
{
    return new (std::nothrow) deck_setup_command_queue_t{};
}

void deck_setup_command_queue_destroy(deck_setup_command_queue_t *queue)
{
    if (queue == nullptr) {
        return;
    }
    for (deck_setup_command_t &command : queue->items) {
        deck_setup_command_clear(&command);
    }
    delete queue;
}

bool deck_setup_command_queue_try_send(
    deck_setup_command_queue_t *queue,
    const deck_setup_command_t *command
)
{
    if (queue == nullptr || command == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(queue->mutex);
    if (queue->count == DECK_SETUP_COMMAND_QUEUE_CAPACITY) {
        return false;
    }
    queue->items[queue->tail] = *command;
    queue->tail = (queue->tail + 1) % DECK_SETUP_COMMAND_QUEUE_CAPACITY;
    ++queue->count;
    return true;
}

bool deck_setup_command_queue_try_receive(
    deck_setup_command_queue_t *queue,
    deck_setup_command_t *command
)
{
    if (queue == nullptr || command == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(queue->mutex);
    if (queue->count == 0) {
        return false;
    }
    *command = queue->items[queue->head];
    deck_setup_command_clear(&queue->items[queue->head]);
    queue->head = (queue->head + 1) % DECK_SETUP_COMMAND_QUEUE_CAPACITY;
    --queue->count;
    return true;
}
