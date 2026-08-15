#include "deck_ota_transport_epoch.h"

#include <cassert>

int main()
{
    DeckOtaTransportEpoch epoch;

    const uint32_t queued_offer = epoch.capture();
    const uint32_t queued_chunk = epoch.capture();
    assert(epoch.accepts(queued_offer));
    assert(epoch.accepts(queued_chunk));

    // Models abort being inserted ahead of commands queued by the old Link.
    epoch.advance();
    assert(!epoch.accepts(queued_offer));
    assert(!epoch.accepts(queued_chunk));

    const uint32_t replacement_offer = epoch.capture();
    assert(epoch.accepts(replacement_offer));

    epoch.advance();
    assert(!epoch.accepts(replacement_offer));
    return 0;
}
