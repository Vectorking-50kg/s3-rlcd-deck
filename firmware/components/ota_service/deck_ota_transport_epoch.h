#pragma once

#include <atomic>
#include <cstdint>

// Separates commands queued by consecutive authenticated Device Link
// transports. The Link task advances the epoch before waiting for an abort;
// the OTA worker accepts only commands captured from the current epoch.
class DeckOtaTransportEpoch final {
public:
    uint32_t capture() const;
    void advance();
    bool accepts(uint32_t command_epoch) const;

private:
    std::atomic<uint32_t> epoch_{1};
};
