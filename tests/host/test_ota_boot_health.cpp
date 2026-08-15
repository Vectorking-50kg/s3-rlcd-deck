#include "deck_ota_boot_health.h"

#include <cassert>

int main()
{
    deck_ota_boot_health_t health{};
    assert(deck_ota_boot_health_decide(&health) == DECK_OTA_BOOT_NOT_PENDING);
    health.pending_verify = true;
    health.deadline_ms = 60'000;
    assert(deck_ota_boot_health_decide(&health) == DECK_OTA_BOOT_WAIT);
    health.display_ready = true;
    health.peripherals_ready = true;
    health.wifi_subsystem_ready = true;
    health.companion_subsystem_ready = true;
    assert(deck_ota_boot_health_decide(&health) == DECK_OTA_BOOT_MARK_VALID);
    health.fatal_failure = true;
    assert(deck_ota_boot_health_decide(&health) == DECK_OTA_BOOT_ROLLBACK);
    health.fatal_failure = false;
    health.companion_subsystem_ready = false;
    health.elapsed_ms = 60'000;
    assert(deck_ota_boot_health_decide(&health) == DECK_OTA_BOOT_ROLLBACK);
    return 0;
}
