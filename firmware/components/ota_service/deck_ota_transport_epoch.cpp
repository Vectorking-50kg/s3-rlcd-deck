#include "deck_ota_transport_epoch.h"

uint32_t DeckOtaTransportEpoch::capture() const
{
    return epoch_.load(std::memory_order_acquire);
}

void DeckOtaTransportEpoch::advance()
{
    (void)epoch_.fetch_add(1, std::memory_order_acq_rel);
}

bool DeckOtaTransportEpoch::accepts(uint32_t command_epoch) const
{
    return command_epoch == capture();
}
