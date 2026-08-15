#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ota_transaction.h"

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_OTA_TRANSACTION_ID_CAPACITY 33

typedef struct deck_ota_service deck_ota_service_t;

typedef struct {
    char transaction_id[DECK_OTA_TRANSACTION_ID_CAPACITY];
    deck_ota_transaction_snapshot_t transaction;
    bool reboot_required;
} deck_ota_service_result_t;

deck_ota_service_t *deck_ota_service_start(const char *running_version);
bool deck_ota_service_stop(deck_ota_service_t *service);
bool deck_ota_service_offer(
    deck_ota_service_t *service,
    const char *transaction_id,
    const deck_ota_manifest_t *manifest
);
bool deck_ota_service_write(
    deck_ota_service_t *service,
    const char *transaction_id,
    uint32_t offset,
    const uint8_t *data,
    size_t size,
    bool final
);
bool deck_ota_service_poll_result(
    deck_ota_service_t *service,
    deck_ota_service_result_t *result
);

/*
 * Starts a first-boot rollback guard only for ESP_OTA_IMG_PENDING_VERIFY.
 * Confirm consumes the guard after all four production subsystems are ready.
 * If confirmation never arrives, the guard rolls back at its deadline.
 */
typedef struct deck_ota_boot_guard deck_ota_boot_guard_t;
deck_ota_boot_guard_t *deck_ota_boot_guard_start(uint64_t deadline_ms);
bool deck_ota_boot_guard_confirm(
    deck_ota_boot_guard_t *guard,
    bool display_ready,
    bool peripherals_ready,
    bool wifi_subsystem_ready,
    bool companion_subsystem_ready
);

#ifdef __cplusplus
}
#endif
