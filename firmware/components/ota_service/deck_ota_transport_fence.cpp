#include "deck_ota_transport_fence.h"

uint32_t DeckOtaTransportFence::capture_epoch() const
{
    return epoch_.load(std::memory_order_acquire);
}

bool DeckOtaTransportFence::accepts(uint32_t command_epoch) const
{
    return command_epoch == capture_epoch();
}

bool DeckOtaTransportFence::begin_transaction(uint32_t command_epoch)
{
    if (!accepts(command_epoch)) {
        return false;
    }
    State expected = State::idle;
    return state_.compare_exchange_strong(
        expected,
        State::active,
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}

bool DeckOtaTransportFence::try_begin_commit()
{
    State expected = State::active;
    return state_.compare_exchange_strong(
        expected,
        State::committing,
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}

void DeckOtaTransportFence::finish_commit()
{
    State expected = State::committing;
    (void)state_.compare_exchange_strong(
        expected,
        State::idle,
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}

void DeckOtaTransportFence::begin_abort()
{
    State state = state_.load(std::memory_order_acquire);
    while (state != State::committing && state != State::aborting &&
           !state_.compare_exchange_weak(
               state,
               State::aborting,
               std::memory_order_acq_rel,
               std::memory_order_acquire
           )) {
    }
    (void)epoch_.fetch_add(1, std::memory_order_acq_rel);
}

void DeckOtaTransportFence::finish_abort()
{
    state_.store(State::idle, std::memory_order_release);
}

void DeckOtaTransportFence::end_transaction()
{
    State expected = State::active;
    (void)state_.compare_exchange_strong(
        expected,
        State::idle,
        std::memory_order_acq_rel,
        std::memory_order_acquire
    );
}
