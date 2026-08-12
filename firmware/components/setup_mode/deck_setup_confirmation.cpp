#include "deck_setup_confirmation.h"

#include <array>
#include <cstring>
#include <new>

struct deck_setup_confirmation {
    deck_setup_confirmation_options_t options;
    std::array<char, DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY> token;
    uint32_t session_id;
    uint64_t issued_at_ms;
    bool issued;
};

namespace {

void clear_issue(deck_setup_confirmation_t *confirmation)
{
    volatile char *bytes = confirmation->token.data();
    for (size_t index = 0; index < confirmation->token.size(); ++index) {
        bytes[index] = '\0';
    }
    confirmation->session_id = 0;
    confirmation->issued_at_ms = 0;
    confirmation->issued = false;
}

bool valid_token(const char *token)
{
    if (token == nullptr ||
        strnlen(token, DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY) !=
            DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY - 1) {
        return false;
    }
    for (size_t index = 0; index < DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY - 1; ++index) {
        if (!((token[index] >= '0' && token[index] <= '9') ||
              (token[index] >= 'a' && token[index] <= 'f'))) {
            return false;
        }
    }
    return true;
}

bool tokens_equal(const char *left, const char *right)
{
    unsigned difference = 0;
    for (size_t index = 0; index < DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY - 1; ++index) {
        difference |= static_cast<unsigned char>(left[index]) ^
                      static_cast<unsigned char>(right[index]);
    }
    return difference == 0;
}

}  // namespace

deck_setup_confirmation_t *deck_setup_confirmation_create(
    const deck_setup_confirmation_options_t *options
)
{
    if (options == nullptr || options->lifetime_ms == 0 || options->random == nullptr) {
        return nullptr;
    }
    auto *confirmation = new (std::nothrow) deck_setup_confirmation_t{};
    if (confirmation != nullptr) {
        confirmation->options = *options;
    }
    return confirmation;
}

void deck_setup_confirmation_destroy(deck_setup_confirmation_t *confirmation)
{
    if (confirmation != nullptr) {
        clear_issue(confirmation);
        delete confirmation;
    }
}

bool deck_setup_confirmation_issue(
    deck_setup_confirmation_t *confirmation,
    uint32_t session_id,
    uint64_t now_ms,
    char *token,
    size_t token_capacity
)
{
    if (confirmation == nullptr || session_id == 0 || token == nullptr ||
        token_capacity < DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY) {
        return false;
    }
    std::array<uint8_t, 8> random_bytes{};
    if (!confirmation->options.random(
            confirmation->options.random_context,
            random_bytes.data(),
            random_bytes.size()
        )) {
        clear_issue(confirmation);
        token[0] = '\0';
        return false;
    }
    static constexpr char kHex[] = "0123456789abcdef";
    for (size_t index = 0; index < random_bytes.size(); ++index) {
        confirmation->token[index * 2] = kHex[random_bytes[index] >> 4U];
        confirmation->token[index * 2 + 1] = kHex[random_bytes[index] & 0x0fU];
    }
    confirmation->token.back() = '\0';
    confirmation->session_id = session_id;
    confirmation->issued_at_ms = now_ms;
    confirmation->issued = true;
    std::memcpy(token, confirmation->token.data(), confirmation->token.size());
    return true;
}

deck_setup_confirmation_result_t deck_setup_confirmation_consume(
    deck_setup_confirmation_t *confirmation,
    uint32_t session_id,
    const char *token,
    uint64_t now_ms
)
{
    if (confirmation == nullptr || !confirmation->issued) {
        return DECK_SETUP_CONFIRMATION_NOT_ISSUED;
    }
    if (now_ms < confirmation->issued_at_ms ||
        now_ms - confirmation->issued_at_ms > confirmation->options.lifetime_ms) {
        clear_issue(confirmation);
        return DECK_SETUP_CONFIRMATION_EXPIRED;
    }
    if (session_id != confirmation->session_id || !valid_token(token) ||
        !tokens_equal(token, confirmation->token.data())) {
        return DECK_SETUP_CONFIRMATION_MISMATCH;
    }
    clear_issue(confirmation);
    return DECK_SETUP_CONFIRMATION_CONFIRMED;
}
