#include "deck_companion_identity.h"

#include <cassert>
#include <cstdint>
#include <cstring>

int main()
{
    uint8_t digest[32]{};
    for (size_t index = 0; index < sizeof(digest); ++index) {
        digest[index] = static_cast<uint8_t>(index);
    }

    char identity[44]{};
    assert(deck_companion_identity_from_digest(
        digest,
        identity,
        sizeof(identity)
    ));
    assert(std::strcmp(
               identity,
               "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
           ) == 0);

    char too_small[43]{};
    assert(!deck_companion_identity_from_digest(
        digest,
        too_small,
        sizeof(too_small)
    ));
    return 0;
}
