#include "deck_pairing_v2_transaction.h"

#include <array>
#include <cassert>
#include <cstring>
#include <fstream>
#include <iterator>
#include <map>
#include <string>
#include <vector>

namespace {

struct FakeStorage {
    std::map<deck_companion_storage_key_t, std::vector<uint8_t>> values;
};

deck_companion_storage_result_t read_storage(
    void *context,
    deck_companion_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    const auto &values = static_cast<FakeStorage *>(context)->values;
    const auto found = values.find(key);
    if (found == values.end()) {
        return DECK_COMPANION_STORAGE_NOT_FOUND;
    }
    if (output == nullptr || size == nullptr || capacity < found->second.size()) {
        return DECK_COMPANION_STORAGE_ERROR;
    }
    std::memcpy(output, found->second.data(), found->second.size());
    *size = found->second.size();
    return DECK_COMPANION_STORAGE_OK;
}

bool write_storage(
    void *context,
    deck_companion_storage_key_t key,
    const uint8_t *data,
    size_t size
)
{
    if (data == nullptr || size == 0) {
        return false;
    }
    static_cast<FakeStorage *>(context)->values[key] =
        std::vector<uint8_t>(data, data + size);
    return true;
}

bool erase_storage(void *context, deck_companion_storage_key_t key)
{
    static_cast<FakeStorage *>(context)->values.erase(key);
    return true;
}

bool unused_redeem(
    void *,
    const char *,
    const char *,
    const char *,
    deck_companion_pairing_credential_t *
)
{
    return false;
}

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

bool fake_sha256(void *, const uint8_t *input, size_t input_size, uint8_t output[32])
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
    } else {
        std::memcpy(output, transcript_digest.data(), transcript_digest.size());
    }
    return true;
}

bool fake_random(void *, uint8_t *output, size_t size)
{
    if (output == nullptr || size != 16U) {
        return false;
    }
    std::memset(output, 0x11, size);
    return true;
}

bool fake_identity(
    void *,
    char *device_id,
    size_t device_id_capacity,
    char *device_identity,
    size_t device_identity_capacity
)
{
    constexpr char id[] = "deck_12345678";
    constexpr char identity[] = "ZGV2aWNlLWlkZW50aXR5LTE";
    if (device_id_capacity < sizeof(id) || device_identity_capacity < sizeof(identity)) {
        return false;
    }
    std::memcpy(device_id, id, sizeof(id));
    std::memcpy(device_identity, identity, sizeof(identity));
    return true;
}

struct Fixture {
    FakeStorage storage;
    deck_companion_profiles_t *profiles = nullptr;
    deck_pairing_v2_transaction_t *transaction = nullptr;

    Fixture()
    {
        const deck_companion_profiles_options_t profile_options = {
            {read_storage, write_storage, erase_storage, &storage},
            {unused_redeem, nullptr},
        };
        profiles = deck_companion_profiles_create(&profile_options);
        assert(profiles != nullptr);
        const deck_pairing_v2_transaction_options_t options = {
            profiles,
            {fake_sha256, nullptr},
            fake_random,
            fake_identity,
            nullptr,
        };
        transaction = deck_pairing_v2_transaction_create(&options);
        assert(transaction != nullptr);
        assert(deck_pairing_v2_transaction_begin_window(
            transaction,
            "0123456789abcdef0123456789abcdef"
        ));
    }

    ~Fixture()
    {
        deck_pairing_v2_transaction_destroy(transaction);
        deck_companion_profiles_destroy(profiles);
    }
};

deck_pairing_v2_transaction_result_t exchange(
    Fixture *value,
    const std::string &document,
    std::string *response,
    deck_pairing_v2_transaction_action_t *action
)
{
    char output[DECK_PAIRING_V2_MAX_DOCUMENT_BYTES]{};
    size_t output_size = 0;
    const auto result = deck_pairing_v2_transaction_exchange(
        value->transaction,
        document.data(),
        document.size(),
        output,
        sizeof(output),
        &output_size,
        action
    );
    response->assign(output, output_size);
    std::memset(output, 0, sizeof(output));
    return result;
}

void profile_is_invisible_until_link_proof_and_commit()
{
    Fixture value;
    std::string response;
    deck_pairing_v2_transaction_action_t action = DECK_PAIRING_V2_ACTION_NONE;
    assert(exchange(
               &value,
               fixture("valid-credentials.json"),
               &response,
               &action
           ) == DECK_PAIRING_V2_TRANSACTION_OK);
    assert(action == DECK_PAIRING_V2_ACTION_START_LINK_PROOF);
    std::string expected_ready = fixture("valid-commit-ready.json");
    while (!expected_ready.empty() &&
           (expected_ready.back() == '\n' || expected_ready.back() == '\r')) {
        expected_ready.pop_back();
    }
    assert(response == expected_ready);

    deck_companion_profiles_snapshot_t profiles{};
    assert(deck_companion_profiles_snapshot(value.profiles, &profiles));
    assert(!profiles.has_active && profiles.count == 0);

    deck_pairing_v2_link_request_t link{};
    assert(deck_pairing_v2_transaction_link_request(value.transaction, &link));
    assert(std::strcmp(link.device_id, "deck_12345678") == 0);
    assert(std::strcmp(
        link.secret.token,
        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    ) == 0);
    assert(!deck_pairing_v2_transaction_mark_link_proven(
        value.transaction,
        link.session_id,
        "00000000000000000000000000000000",
        1'787'040'000'000ULL
    ));

    std::string receipt;
    assert(exchange(
               &value,
               fixture("valid-commit.json"),
               &receipt,
               &action
           ) == DECK_PAIRING_V2_TRANSACTION_LINK_REQUIRED);
    assert(deck_pairing_v2_transaction_mark_link_proven(
        value.transaction,
        link.session_id,
        link.transaction_id,
        1'787'040'000'000ULL
    ));
    deck_pairing_v2_link_request_clear(&link);
    assert(exchange(
               &value,
               fixture("valid-commit.json"),
               &receipt,
               &action
           ) == DECK_PAIRING_V2_TRANSACTION_OK);
    assert(action == DECK_PAIRING_V2_ACTION_PROFILE_COMMITTED);
    assert(deck_companion_profiles_snapshot(value.profiles, &profiles));
    assert(profiles.has_active && profiles.count == 1 && profiles.generation == 1);

    const deck_pairing_v2_crypto_t crypto{fake_sha256, nullptr};
    deck_pairing_v2_message_t decoded{};
    assert(deck_pairing_v2_contract_decode(
        receipt.data(), receipt.size(), &crypto, &decoded
    ));
    assert(decoded.type == DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT);
    assert(decoded.profile_generation == 1);
    deck_pairing_v2_contract_clear(&decoded);
}

void reset_cancels_the_uncommitted_profile()
{
    Fixture value;
    std::string response;
    deck_pairing_v2_transaction_action_t action = DECK_PAIRING_V2_ACTION_NONE;
    assert(exchange(
               &value,
               fixture("valid-credentials.json"),
               &response,
               &action
           ) == DECK_PAIRING_V2_TRANSACTION_OK);
    deck_pairing_v2_transaction_reset(value.transaction);
    deck_pairing_v2_link_request_t link{};
    assert(!deck_pairing_v2_transaction_link_request(value.transaction, &link));
    deck_companion_profiles_snapshot_t profiles{};
    assert(deck_companion_profiles_snapshot(value.profiles, &profiles));
    assert(!profiles.has_active && profiles.count == 0);
}

}  // namespace

int main()
{
    profile_is_invisible_until_link_proof_and_commit();
    reset_cancels_the_uncommitted_profile();
    return 0;
}
