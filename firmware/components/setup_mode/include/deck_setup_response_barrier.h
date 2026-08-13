#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_setup_response_barrier deck_setup_response_barrier_t;

#define DECK_SETUP_RESPONSE_ACK_SIZE 16

deck_setup_response_barrier_t *deck_setup_response_barrier_create(size_t capacity);
void deck_setup_response_barrier_destroy(deck_setup_response_barrier_t *barrier);
uint32_t deck_setup_response_barrier_issue(
    deck_setup_response_barrier_t *barrier,
    uint32_t client_ipv4,
    const uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]
);
bool deck_setup_response_barrier_acknowledge(
    deck_setup_response_barrier_t *barrier,
    uint32_t client_ipv4,
    const uint8_t response_ack[DECK_SETUP_RESPONSE_ACK_SIZE]
);
bool deck_setup_response_barrier_is_complete(
    const deck_setup_response_barrier_t *barrier,
    uint32_t generation
);
void deck_setup_response_barrier_release(
    deck_setup_response_barrier_t *barrier,
    uint32_t generation
);

#ifdef __cplusplus
}
#endif
