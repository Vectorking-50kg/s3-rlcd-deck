#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Encodes a SHA-256 digest as 43-character unpadded Base64url plus NUL. */
bool deck_companion_identity_from_digest(
    const uint8_t *digest,
    char *identity,
    size_t identity_capacity
);

#ifdef __cplusplus
}
#endif
