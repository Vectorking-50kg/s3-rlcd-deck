#include "deck_serial_frame.h"
#include "deck_serial_request_tracker.h"

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

void web_frame_order_is_strict_but_resets_for_a_new_connection()
{
    const uint8_t payload = 'x';
    deck_serial_frame_view_t frame{
        DECK_SERIAL_FRAME_WEB_TX,
        42,
        900,
        5000,
        &payload,
        1,
    };
    deck_serial_frame_order_t order{};
    assert(deck_serial_frame_order_accepts(&order, &frame));
    deck_serial_frame_order_commit(&order, &frame);
    assert(!deck_serial_frame_order_accepts(&order, &frame));
    frame.sequence = 901;
    frame.monotonic_ms = 4999;
    assert(!deck_serial_frame_order_accepts(&order, &frame));

    // A newly connected Companion starts its process-local sequence and
    // monotonic epoch from one. The Link disconnect fence authorizes that new
    // generation without weakening ordering inside either connection.
    deck_serial_frame_order_reset(&order);
    frame.sequence = 1;
    frame.monotonic_ms = 0;
    assert(deck_serial_frame_order_accepts(&order, &frame));
    deck_serial_frame_order_commit(&order, &frame);
    assert(!deck_serial_frame_order_accepts(&order, &frame));
}

void companion_request_ids_are_mapped_to_a_link_lifetime_sequence()
{
    deck_serial_request_tracker_t tracker{};
    uint64_t service_request_id = 0;
    assert(deck_serial_request_begin(
               &tracker,
               900,
               &service_request_id
           ) == DECK_SERIAL_REQUEST_NEW);
    assert(service_request_id == 1);
    uint64_t replay_service_request_id = 0;
    assert(deck_serial_request_begin(
               &tracker,
               900,
               &replay_service_request_id
           ) == DECK_SERIAL_REQUEST_REPLAY);
    assert(replay_service_request_id == service_request_id);
    assert(deck_serial_request_begin(
               &tracker,
               901,
               &replay_service_request_id
           ) == DECK_SERIAL_REQUEST_BUSY);
    uint64_t external_request_id = 0;
    assert(deck_serial_request_complete(
        &tracker,
        service_request_id,
        &external_request_id
    ));
    assert(external_request_id == 900);

    // A restarted Companion may start again at external request 1. The Serial
    // Service still receives a strictly newer Link-lifetime ID.
    assert(deck_serial_request_begin(
               &tracker,
               1,
               &service_request_id
           ) == DECK_SERIAL_REQUEST_NEW);
    assert(service_request_id == 2);
    deck_serial_request_transport_reset(&tracker);
    assert(deck_serial_request_begin(
               &tracker,
               1,
               &service_request_id
           ) == DECK_SERIAL_REQUEST_NEW);
    assert(service_request_id == 3);
}

}  // namespace

int main()
{
    constants_match_the_shared_catalog();
    shared_fixtures_match_the_binary_contract();
    encoder_is_byte_exact_and_bounded();
    web_frame_order_is_strict_but_resets_for_a_new_connection();
    companion_request_ids_are_mapped_to_a_link_lifetime_sequence();
    return 0;
}
