#include "deck_device_protocol.h"

#include <cassert>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <string>

namespace {

std::string fixture(const char *name)
{
    const std::string path = std::string(DECK_REPOSITORY_ROOT) +
                             "/protocol/fixtures/device-link-v1/" + name;
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    return std::string(
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    );
}

void shared_hello_fixtures_match_the_device_contract()
{
    const std::string valid = fixture("hello-valid.json");
    assert(deck_device_protocol_validate_hello(
        valid.data(), valid.size(), "deck-001122334455"
    ));
    const std::string wrong_board = fixture("hello-wrong-board.json");
    assert(!deck_device_protocol_validate_hello(
        wrong_board.data(), wrong_board.size(), "deck-001122334455"
    ));
    const std::string duplicate = fixture("hello-duplicate-device-id.json");
    assert(!deck_device_protocol_validate_hello(
        duplicate.data(), duplicate.size(), "deck-001122334455"
    ));
}

void shared_heartbeat_fixtures_match_the_device_contract()
{
    deck_device_heartbeat_t heartbeat{};
    const std::string valid = fixture("heartbeat-valid.json");
    assert(deck_device_protocol_parse_heartbeat(
        valid.data(), valid.size(), 41, true, &heartbeat
    ));
    assert(heartbeat.monotonic_ms == 42);
    assert(heartbeat.utc_unix_ms == 1'786'624'496'123ULL);

    constexpr const char *kRejected[] = {
        "heartbeat-depth-over-capacity.json",
        "heartbeat-noncanonical-utc.json",
        "heartbeat-duplicate-field.json",
    };
    for (const char *name : kRejected) {
        const std::string message = fixture(name);
        assert(!deck_device_protocol_parse_heartbeat(
            message.data(), message.size(), 0, false, &heartbeat
        ));
    }
    const std::string regression = fixture("heartbeat-monotonic-regression.json");
    assert(!deck_device_protocol_parse_heartbeat(
        regression.data(), regression.size(), 43, true, &heartbeat
    ));
}

void exact_certificate_pin_rejects_a_wrong_digest()
{
    uint8_t digest[32]{};
    assert(deck_device_protocol_fingerprint_matches_sha256(
        digest,
        "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    ));
    digest[31] = 1;
    assert(!deck_device_protocol_fingerprint_matches_sha256(
        digest,
        "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    ));
    assert(deck_device_protocol_fingerprint_matches_sha256(
        digest,
        "sha256:0000000000000000000000000000000000000000000000000000000000000001"
    ));
    assert(!deck_device_protocol_fingerprint_matches_sha256(
        digest,
        "SHA256:0000000000000000000000000000000000000000000000000000000000000001"
    ));
}

}  // namespace

int main()
{
    shared_hello_fixtures_match_the_device_contract();
    shared_heartbeat_fixtures_match_the_device_contract();
    exact_certificate_pin_rejects_a_wrong_digest();
    return 0;
}
