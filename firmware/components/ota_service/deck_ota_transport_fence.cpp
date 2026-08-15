#include "deck_ota_transport_fence.h"

uint32_t DeckOtaTransportFence::capture_epoch() const
{
    return epoch_of(word_.load(std::memory_order_acquire));
}

bool DeckOtaTransportFence::accepts(uint32_t command_epoch) const
{
    return command_epoch == capture_epoch();
}

bool DeckOtaTransportFence::begin_transaction(uint32_t command_epoch)
{
    uint32_t current = word_.load(std::memory_order_acquire);
    if (epoch_of(current) != command_epoch || state_of(current) != State::idle) {
        return false;
    }
    return word_.compare_exchange_strong(
        current,
        encode(command_epoch, State::active),
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}

bool DeckOtaTransportFence::try_begin_commit()
{
    uint32_t current = word_.load(std::memory_order_acquire);
    if (state_of(current) != State::active) {
        return false;
    }
    return word_.compare_exchange_strong(
        current,
        encode(epoch_of(current), State::committing),
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}

void DeckOtaTransportFence::finish_commit()
{
    uint32_t current = word_.load(std::memory_order_acquire);
    while (state_of(current) == State::committing &&
           !word_.compare_exchange_weak(
               current,
               encode(epoch_of(current), State::idle),
               std::memory_order_acq_rel,
               std::memory_order_acquire
           )) {
    }
}

void DeckOtaTransportFence::begin_abort()
{
    uint32_t current = word_.load(std::memory_order_acquire);
    while (true) {
        const State current_state = state_of(current);
        const State next_state = current_state == State::committing
                                     ? State::committing
                                     : State::aborting;
        const uint32_t current_epoch = epoch_of(current);
        const uint32_t next_epoch = current_epoch == kMaximumEpoch
                                        ? 1U
                                        : current_epoch + 1U;
        if (word_.compare_exchange_weak(
                current,
                encode(next_epoch, next_state),
               std::memory_order_acq_rel,
               std::memory_order_acquire
            )) {
            return;
        }
    }
}

void DeckOtaTransportFence::finish_abort()
{
    uint32_t current = word_.load(std::memory_order_acquire);
    while (state_of(current) != State::idle &&
           !word_.compare_exchange_weak(
               current,
               encode(epoch_of(current), State::idle),
               std::memory_order_acq_rel,
               std::memory_order_acquire
           )) {
    }
}

void DeckOtaTransportFence::end_transaction()
{
    uint32_t current = word_.load(std::memory_order_acquire);
    while (state_of(current) == State::active &&
           !word_.compare_exchange_weak(
               current,
               encode(epoch_of(current), State::idle),
               std::memory_order_acq_rel,
               std::memory_order_acquire
           )) {
    }
}
