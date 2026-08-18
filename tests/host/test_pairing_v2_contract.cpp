#include "deck_pairing_v2_contract.h"

#include <array>
#include <cassert>
#include <cstring>
#include <fstream>
#include <iterator>
#include <string>
#include <vector>

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

std::vector<uint8_t> expected_transcript()
{
    std::vector<uint8_t> transcript;
    const auto append = [&transcript](const char *label, const uint8_t *value, size_t size) {
        transcript.insert(transcript.end(), label, label + std::strlen(label));
        transcript.push_back(0);
        transcript.push_back(static_cast<uint8_t>((size >> 24U) & 0xffU));
        transcript.push_back(static_cast<uint8_t>((size >> 16U) & 0xffU));
        transcript.push_back(static_cast<uint8_t>((size >> 8U) & 0xffU));
        transcript.push_back(static_cast<uint8_t>(size & 0xffU));
        transcript.insert(transcript.end(), value, value + size);
    };
    const auto text = [&append](const char *label, const char *value) {
        append(label, reinterpret_cast<const uint8_t *>(value), std::strlen(value));
    };
    constexpr char domain[] = "s3-rlcd-pairing-v2-transcript";
    transcript.insert(transcript.end(), domain, domain + sizeof(domain));
    const uint8_t version[] = {0, 0, 0, 2};
    append("protocol_version", version, sizeof(version));
    text("session_id", "00112233445566778899aabbccddeeff");
    text("transaction_id", "ffeeddccbbaa99887766554433221100");
    text("window_nonce", "0123456789abcdef0123456789abcdef");
    text("companion_nonce", "abcdef0123456789abcdef0123456789");
    text("hub_service", "s3deck-companion-a1b2._s3rlcd-hub._tcp.local.");
    text("hub_address", "192.168.31.3:7780");
    text("token", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA");
    text(
        "certificate_fingerprint",
        "sha256:c8127643942ee8ff1ffebd2a76430e8c88c14ac3026ae2855565f191d8726bc5"
    );
    constexpr char certificate[] = "pairing-v2-test-certificate";
    append(
        "certificate_der",
        reinterpret_cast<const uint8_t *>(certificate),
        sizeof(certificate) - 1U
    );
    const uint8_t device_link_protocol[] = {0, 0, 0, 1};
    append("device_link_protocol", device_link_protocol, sizeof(device_link_protocol));
    text("deck_nonce", "11111111111111111111111111111111");
    text("device_id", "deck_12345678");
    text("device_identity", "ZGV2aWNlLWlkZW50aXR5LTE");
    text(
        "profile_id",
        "sha256:c8127643942ee8ff1ffebd2a76430e8c88c14ac3026ae2855565f191d8726bc5"
    );
    return transcript;
}

bool fixture_sha256(void *, const uint8_t *input, size_t input_size, uint8_t output[32])
{
    constexpr char certificate[] = "pairing-v2-test-certificate";
    constexpr std::array<uint8_t, 32> certificate_digest = {
        0xc8, 0x12, 0x76, 0x43, 0x94, 0x2e, 0xe8, 0xff,
        0x1f, 0xfe, 0xbd, 0x2a, 0x76, 0x43, 0x0e, 0x8c,
        0x88, 0xc1, 0x4a, 0xc3, 0x02, 0x6a, 0xe2, 0x85,
        0x55, 0x65, 0xf1, 0x91, 0xd8, 0x72, 0x6b, 0xc5,
    };
    constexpr std::array<uint8_t, 32> transcript_digest = {
        0xed, 0x73, 0xb9, 0x9f, 0xc5, 0x0c, 0x3d, 0x32,
        0xbc, 0xb6, 0x40, 0x4f, 0x42, 0xc1, 0x18, 0x90,
        0x58, 0x5d, 0x23, 0x62, 0xaf, 0x77, 0xb7, 0x55,
        0x78, 0x14, 0x0d, 0xd1, 0x61, 0x9d, 0x04, 0x93,
    };
    if (input == nullptr || output == nullptr) {
        return false;
    }
    if (input_size == sizeof(certificate) - 1U &&
        std::memcmp(input, certificate, sizeof(certificate) - 1U) == 0) {
        std::memcpy(output, certificate_digest.data(), certificate_digest.size());
        return true;
    }
    const std::vector<uint8_t> transcript = expected_transcript();
    if (input_size == transcript.size() &&
        std::memcmp(input, transcript.data(), transcript.size()) == 0) {
        std::memcpy(output, transcript_digest.data(), transcript_digest.size());
        return true;
    }
    return false;
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

void canonical_transcript_matches_the_cross_end_kat()
{
    const deck_pairing_v2_crypto_t crypto{fixture_sha256, nullptr};
    deck_pairing_v2_message_t credentials{};
    deck_pairing_v2_message_t ready{};
    const std::string credentials_document = fixture("valid-credentials.json");
    const std::string ready_document = fixture("valid-commit-ready.json");
    assert(deck_pairing_v2_contract_decode(
        credentials_document.data(), credentials_document.size(), &crypto, &credentials
    ));
    assert(deck_pairing_v2_contract_decode(
        ready_document.data(), ready_document.size(), &crypto, &ready
    ));
    char digest[DECK_PAIRING_V2_DIGEST_CAPACITY]{};
    assert(deck_pairing_v2_contract_transcript_sha256(
        &credentials, &ready, &crypto, digest
    ));
    assert(std::strcmp(
        digest,
        "sha256:ed73b99fc50c3d32bcb6404f42c11890585d2362af77b75578140dd1619d0493"
    ) == 0);
    assert(std::strcmp(digest, ready.commit_ready.transcript_sha256) == 0);

    ready.commit_ready.profile_id[7] = '0';
    assert(!deck_pairing_v2_contract_transcript_sha256(
        &credentials, &ready, &crypto, digest
    ));
    for (char byte : digest) {
        assert(byte == 0);
    }
    deck_pairing_v2_contract_clear(&credentials);
    deck_pairing_v2_contract_clear(&ready);
}

void deck_owned_messages_encode_and_round_trip()
{
    const deck_pairing_v2_crypto_t crypto{fixture_sha256, nullptr};
    for (const char *name : {"valid-commit-ready.json", "valid-commit-receipt.json"}) {
        const std::string source = fixture(name);
        deck_pairing_v2_message_t message{};
        assert(deck_pairing_v2_contract_decode(
            source.data(), source.size(), &crypto, &message
        ));
        char encoded[DECK_PAIRING_V2_MAX_DOCUMENT_BYTES]{};
        size_t encoded_size = 0;
        assert(deck_pairing_v2_contract_encode(
            &message,
            encoded,
            sizeof(encoded),
            &encoded_size
        ));
        deck_pairing_v2_message_t round_trip{};
        assert(deck_pairing_v2_contract_decode(
            encoded,
            encoded_size,
            &crypto,
            &round_trip
        ));
        assert(round_trip.type == message.type);
        assert(std::strcmp(round_trip.common.session_id, message.common.session_id) == 0);
        if (message.type == DECK_PAIRING_V2_MESSAGE_COMMIT_READY) {
            assert(std::strcmp(
                round_trip.commit_ready.transcript_sha256,
                message.commit_ready.transcript_sha256
            ) == 0);
        } else {
            assert(round_trip.profile_generation == message.profile_generation);
            assert(std::strcmp(
                round_trip.transcript_sha256,
                message.transcript_sha256
            ) == 0);
        }
        deck_pairing_v2_contract_clear(&message);
        deck_pairing_v2_contract_clear(&round_trip);
    }
}

}  // namespace

int main()
{
    shared_fixtures_match_the_firmware_contract();
    credentials_are_owned_and_cleared();
    malformed_documents_fail_closed();
    canonical_transcript_matches_the_cross_end_kat();
    deck_owned_messages_encode_and_round_trip();
    return 0;
}
