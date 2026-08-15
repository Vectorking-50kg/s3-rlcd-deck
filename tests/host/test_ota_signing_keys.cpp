#include "deck_ota_signing_keys.h"

#include <cassert>
#include <fstream>
#include <iomanip>
#include <iterator>
#include <sstream>
#include <string>

int main()
{
    const uint8_t *key = nullptr;
    size_t size = 0;
    assert(!deck_ota_signing_public_key(0, &key, &size));
    assert(deck_ota_signing_public_key(1, &key, &size));
    assert(key != nullptr && size == 65U && key[0] == 0x04U);
    std::ostringstream hex;
    hex << std::hex << std::setfill('0');
    for (size_t index = 0; index < size; ++index) {
        hex << std::setw(2) << static_cast<unsigned>(key[index]);
    }
    std::ifstream input(
        std::string(DECK_REPOSITORY_ROOT) +
        "/protocol/catalog/ota-signing-keys-v1.json",
        std::ios::binary
    );
    assert(input.good());
    const std::string catalog{
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    };
    assert(catalog.find("\"id\": 1") != std::string::npos);
    assert(catalog.find(hex.str()) != std::string::npos);
    return 0;
}
