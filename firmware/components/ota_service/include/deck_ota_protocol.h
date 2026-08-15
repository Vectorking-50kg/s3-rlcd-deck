#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_ota_service.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DECK_OTA_PROTOCOL_OFFER = 0,
    DECK_OTA_PROTOCOL_CHUNK,
} deck_ota_protocol_kind_t;

typedef struct {
    deck_ota_protocol_kind_t kind;
    char transaction_id[DECK_OTA_TRANSACTION_ID_CAPACITY];
    deck_ota_manifest_t manifest;
    uint32_t offset;
    uint8_t data[DECK_OTA_MAX_CHUNK_BYTES];
    size_t data_size;
    bool final;
} deck_ota_protocol_command_t;

bool deck_ota_protocol_parse(
    const char *message,
    size_t message_size,
    deck_ota_protocol_command_t *command
);
bool deck_ota_protocol_format_result(
    const deck_ota_service_result_t *result,
    char *output,
    size_t capacity,
    size_t *size
);
void deck_ota_protocol_command_clear(deck_ota_protocol_command_t *command);

#ifdef __cplusplus
}
#endif
