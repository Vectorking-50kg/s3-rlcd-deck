#pragma once

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    bool pending_verify;
    bool display_ready;
    bool peripherals_ready;
    bool wifi_subsystem_ready;
    bool companion_subsystem_ready;
    bool fatal_failure;
    uint64_t elapsed_ms;
    uint64_t deadline_ms;
} deck_ota_boot_health_t;

typedef enum {
    DECK_OTA_BOOT_NOT_PENDING = 0,
    DECK_OTA_BOOT_WAIT,
    DECK_OTA_BOOT_MARK_VALID,
    DECK_OTA_BOOT_ROLLBACK,
} deck_ota_boot_decision_t;

deck_ota_boot_decision_t deck_ota_boot_health_decide(
    const deck_ota_boot_health_t *health
);

#ifdef __cplusplus
}
#endif
