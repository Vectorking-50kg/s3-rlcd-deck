#include "deck_companion_identity.h"

namespace {

constexpr char kBase64Url[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
constexpr size_t kDigestSize = 32;
constexpr size_t kIdentitySize = 43;

}  // namespace

bool deck_companion_identity_from_digest(
    const uint8_t *digest,
    char *identity,
    size_t identity_capacity
)
{
    if (digest == nullptr || identity == nullptr ||
        identity_capacity < kIdentitySize + 1) {
        if (identity != nullptr && identity_capacity != 0) {
            identity[0] = '\0';
        }
        return false;
    }

    size_t input_index = 0;
    size_t output_index = 0;
    while (input_index + 3 <= kDigestSize) {
        const uint32_t value =
            (static_cast<uint32_t>(digest[input_index]) << 16U) |
            (static_cast<uint32_t>(digest[input_index + 1]) << 8U) |
            static_cast<uint32_t>(digest[input_index + 2]);
        identity[output_index++] = kBase64Url[(value >> 18U) & 0x3fU];
        identity[output_index++] = kBase64Url[(value >> 12U) & 0x3fU];
        identity[output_index++] = kBase64Url[(value >> 6U) & 0x3fU];
        identity[output_index++] = kBase64Url[value & 0x3fU];
        input_index += 3;
    }

    const uint32_t tail =
        (static_cast<uint32_t>(digest[input_index]) << 16U) |
        (static_cast<uint32_t>(digest[input_index + 1]) << 8U);
    identity[output_index++] = kBase64Url[(tail >> 18U) & 0x3fU];
    identity[output_index++] = kBase64Url[(tail >> 12U) & 0x3fU];
    identity[output_index++] = kBase64Url[(tail >> 6U) & 0x3fU];
    identity[output_index] = '\0';
    return output_index == kIdentitySize;
}
