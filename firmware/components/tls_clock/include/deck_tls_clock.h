#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/*
 * Prepares the system wall clock for an exact, already-authenticated pinned
 * certificate. A credible wall clock is never changed to make an expired or
 * not-yet-valid certificate pass. On a cold boot with no credible wall clock,
 * the certificate window and firmware build time form a conservative seed.
 */
bool deck_tls_clock_prepare_pinned_certificate(
    const uint8_t *certificate_der,
    size_t certificate_der_size
);

/* Applies UTC learned only after the pinned Device Link heartbeat validates. */
bool deck_tls_clock_accept_trusted_utc(uint64_t unix_ms);

#ifdef __cplusplus
}
#endif
