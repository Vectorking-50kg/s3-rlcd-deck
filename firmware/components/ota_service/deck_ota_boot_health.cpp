#include "deck_ota_boot_health.h"

deck_ota_boot_decision_t deck_ota_boot_health_decide(
    const deck_ota_boot_health_t *health
)
{
    if (health == nullptr || !health->pending_verify) {
        return DECK_OTA_BOOT_NOT_PENDING;
    }
    if (health->fatal_failure || health->deadline_ms == 0 ||
        health->elapsed_ms >= health->deadline_ms) {
        return DECK_OTA_BOOT_ROLLBACK;
    }
    if (health->display_ready && health->peripherals_ready &&
        health->wifi_subsystem_ready && health->companion_subsystem_ready) {
        return DECK_OTA_BOOT_MARK_VALID;
    }
    return DECK_OTA_BOOT_WAIT;
}
