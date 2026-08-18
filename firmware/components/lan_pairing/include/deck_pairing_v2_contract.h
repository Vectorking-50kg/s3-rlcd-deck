#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_PAIRING_V2_PROTOCOL_VERSION 2
#define DECK_PAIRING_V2_MAX_DOCUMENT_BYTES 4096
#define DECK_PAIRING_V2_ID_CAPACITY 33
#define DECK_PAIRING_V2_DIGEST_CAPACITY 72
#define DECK_PAIRING_V2_HUB_SERVICE_CAPACITY 96
#define DECK_PAIRING_V2_HUB_ADDRESS_CAPACITY 22
#define DECK_PAIRING_V2_TOKEN_CAPACITY 44
#define DECK_PAIRING_V2_CERTIFICATE_DER_CAPACITY 1024
#define DECK_PAIRING_V2_DEVICE_ID_CAPACITY 65
#define DECK_PAIRING_V2_DEVICE_IDENTITY_CAPACITY 684

typedef enum {
    DECK_PAIRING_V2_MESSAGE_INVALID = 0,
    DECK_PAIRING_V2_MESSAGE_CREDENTIALS,
    DECK_PAIRING_V2_MESSAGE_COMMIT_READY,
    DECK_PAIRING_V2_MESSAGE_COMMIT,
    DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT,
    DECK_PAIRING_V2_MESSAGE_STATUS_REQUEST,
    DECK_PAIRING_V2_MESSAGE_STATUS,
    DECK_PAIRING_V2_MESSAGE_CANCEL,
    DECK_PAIRING_V2_MESSAGE_ERROR,
} deck_pairing_v2_message_type_t;

typedef bool (*deck_pairing_v2_sha256_fn)(
    void *context,
    const uint8_t *input,
    size_t input_size,
    uint8_t output[32]
);

typedef struct {
    deck_pairing_v2_sha256_fn sha256;
    void *context;
} deck_pairing_v2_crypto_t;

typedef struct {
    char session_id[DECK_PAIRING_V2_ID_CAPACITY];
    char transaction_id[DECK_PAIRING_V2_ID_CAPACITY];
    uint32_t sequence;
} deck_pairing_v2_common_t;

typedef struct {
    char window_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char companion_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char hub_service[DECK_PAIRING_V2_HUB_SERVICE_CAPACITY];
    char hub_address[DECK_PAIRING_V2_HUB_ADDRESS_CAPACITY];
    char token[DECK_PAIRING_V2_TOKEN_CAPACITY];
    char certificate_fingerprint[DECK_PAIRING_V2_DIGEST_CAPACITY];
    uint8_t certificate_der[DECK_PAIRING_V2_CERTIFICATE_DER_CAPACITY];
    size_t certificate_der_size;
    uint32_t device_link_protocol;
} deck_pairing_v2_credentials_t;

typedef struct {
    char deck_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char transcript_sha256[DECK_PAIRING_V2_DIGEST_CAPACITY];
} deck_pairing_v2_commit_t;

typedef struct {
    char window_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char companion_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char deck_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    char device_id[DECK_PAIRING_V2_DEVICE_ID_CAPACITY];
    char device_identity[DECK_PAIRING_V2_DEVICE_IDENTITY_CAPACITY];
    char profile_id[DECK_PAIRING_V2_DIGEST_CAPACITY];
    char transcript_sha256[DECK_PAIRING_V2_DIGEST_CAPACITY];
} deck_pairing_v2_commit_ready_t;

typedef struct {
    deck_pairing_v2_message_type_t type;
    deck_pairing_v2_common_t common;
    deck_pairing_v2_credentials_t credentials;
    deck_pairing_v2_commit_ready_t commit_ready;
    deck_pairing_v2_commit_t commit;
    char state[16];
    char error_code[32];
    char profile_id[DECK_PAIRING_V2_DIGEST_CAPACITY];
    char transcript_sha256[DECK_PAIRING_V2_DIGEST_CAPACITY];
    uint32_t profile_generation;
} deck_pairing_v2_message_t;

bool deck_pairing_v2_contract_decode(
    const char *document,
    size_t document_size,
    const deck_pairing_v2_crypto_t *crypto,
    deck_pairing_v2_message_t *message
);

void deck_pairing_v2_contract_clear(deck_pairing_v2_message_t *message);

/* Encodes Deck-owned commit-ready or commit-receipt messages canonically. */
bool deck_pairing_v2_contract_encode(
    const deck_pairing_v2_message_t *message,
    char *document,
    size_t document_capacity,
    size_t *document_size
);

/* Computes the canonical transcript bound by commit-ready and commit. */
bool deck_pairing_v2_contract_transcript_sha256(
    const deck_pairing_v2_message_t *credentials,
    const deck_pairing_v2_message_t *commit_ready,
    const deck_pairing_v2_crypto_t *crypto,
    char output[DECK_PAIRING_V2_DIGEST_CAPACITY]
);

#ifdef __cplusplus
}
#endif
