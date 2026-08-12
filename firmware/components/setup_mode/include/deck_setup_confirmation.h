#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY 17

typedef struct deck_setup_confirmation deck_setup_confirmation_t;
typedef bool (*deck_setup_confirmation_random_fn)(
    void *context,
    uint8_t *output,
    size_t size
);

typedef struct {
    uint64_t lifetime_ms;
    deck_setup_confirmation_random_fn random;
    void *random_context;
} deck_setup_confirmation_options_t;

typedef enum {
    DECK_SETUP_CONFIRMATION_CONFIRMED = 0,
    DECK_SETUP_CONFIRMATION_NOT_ISSUED,
    DECK_SETUP_CONFIRMATION_MISMATCH,
    DECK_SETUP_CONFIRMATION_EXPIRED,
} deck_setup_confirmation_result_t;

deck_setup_confirmation_t *deck_setup_confirmation_create(
    const deck_setup_confirmation_options_t *options
);
void deck_setup_confirmation_destroy(deck_setup_confirmation_t *confirmation);
bool deck_setup_confirmation_issue(
    deck_setup_confirmation_t *confirmation,
    uint32_t session_id,
    uint64_t now_ms,
    char *token,
    size_t token_capacity
);
deck_setup_confirmation_result_t deck_setup_confirmation_consume(
    deck_setup_confirmation_t *confirmation,
    uint32_t session_id,
    const char *token,
    uint64_t now_ms
);

#ifdef __cplusplus
}
#endif
