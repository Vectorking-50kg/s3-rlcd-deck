#pragma once

#include <atomic>
#include <cstdint>

// Linearizes authenticated transport abort against OTA boot-slot commit while
// also rejecting commands queued by a previous Device Link transport.
class DeckOtaTransportFence final {
public:
    uint32_t capture_epoch() const;
    bool accepts(uint32_t command_epoch) const;

    // Starts a transaction only when the command belongs to the current
    // transport and abort has not already won the linearization race.
    bool begin_transaction(uint32_t command_epoch);

    // Returns true only when commit wins before abort. finish_commit must be
    // called after every successful try_begin_commit, regardless of I/O result.
    bool try_begin_commit();
    void finish_commit();

    // Prevents any not-yet-committing transaction from selecting a boot slot
    // and advances the command epoch. finish_abort reopens the fence only after
    // the worker has destroyed the transaction and drained stale results.
    void begin_abort();
    void finish_abort();

    // Releases a non-committing transaction after validation/write failure or
    // when replacing a terminal transaction with a new offer.
    void end_transaction();

private:
    enum class State : uint8_t { idle, active, committing, aborting };

    static constexpr uint32_t kStateBits = 2;
    static constexpr uint32_t kStateMask = (1U << kStateBits) - 1U;
    static constexpr uint32_t kMaximumEpoch = UINT32_MAX >> kStateBits;

    static constexpr uint32_t encode(uint32_t epoch, State state)
    {
        return (epoch << kStateBits) | static_cast<uint32_t>(state);
    }
    static constexpr uint32_t epoch_of(uint32_t word)
    {
        return word >> kStateBits;
    }
    static constexpr State state_of(uint32_t word)
    {
        return static_cast<State>(word & kStateMask);
    }

    // Epoch and state share one CAS word so begin_transaction cannot validate
    // an old epoch and later acquire a freshly-idle state from another epoch.
    std::atomic<uint32_t> word_{encode(1, State::idle)};
};
