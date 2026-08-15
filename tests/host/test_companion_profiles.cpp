#include "deck_companion_profiles.h"

#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <map>
#include <string>
#include <thread>
#include <vector>

namespace {

struct FakeStorage {
    std::map<deck_companion_storage_key_t, std::vector<uint8_t>> values;
    deck_companion_storage_key_t fail_write = DECK_COMPANION_STORAGE_KEY_COUNT;
};

deck_companion_storage_result_t read_storage(
    void *context,
    deck_companion_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    const auto found = storage->values.find(key);
    if (found == storage->values.end()) {
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
    auto *storage = static_cast<FakeStorage *>(context);
    if (key == storage->fail_write || data == nullptr || size == 0) {
        return false;
    }
    storage->values[key] = std::vector<uint8_t>(data, data + size);
    return true;
}

bool erase_storage(void *context, deck_companion_storage_key_t key)
{
    static_cast<FakeStorage *>(context)->values.erase(key);
    return true;
}

struct FakePairing {
    bool succeed = true;
    unsigned calls = 0;
    std::string last_address;
    std::string last_pairing_address;
    std::string last_code;
    deck_companion_pairing_credential_t next{};
};

bool redeem(
    void *context,
    const char *address,
    const char *pairing_address,
    const char *code,
    deck_companion_pairing_credential_t *credential
)
{
    auto *pairing = static_cast<FakePairing *>(context);
    ++pairing->calls;
    pairing->last_address = address;
    pairing->last_pairing_address = pairing_address;
    pairing->last_code = code;
    if (!pairing->succeed) {
        return false;
    }
    *credential = pairing->next;
    return true;
}

void copy_string(char *destination, size_t capacity, const std::string &value)
{
    assert(value.size() < capacity);
    std::memcpy(destination, value.c_str(), value.size() + 1);
}

void set_credential(FakePairing *pairing, unsigned index)
{
    pairing->next = {};
    copy_string(
        pairing->next.token,
        sizeof(pairing->next.token),
        std::string(43, static_cast<char>('A' + index))
    );
    const char hex = "0123456789abcdef"[index % 16U];
    copy_string(
        pairing->next.certificate_fingerprint,
        sizeof(pairing->next.certificate_fingerprint),
        "sha256:" + std::string(64, hex)
    );
    const uint8_t certificate_der[] = {
        0x30,
        0x03,
        static_cast<uint8_t>(0x10U + index),
        0x00,
    };
    std::memcpy(
        pairing->next.certificate_der,
        certificate_der,
        sizeof(certificate_der)
    );
    pairing->next.certificate_der_size = sizeof(certificate_der);
    pairing->next.protocol_version = 1;
}

deck_companion_profiles_t *create_profiles(FakeStorage *storage, FakePairing *pairing)
{
    const deck_companion_profiles_options_t options = {
        {read_storage, write_storage, erase_storage, storage},
        {redeem, pairing},
    };
    return deck_companion_profiles_create(&options);
}

deck_companion_profiles_snapshot_t snapshot(deck_companion_profiles_t *profiles)
{
    deck_companion_profiles_snapshot_t value{};
    assert(deck_companion_profiles_snapshot(profiles, &value));
    return value;
}

deck_companion_pair_request_t request(unsigned index)
{
    deck_companion_pair_request_t value{};
    copy_string(
        value.hub_address,
        sizeof(value.hub_address),
        "companion-" + std::to_string(index) + ".local:" +
            std::to_string(7780U + index)
    );
    copy_string(
        value.pairing_address,
        sizeof(value.pairing_address),
        "192.168.4.2:" + std::to_string(7780U + index)
    );
    copy_string(value.code, sizeof(value.code), "123456");
    return value;
}

void successful_pairing_commits_a_redacted_active_profile()
{
    FakeStorage storage;
    FakePairing pairing;
    set_credential(&pairing, 1);
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    assert(profiles != nullptr);
    assert(snapshot(profiles).count == 0);

    const deck_companion_pair_request_t pairing_request = request(1);
    assert(deck_companion_profiles_pair(profiles, &pairing_request) ==
           DECK_COMPANION_PAIR_PAIRED);
    const deck_companion_profiles_snapshot_t current = snapshot(profiles);
    assert(current.count == 1);
    assert(current.has_active);
    assert(std::string(current.active_profile_id) ==
           pairing.next.certificate_fingerprint);
    assert(std::string(current.profiles[0].profile_id) ==
           pairing.next.certificate_fingerprint);
    assert(std::string(current.profiles[0].display_name) == pairing_request.hub_address);
    assert(std::string(current.profiles[0].hub_address) == pairing_request.hub_address);
    assert(current.profiles[0].profile_version == 1);
    assert(pairing.calls == 1);
    assert(pairing.last_pairing_address == "192.168.4.2:7781");
    assert(pairing.last_code == "123456");

    deck_companion_profile_secret_t active{};
    assert(deck_companion_profiles_active_secret(profiles, &active));
    assert(std::string(active.token) == pairing.next.token);
    assert(active.certificate_der_size == pairing.next.certificate_der_size);
    assert(std::memcmp(
               active.certificate_der,
               pairing.next.certificate_der,
               active.certificate_der_size
           ) == 0);
    deck_companion_profile_secret_clear(&active);
    assert(active.token[0] == '\0');
    assert(active.certificate_der_size == 0);
    deck_companion_profiles_destroy(profiles);

    profiles = create_profiles(&storage, &pairing);
    assert(profiles != nullptr);
    assert(snapshot(profiles).count == 1);
    assert(deck_companion_profiles_active_secret(profiles, &active));
    assert(std::string(active.token) == pairing.next.token);
    assert(active.certificate_der_size == pairing.next.certificate_der_size);
    assert(std::memcmp(
               active.certificate_der,
               pairing.next.certificate_der,
               active.certificate_der_size
           ) == 0);
    deck_companion_profile_secret_clear(&active);
    deck_companion_profiles_destroy(profiles);
}

void failed_pairing_and_invalid_inputs_preserve_existing_state()
{
    FakeStorage storage;
    FakePairing pairing;
    set_credential(&pairing, 2);
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    const deck_companion_pair_request_t first = request(2);
    assert(deck_companion_profiles_pair(profiles, &first) == DECK_COMPANION_PAIR_PAIRED);
    const auto before = storage.values;

    pairing.succeed = false;
    deck_companion_pair_request_t failed = request(3);
    assert(deck_companion_profiles_pair(profiles, &failed) ==
           DECK_COMPANION_PAIR_REDEEM_FAILED);
    assert(storage.values == before);
    assert(snapshot(profiles).count == 1);

    const char *bad_addresses[] = {
        "http://companion.local:7780",
        "user@companion.local:7780",
        "companion.local:7780/path",
        "companion.local",
        "companion.local:0",
        "companion.local:65536",
        "companion.local:7780\rspoof",
    };
    const unsigned calls_before = pairing.calls;
    for (const char *address : bad_addresses) {
        failed = {};
        copy_string(failed.hub_address, sizeof(failed.hub_address), address);
        copy_string(failed.code, sizeof(failed.code), "123456");
        assert(deck_companion_profiles_pair(profiles, &failed) ==
               DECK_COMPANION_PAIR_INVALID_ADDRESS);
    }
    failed = request(4);
    copy_string(failed.code, sizeof(failed.code), "12x456");
    assert(deck_companion_profiles_pair(profiles, &failed) ==
           DECK_COMPANION_PAIR_INVALID_CODE);
    failed = request(4);
    copy_string(
        failed.pairing_address,
        sizeof(failed.pairing_address),
        "10.0.0.2:7784"
    );
    assert(deck_companion_profiles_pair(profiles, &failed) ==
           DECK_COMPANION_PAIR_INVALID_ADDRESS);
    failed = request(4);
    copy_string(
        failed.pairing_address,
        sizeof(failed.pairing_address),
        "192.168.4.2:7785"
    );
    assert(deck_companion_profiles_pair(profiles, &failed) ==
           DECK_COMPANION_PAIR_INVALID_ADDRESS);
    assert(pairing.calls == calls_before);
    deck_companion_profiles_destroy(profiles);
}

void capacity_update_selection_and_revoke_are_transactional()
{
    FakeStorage storage;
    FakePairing pairing;
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    for (unsigned index = 0; index < DECK_COMPANION_MAX_PROFILES; ++index) {
        set_credential(&pairing, index);
        const deck_companion_pair_request_t add = request(index);
        assert(deck_companion_profiles_pair(profiles, &add) ==
               DECK_COMPANION_PAIR_PAIRED);
    }
    assert(snapshot(profiles).count == DECK_COMPANION_MAX_PROFILES);

    set_credential(&pairing, 9);
    const deck_companion_pair_request_t overflow = request(9);
    assert(deck_companion_profiles_pair(profiles, &overflow) ==
           DECK_COMPANION_PAIR_CAPACITY_REACHED);
    assert(snapshot(profiles).count == DECK_COMPANION_MAX_PROFILES);

    set_credential(&pairing, 2);
    const deck_companion_pair_request_t replacement = request(8);
    assert(deck_companion_profiles_pair(profiles, &replacement) ==
           DECK_COMPANION_PAIR_PAIRED);
    assert(snapshot(profiles).count == DECK_COMPANION_MAX_PROFILES);
    bool saw_updated = false;
    for (size_t index = 0; index < snapshot(profiles).count; ++index) {
        const deck_companion_profile_view_t &profile = snapshot(profiles).profiles[index];
        if (std::string(profile.profile_id) == pairing.next.certificate_fingerprint) {
            assert(std::string(profile.hub_address) == replacement.hub_address);
            saw_updated = true;
        }
    }
    assert(saw_updated);

    const std::string selected = snapshot(profiles).profiles[0].profile_id;
    assert(deck_companion_profiles_select_active(profiles, selected.c_str()) ==
           DECK_COMPANION_PROFILE_UPDATED);
    assert(std::string(snapshot(profiles).active_profile_id) == selected);
    const uint32_t before_priority = snapshot(profiles).generation;
    assert(deck_companion_profiles_set_priority(
               profiles,
               selected.c_str(),
               42
           ) == DECK_COMPANION_PROFILE_UPDATED);
    const deck_companion_profiles_snapshot_t prioritized = snapshot(profiles);
    assert(prioritized.generation > before_priority);
    bool saw_priority = false;
    for (size_t index = 0; index < prioritized.count; ++index) {
        if (std::string(prioritized.profiles[index].profile_id) == selected) {
            assert(prioritized.profiles[index].priority == 42);
            saw_priority = true;
        }
    }
    assert(saw_priority);
    assert(deck_companion_profiles_revoke(profiles, selected.c_str()) ==
           DECK_COMPANION_PROFILE_UPDATED);
    const deck_companion_profiles_snapshot_t after_revoke = snapshot(profiles);
    assert(after_revoke.count == DECK_COMPANION_MAX_PROFILES - 1);
    assert(after_revoke.has_active);
    assert(std::string(after_revoke.active_profile_id) != selected);
    deck_companion_profiles_destroy(profiles);
}

void storage_failure_keeps_the_last_valid_set_across_restart()
{
    FakeStorage storage;
    FakePairing pairing;
    set_credential(&pairing, 1);
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    const deck_companion_pair_request_t first = request(1);
    assert(deck_companion_profiles_pair(profiles, &first) == DECK_COMPANION_PAIR_PAIRED);
    const std::string old_id = snapshot(profiles).active_profile_id;

    storage.fail_write = DECK_COMPANION_STORAGE_ACTIVE_MARKER;
    set_credential(&pairing, 2);
    const deck_companion_pair_request_t second = request(2);
    assert(deck_companion_profiles_pair(profiles, &second) ==
           DECK_COMPANION_PAIR_STORAGE_FAILURE);
    assert(snapshot(profiles).count == 1);
    assert(std::string(snapshot(profiles).active_profile_id) == old_id);
    deck_companion_profiles_destroy(profiles);

    storage.fail_write = DECK_COMPANION_STORAGE_KEY_COUNT;
    profiles = create_profiles(&storage, &pairing);
    assert(profiles != nullptr);
    assert(snapshot(profiles).count == 1);
    assert(std::string(snapshot(profiles).active_profile_id) == old_id);
    deck_companion_profiles_destroy(profiles);
}

void concurrent_setup_submissions_serialize_duplicate_profile_updates()
{
    FakeStorage storage;
    FakePairing pairing;
    set_credential(&pairing, 4);
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    const deck_companion_pair_request_t same = request(4);
    constexpr size_t kWorkers = 12;
    std::vector<deck_companion_pair_result_t> results(
        kWorkers,
        DECK_COMPANION_PAIR_STORAGE_FAILURE
    );
    std::vector<std::thread> workers;
    workers.reserve(kWorkers);
    for (size_t index = 0; index < kWorkers; ++index) {
        workers.emplace_back([profiles, &same, &results, index]() {
            results[index] = deck_companion_profiles_pair(profiles, &same);
        });
    }
    for (std::thread &worker : workers) {
        worker.join();
    }
    for (const deck_companion_pair_result_t result : results) {
        assert(result == DECK_COMPANION_PAIR_PAIRED);
    }
    const deck_companion_profiles_snapshot_t current = snapshot(profiles);
    assert(current.count == 1);
    assert(current.generation == kWorkers);
    assert(pairing.calls == kWorkers);
    deck_companion_profiles_destroy(profiles);
}

void candidate_success_is_atomic_and_cannot_override_a_newer_manual_selection()
{
    FakeStorage storage;
    FakePairing pairing;
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    std::string ids[3];
    for (unsigned index = 0; index < 3; ++index) {
        set_credential(&pairing, index);
        ids[index] = pairing.next.certificate_fingerprint;
        const deck_companion_pair_request_t add = request(index);
        assert(deck_companion_profiles_pair(profiles, &add) ==
               DECK_COMPANION_PAIR_PAIRED);
    }
    assert(deck_companion_profiles_select_active(profiles, ids[0].c_str()) ==
           DECK_COMPANION_PROFILE_UPDATED);
    const uint32_t failover_generation = snapshot(profiles).generation;

    deck_companion_profile_secret_t candidate{};
    assert(deck_companion_profiles_secret_for(
        profiles,
        ids[1].c_str(),
        failover_generation,
        &candidate
    ));
    assert(std::string(candidate.profile_id) == ids[1]);
    deck_companion_profile_secret_clear(&candidate);

    assert(deck_companion_profiles_activate_on_success(
               profiles,
               ids[1].c_str(),
               failover_generation,
               1'234'000
           ) == DECK_COMPANION_PROFILE_UPDATED);
    deck_companion_profiles_snapshot_t current = snapshot(profiles);
    assert(std::string(current.active_profile_id) == ids[1]);
    bool saw_success = false;
    for (size_t index = 0; index < current.count; ++index) {
        if (std::string(current.profiles[index].profile_id) == ids[1]) {
            assert(current.profiles[index].last_success_unix_ms == 1'234'000);
            saw_success = true;
        }
    }
    assert(saw_success);

    const uint32_t monotonic_generation = current.generation;
    assert(deck_companion_profiles_activate_on_success(
               profiles,
               ids[1].c_str(),
               monotonic_generation,
               1'000
           ) == DECK_COMPANION_PROFILE_UPDATED);
    current = snapshot(profiles);
    for (size_t index = 0; index < current.count; ++index) {
        if (std::string(current.profiles[index].profile_id) == ids[1]) {
            assert(current.profiles[index].last_success_unix_ms == 1'234'000);
        }
    }

    const uint32_t stale_generation = current.generation;
    assert(deck_companion_profiles_select_active(profiles, ids[2].c_str()) ==
           DECK_COMPANION_PROFILE_UPDATED);
    assert(deck_companion_profiles_activate_on_success(
               profiles,
               ids[0].c_str(),
               stale_generation,
               1'235'000
           ) == DECK_COMPANION_PROFILE_STALE_GENERATION);
    current = snapshot(profiles);
    assert(std::string(current.active_profile_id) == ids[2]);
    assert(!deck_companion_profiles_secret_for(
        profiles,
        ids[0].c_str(),
        stale_generation,
        &candidate
    ));
    deck_companion_profiles_destroy(profiles);
}

void failed_candidate_commit_preserves_the_active_and_other_profile_secrets()
{
    FakeStorage storage;
    FakePairing pairing;
    deck_companion_profiles_t *profiles = create_profiles(&storage, &pairing);
    std::string ids[2];
    std::string tokens[2];
    for (unsigned index = 0; index < 2; ++index) {
        set_credential(&pairing, index);
        ids[index] = pairing.next.certificate_fingerprint;
        tokens[index] = pairing.next.token;
        const deck_companion_pair_request_t add = request(index);
        assert(deck_companion_profiles_pair(profiles, &add) ==
               DECK_COMPANION_PAIR_PAIRED);
    }
    assert(deck_companion_profiles_select_active(profiles, ids[0].c_str()) ==
           DECK_COMPANION_PROFILE_UPDATED);
    const uint32_t generation = snapshot(profiles).generation;
    storage.fail_write = DECK_COMPANION_STORAGE_ACTIVE_MARKER;
    assert(deck_companion_profiles_activate_on_success(
               profiles,
               ids[1].c_str(),
               generation,
               55'000
           ) == DECK_COMPANION_PROFILE_STORAGE_FAILURE);
    deck_companion_profiles_snapshot_t current = snapshot(profiles);
    assert(std::string(current.active_profile_id) == ids[0]);

    deck_companion_profile_secret_t secret{};
    assert(deck_companion_profiles_secret_for(
        profiles,
        ids[1].c_str(),
        current.generation,
        &secret
    ));
    assert(std::string(secret.token) == tokens[1]);
    deck_companion_profile_secret_clear(&secret);
    assert(deck_companion_profiles_revoke(profiles, ids[1].c_str()) ==
           DECK_COMPANION_PROFILE_STORAGE_FAILURE);
    current = snapshot(profiles);
    assert(current.count == 2);
    assert(std::string(current.active_profile_id) == ids[0]);
    deck_companion_profiles_destroy(profiles);
}

}  // namespace

int main()
{
    successful_pairing_commits_a_redacted_active_profile();
    failed_pairing_and_invalid_inputs_preserve_existing_state();
    capacity_update_selection_and_revoke_are_transactional();
    storage_failure_keeps_the_last_valid_set_across_restart();
    concurrent_setup_submissions_serialize_duplicate_profile_updates();
    candidate_success_is_atomic_and_cannot_override_a_newer_manual_selection();
    failed_candidate_commit_preserves_the_active_and_other_profile_secrets();
    return 0;
}
