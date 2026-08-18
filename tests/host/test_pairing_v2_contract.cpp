#include "deck_pairing_v2_contract.h"

#include <array>
#include <cassert>
#include <cstring>
#include <fstream>
#include <iterator>
#include <string>

namespace {

std::string fixture(const char *name)
{
    const std::string path = std::string(DECK_REPOSITORY_ROOT) +
                             "/protocol/fixtures/pairing-v2/" + name;
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    return std::string(
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    );
}

bool fixture_sha256(void *, const uint8_t *input, size_t input_size, uint8_t output[32])
{
    constexpr char certificate[] = "pairing-v2-test-certificate";
    constexpr std::array<uint8_t, 32> digest = {
        0xc8, 0x12, 0x76, 0x43, 0x94, 0x2e, 0xe8, 0xff,
        0x1f, 0xfe, 0xbd, 0x2a, 0x76, 0x43, 0x0e, 0x8c,
        0x88, 0xc1, 0x4a, 0xc3, 0x02, 0x6a, 0xe2, 0x85,
        0x55, 0x65, 0xf1, 0x91, 0xd8, 0x72, 0x6b, 0xc5,
    };
    if (input == nullptr || output == nullptr || input_size != sizeof(certificate) - 1U ||
        std::memcmp(input, certificate, sizeof(certificate) - 1U) != 0) {
        return false;
    }
    std::memcpy(output, digest.data(), digest.size());
    return true;
}

void shared_fixtures_match_the_firmware_contract()
{
    const deck_pairing_v2_crypto_t crypto{fixture_sha256, nullptr};
    struct AcceptedFixture {
        const char *name;
        deck_pairing_v2_message_type_t type;
    };
    constexpr AcceptedFixture accepted[] = {
        {"valid-credentials.json", DECK_PAIRING_V2_MESSAGE_CREDENTIALS},
        {"valid-commit-ready.json", DECK_PAIRING_V2_MESSAGE_COMMIT_READY},
        {"valid-commit.json", DECK_PAIRING_V2_MESSAGE_COMMIT},
        {"valid-commit-receipt.json", DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT},
        {"valid-status-request.json", DECK_PAIRING_V2_MESSAGE_STATUS_REQUEST},
        {"valid-status.json", DECK_PAIRING_V2_MESSAGE_STATUS},
        {"valid-cancel.json", DECK_PAIRING_V2_MESSAGE_CANCEL},
        {"valid-error.json", DECK_PAIRING_V2_MESSAGE_ERROR},
    };
    deck_pairing_v2_message_t message{};
    for (const AcceptedFixture &test : accepted) {
        const std::string document = fixture(test.name);
        assert(deck_pairing_v2_contract_decode(
            document.data(), document.size(), &crypto, &message
        ));
        assert(message.type == test.type);
        assert(std::strcmp(message.common.session_id, "00112233445566778899aabbccddeeff") == 0);
        deck_pairing_v2_contract_clear(&message);
    }

    constexpr const char *rejected[] = {
        "invalid-duplicate-sequence.json",
        "invalid-escaped-type.json",
        "invalid-extra-secret.json",
        "invalid-major.json",
        "invalid-certificate-mismatch.json",
        "invalid-commit-sequence.json",
    };
    for (const char *name : rejected) {
        const std::string document = fixture(name);
        assert(!deck_pairing_v2_contract_decode(
            document.data(), document.size(), &crypto, &message
        ));
        assert(message.type == DECK_PAIRING_V2_MESSAGE_INVALID);
    }
}

void credentials_are_owned_and_cleared()
{
    const deck_pairing_v2_crypto_t crypto{fixture_sha256, nullptr};
    const std::string document = fixture("valid-credentials.json");
    deck_pairing_v2_message_t message{};
    assert(deck_pairing_v2_contract_decode(
        document.data(), document.size(), &crypto, &message
    ));
    assert(message.credentials.certificate_der_size == 27);
    assert(std::memcmp(
        message.credentials.certificate_der,
        "pairing-v2-test-certificate",
        message.credentials.certificate_der_size
    ) == 0);
    assert(std::strcmp(message.credentials.hub_address, "192.168.31.3:7780") == 0);
    assert(std::strcmp(
        message.credentials.token,
        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    ) == 0);
    deck_pairing_v2_contract_clear(&message);
    const uint8_t *bytes = reinterpret_cast<const uint8_t *>(&message);
    for (size_t index = 0; index < sizeof(message); ++index) {
        assert(bytes[index] == 0);
    }
}

void malformed_documents_fail_closed()
{
    const deck_pairing_v2_crypto_t crypto{fixture_sha256, nullptr};
    deck_pairing_v2_message_t message{};
    constexpr char escaped[] =
        "{\"type\":\"pairing\\u002ecommit\",\"protocol_version\":2,"
        "\"session_id\":\"00112233445566778899aabbccddeeff\","
        "\"transaction_id\":\"ffeeddccbbaa99887766554433221100\","
        "\"sequence\":3,\"deck_nonce\":\"11111111111111111111111111111111\","
        "\"transcript_sha256\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"}";
    assert(!deck_pairing_v2_contract_decode(
        escaped, sizeof(escaped) - 1U, &crypto, &message
    ));
    std::string oversized(DECK_PAIRING_V2_MAX_DOCUMENT_BYTES + 1U, 'x');
    assert(!deck_pairing_v2_contract_decode(
        oversized.data(), oversized.size(), &crypto, &message
    ));
    assert(!deck_pairing_v2_contract_decode(nullptr, 0, &crypto, &message));
}

}  // namespace

int main()
{
    shared_fixtures_match_the_firmware_contract();
    credentials_are_owned_and_cleared();
    malformed_documents_fail_closed();
    return 0;
}
