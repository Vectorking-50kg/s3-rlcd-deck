#include "deck_ota_transport_fence.h"

#include <atomic>
#include <cassert>
#include <thread>

namespace {

void abort_rejects_queued_commands_and_not_yet_committing_transaction()
{
    DeckOtaTransportFence fence;
    const uint32_t queued_offer = fence.capture_epoch();
    const uint32_t queued_chunk = fence.capture_epoch();
    assert(fence.begin_transaction(queued_offer));

    fence.begin_abort();
    assert(!fence.accepts(queued_offer));
    assert(!fence.accepts(queued_chunk));
    assert(!fence.try_begin_commit());

    fence.finish_abort();
    const uint32_t replacement_offer = fence.capture_epoch();
    assert(fence.accepts(replacement_offer));
    assert(fence.begin_transaction(replacement_offer));
    fence.end_transaction();
}

void commit_that_linearizes_first_may_finish_before_abort_cleanup()
{
    DeckOtaTransportFence fence;
    const uint32_t offer = fence.capture_epoch();
    assert(fence.begin_transaction(offer));
    assert(fence.try_begin_commit());

    fence.begin_abort();
    assert(!fence.accepts(offer));

    // Selection owns the irreversible boundary because it won first.
    fence.finish_commit();
    fence.finish_abort();
    const uint32_t replacement_offer = fence.capture_epoch();
    assert(fence.begin_transaction(replacement_offer));
    fence.end_transaction();
}

void begin_transaction_and_abort_are_one_atomic_epoch_state_race()
{
    for (unsigned iteration = 0; iteration < 10'000; ++iteration) {
        DeckOtaTransportFence fence;
        const uint32_t old_epoch = fence.capture_epoch();
        std::atomic<bool> start{false};
        std::thread begin([&]() {
            while (!start.load(std::memory_order_acquire)) {
            }
            (void)fence.begin_transaction(old_epoch);
        });
        std::thread abort([&]() {
            while (!start.load(std::memory_order_acquire)) {
            }
            fence.begin_abort();
        });
        start.store(true, std::memory_order_release);
        begin.join();
        abort.join();

        assert(!fence.accepts(old_epoch));
        assert(!fence.try_begin_commit());
        fence.finish_abort();
        assert(fence.begin_transaction(fence.capture_epoch()));
        fence.end_transaction();
    }
}

}  // namespace

int main()
{
    abort_rejects_queued_commands_and_not_yet_committing_transaction();
    commit_that_linearizes_first_may_finish_before_abort_cleanup();
    begin_transaction_and_abort_are_one_atomic_epoch_state_race();
    return 0;
}
