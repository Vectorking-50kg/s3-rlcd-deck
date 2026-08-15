#include "deck_serial_frame.h"

#include <algorithm>
#include <cassert>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <regex>
#include <string>
#include <vector>

namespace {

std::vector<uint8_t> fixture(const char *name)
{
    const std::string path = std::string(DECK_REPOSITORY_ROOT) +
                             "/protocol/fixtures/serial-frame-v1/" + name;
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    const std::string hex{
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    };
    std::vector<uint8_t> bytes;
    for (size_t index = 0; index + 1 < hex.size(); index += 2) {
        if (hex[index] == '\n' || hex[index] == '\r') {
            break;
        }
        const auto digit = [](char value) -> uint8_t {
            return static_cast<uint8_t>(
                value <= '9' ? value - '0' : value - 'a' + 10
            );
        };
        bytes.push_back(static_cast<uint8_t>(
            (digit(hex[index]) << 4U) | digit(hex[index + 1])
        ));
    }
    return bytes;
}

std::string catalog()
{
    const std::string path = std::string(DECK_REPOSITORY_ROOT) +
                             "/protocol/catalog/serial-frame-v1.json";
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    return std::string(
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    );
}

unsigned catalog_unsigned(const std::string &document, const char *field)
{
    const std::regex expression(
        std::string("\\\"") + field + "\\\"\\s*:\\s*([0-9]+)"
    );
    std::smatch match;
    assert(std::regex_search(document, match, expression));
    return static_cast<unsigned>(std::stoul(match[1].str()));
}

void constants_match_the_shared_catalog()
{
    const std::string document = catalog();
    assert(document.find("\"magic_hex\": \"53524431\"") != std::string::npos);
    assert(document.find("\"byte_order\": \"big\"") != std::string::npos);
    assert(catalog_unsigned(document, "header_bytes") == DECK_SERIAL_FRAME_HEADER_BYTES);
    assert(catalog_unsigned(document, "max_payload_bytes") ==
           DECK_SERIAL_FRAME_MAX_PAYLOAD_BYTES);
    assert(catalog_unsigned(document, "target_rx") == DECK_SERIAL_FRAME_TARGET_RX);
    assert(catalog_unsigned(document, "web_tx") == DECK_SERIAL_FRAME_WEB_TX);
}

void shared_fixtures_match_the_binary_contract()
{
    const std::vector<uint8_t> target = fixture("valid-target-rx.hex");
    deck_serial_frame_view_t frame{};
    assert(deck_serial_frame_decode(target.data(), target.size(), &frame));
    assert(frame.channel == DECK_SERIAL_FRAME_TARGET_RX);
    assert(frame.session_id == 42);
    assert(frame.sequence == 7);
    assert(frame.monotonic_ms == 1234);
    assert(frame.payload_size == 3);
    assert(frame.payload[0] == 0 && frame.payload[1] == 0xff && frame.payload[2] == 'A');

    const std::vector<uint8_t> web = fixture("valid-web-tx.hex");
    assert(deck_serial_frame_decode(web.data(), web.size(), &frame));
    assert(frame.channel == DECK_SERIAL_FRAME_WEB_TX);

    for (const char *name : {"invalid-flags.hex", "invalid-length.hex"}) {
        const std::vector<uint8_t> rejected = fixture(name);
        assert(!deck_serial_frame_decode(rejected.data(), rejected.size(), &frame));
    }
}

void encoder_is_byte_exact_and_bounded()
{
    const uint8_t payload[] = {0x00, 0xff, 'A'};
    uint8_t document[DECK_SERIAL_FRAME_MAX_BYTES]{};
    const size_t size = deck_serial_frame_encode(
        DECK_SERIAL_FRAME_TARGET_RX,
        42,
        7,
        1234,
        payload,
        sizeof(payload),
        document,
        sizeof(document)
    );
    const std::vector<uint8_t> expected = fixture("valid-target-rx.hex");
    assert(size == expected.size());
    assert(std::equal(expected.begin(), expected.end(), document));
    assert(deck_serial_frame_encode(
        DECK_SERIAL_FRAME_TARGET_RX,
        0,
        7,
        0,
        payload,
        sizeof(payload),
        document,
        sizeof(document)
    ) == 0);
}

}  // namespace

int main()
{
    constants_match_the_shared_catalog();
    shared_fixtures_match_the_binary_contract();
    encoder_is_byte_exact_and_bounded();
    return 0;
}
