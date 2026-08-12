#include "deck_setup_confirmation.h"

#include <cassert>
#include <cstddef>
#include <cstdint>
#include <string>

namespace {

struct DeterministicRandom {
    uint8_t next;
    bool fail;
};

bool fill_random(void *context, uint8_t *output, size_t size)
{
    auto *random = static_cast<DeterministicRandom *>(context);
    if (random->fail) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        output[index] = random->next++;
    }
    return true;
}

deck_setup_confirmation_t *create_confirmation(DeterministicRandom *random)
{
    const deck_setup_confirmation_options_t options = {
        60'000,
        fill_random,
        random,
    };
    return deck_setup_confirmation_create(&options);
}

void token_is_session_bound_limited_and_single_use()
{
    DeterministicRandom random{0, false};
    deck_setup_confirmation_t *confirmation = create_confirmation(&random);
    assert(confirmation != nullptr);
    char token[DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY];
    assert(deck_setup_confirmation_issue(confirmation, 7, 100, token, sizeof(token)));
    assert(std::string(token) == "0001020304050607");
    assert(deck_setup_confirmation_consume(confirmation, 8, token, 101) ==
           DECK_SETUP_CONFIRMATION_MISMATCH);
    assert(deck_setup_confirmation_consume(confirmation, 7, "wrong", 101) ==
           DECK_SETUP_CONFIRMATION_MISMATCH);
    assert(deck_setup_confirmation_consume(confirmation, 7, token, 60'100) ==
           DECK_SETUP_CONFIRMATION_CONFIRMED);
    assert(deck_setup_confirmation_consume(confirmation, 7, token, 60'100) ==
           DECK_SETUP_CONFIRMATION_NOT_ISSUED);
    deck_setup_confirmation_destroy(confirmation);
}

void expired_or_unavailable_tokens_never_confirm()
{
    DeterministicRandom random{8, false};
    deck_setup_confirmation_t *confirmation = create_confirmation(&random);
    assert(confirmation != nullptr);
    char token[DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY];
    assert(deck_setup_confirmation_issue(confirmation, 2, 1'000, token, sizeof(token)));
    assert(deck_setup_confirmation_consume(confirmation, 2, token, 61'001) ==
           DECK_SETUP_CONFIRMATION_EXPIRED);

    random.fail = true;
    assert(!deck_setup_confirmation_issue(confirmation, 3, 70'000, token, sizeof(token)));
    assert(deck_setup_confirmation_consume(confirmation, 3, token, 70'000) ==
           DECK_SETUP_CONFIRMATION_NOT_ISSUED);
    deck_setup_confirmation_destroy(confirmation);
}

}  // namespace

int main()
{
    token_is_session_bound_limited_and_single_use();
    expired_or_unavailable_tokens_never_confirm();
    return 0;
}
