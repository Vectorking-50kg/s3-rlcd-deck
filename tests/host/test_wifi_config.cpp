#include "deck_wifi_config.h"

#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <map>
#include <string>
#include <vector>

namespace {

struct FakeStorage {
    std::map<deck_wifi_storage_key_t, std::vector<uint8_t>> values;
    std::vector<deck_wifi_storage_key_t> writes;
    deck_wifi_storage_key_t fail_write = DECK_WIFI_STORAGE_KEY_COUNT;
    deck_wifi_storage_key_t fail_read = DECK_WIFI_STORAGE_KEY_COUNT;
    bool corrupt_candidate_write = false;
    bool corrupt_marker_write = false;
    unsigned unrelated_value = 42;
};

deck_wifi_storage_result_t read_storage(
    void *context,
    deck_wifi_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    if (key == storage->fail_read) {
        return DECK_WIFI_STORAGE_ERROR;
    }
    const auto value = storage->values.find(key);
    if (value == storage->values.end()) {
        return DECK_WIFI_STORAGE_NOT_FOUND;
    }
    if (size == nullptr || output == nullptr || capacity < value->second.size()) {
        return DECK_WIFI_STORAGE_ERROR;
    }
    std::memcpy(output, value->second.data(), value->second.size());
    *size = value->second.size();
    return DECK_WIFI_STORAGE_OK;
}

bool write_storage(
    void *context,
    deck_wifi_storage_key_t key,
    const uint8_t *data,
    size_t size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    storage->writes.push_back(key);
    if (key == storage->fail_write || (data == nullptr && size != 0)) {
        return false;
    }
    storage->values[key] = std::vector<uint8_t>(data, data + size);
    if (key == DECK_WIFI_STORAGE_CANDIDATE && storage->corrupt_candidate_write && size != 0) {
        storage->values[key].back() ^= 0x40U;
    }
    if (key == DECK_WIFI_STORAGE_ACTIVE_MARKER && storage->corrupt_marker_write && size != 0) {
        storage->values[key].back() ^= 0x20U;
    }
    return true;
}

bool erase_storage(void *context, deck_wifi_storage_key_t key)
{
    auto *storage = static_cast<FakeStorage *>(context);
    storage->values.erase(key);
    return true;
}

struct FakeWifi {
    bool begin_result = true;
    unsigned begin_count = 0;
    unsigned cancel_count = 0;
    deck_wifi_credentials_t last{};
};

bool begin_validation(void *context, const deck_wifi_credentials_t *credentials)
{
    auto *wifi = static_cast<FakeWifi *>(context);
    ++wifi->begin_count;
    wifi->last = *credentials;
    return wifi->begin_result;
}

void cancel_validation(void *context)
{
    auto *wifi = static_cast<FakeWifi *>(context);
    ++wifi->cancel_count;
}

deck_wifi_config_t *create_manager(
    FakeStorage *storage,
    FakeWifi *wifi,
    uint64_t timeout_ms = 15'000
)
{
    const deck_wifi_config_options_t options = {
        {read_storage, write_storage, erase_storage, storage},
        {begin_validation, cancel_validation, wifi},
        timeout_ms,
    };
    return deck_wifi_config_create(&options);
}

deck_wifi_credentials_t credentials(const char *ssid, const char *password)
{
    deck_wifi_credentials_t value{};
    std::strncpy(value.ssid, ssid, sizeof(value.ssid) - 1);
    std::strncpy(value.password, password, sizeof(value.password) - 1);
    return value;
}

void write_u32(std::vector<uint8_t> *output, size_t offset, uint32_t value)
{
    (*output)[offset] = static_cast<uint8_t>(value & 0xffU);
    (*output)[offset + 1] = static_cast<uint8_t>((value >> 8U) & 0xffU);
    (*output)[offset + 2] = static_cast<uint8_t>((value >> 16U) & 0xffU);
    (*output)[offset + 3] = static_cast<uint8_t>((value >> 24U) & 0xffU);
}

uint32_t crc32(const uint8_t *data, size_t size)
{
    uint32_t crc = 0xffffffffU;
    for (size_t index = 0; index < size; ++index) {
        crc ^= data[index];
        for (unsigned bit = 0; bit < 8; ++bit) {
            const uint32_t mask = 0U - (crc & 1U);
            crc = (crc >> 1U) ^ (0xedb88320U & mask);
        }
    }
    return ~crc;
}

std::vector<uint8_t> schema_v1_record(
    const deck_wifi_credentials_t &value,
    uint32_t generation
)
{
    const size_t ssid_size = std::strlen(value.ssid);
    const size_t password_size = std::strlen(value.password);
    const size_t payload_size = ssid_size + password_size;
    std::vector<uint8_t> encoded(14 + payload_size + 4, 0);
    std::memcpy(encoded.data(), "DWF1", 4);
    encoded[4] = 1;
    encoded[6] = static_cast<uint8_t>(payload_size & 0xffU);
    encoded[7] = static_cast<uint8_t>((payload_size >> 8U) & 0xffU);
    write_u32(&encoded, 8, generation);
    encoded[12] = static_cast<uint8_t>(ssid_size);
    encoded[13] = static_cast<uint8_t>(password_size);
    std::memcpy(encoded.data() + 14, value.ssid, ssid_size);
    std::memcpy(encoded.data() + 14 + ssid_size, value.password, password_size);
    write_u32(&encoded, 14 + payload_size, crc32(encoded.data(), 14 + payload_size));
    return encoded;
}

std::vector<uint8_t> schema_v1_marker(uint8_t slot, uint32_t generation)
{
    std::vector<uint8_t> encoded(20, 0);
    std::memcpy(encoded.data(), "DWM1", 4);
    encoded[4] = 1;
    encoded[5] = slot;
    write_u32(&encoded, 6, generation);
    write_u32(&encoded, 16, crc32(encoded.data(), 16));
    return encoded;
}

deck_wifi_config_snapshot_t snapshot(deck_wifi_config_t *manager)
{
    deck_wifi_config_snapshot_t value{};
    assert(deck_wifi_config_snapshot(manager, &value));
    return value;
}

void candidate_is_committed_only_after_successful_validation()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    assert(manager != nullptr);
    assert(snapshot(manager).state == DECK_WIFI_CONFIG_NO_ACTIVE);

    const deck_wifi_credentials_t office = credentials("Office", "correct-horse");
    assert(deck_wifi_config_submit(manager, &office, 100) == DECK_WIFI_SUBMIT_ACCEPTED);
    deck_wifi_config_snapshot_t pending = snapshot(manager);
    assert(pending.state == DECK_WIFI_CONFIG_VALIDATING);
    assert(!pending.has_active);
    assert(pending.has_candidate);
    assert(std::string(pending.candidate_ssid) == "Office");
    assert(wifi.begin_count == 1);
    assert(std::string(wifi.last.password) == "correct-horse");
    const std::vector<deck_wifi_storage_key_t> candidate_writes = {
        DECK_WIFI_STORAGE_CANDIDATE,
    };
    assert(storage.writes == candidate_writes);

    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const deck_wifi_config_snapshot_t active = snapshot(manager);
    assert(active.state == DECK_WIFI_CONFIG_ACTIVE);
    assert(active.record_status == DECK_WIFI_RECORD_VALID);
    assert(active.has_active);
    assert(!active.has_candidate);
    assert(active.generation == 1);
    assert(std::string(active.active_ssid) == "Office");
    assert(!deck_wifi_config_recovery_required(&active));
    const std::vector<deck_wifi_storage_key_t> activation_writes = {
        DECK_WIFI_STORAGE_CANDIDATE,
        DECK_WIFI_STORAGE_SLOT_0,
        DECK_WIFI_STORAGE_ACTIVE_MARKER,
    };
    assert(storage.writes == activation_writes);
    deck_wifi_credentials_t restored{};
    assert(deck_wifi_config_active_credentials(manager, &restored));
    assert(std::string(restored.password) == "correct-horse");
    deck_wifi_config_destroy(manager);
}

void schema_v1_records_from_ticket_6_survive_the_shared_store_upgrade()
{
    FakeStorage storage;
    FakeWifi wifi;
    const deck_wifi_credentials_t active = credentials("Existing", "existing-password");
    const deck_wifi_credentials_t candidate = credentials("Pending", "pending-password");
    storage.values[DECK_WIFI_STORAGE_SLOT_0] = schema_v1_record(active, 7);
    storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER] = schema_v1_marker(0, 7);
    storage.values[DECK_WIFI_STORAGE_CANDIDATE] = schema_v1_record(candidate, 8);

    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    assert(manager != nullptr);
    const deck_wifi_config_snapshot_t restored = snapshot(manager);
    assert(restored.record_status == DECK_WIFI_RECORD_VALID);
    assert(restored.candidate_record_status == DECK_WIFI_RECORD_VALID);
    assert(restored.has_active);
    assert(restored.has_candidate);
    assert(restored.generation == 7);
    assert(std::string(restored.active_ssid) == "Existing");
    assert(std::string(restored.candidate_ssid) == "Pending");

    const deck_wifi_credentials_t replacement = credentials("New", "new-password");
    assert(deck_wifi_config_submit(manager, &replacement, 10) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    assert(snapshot(manager).generation == 8);
    assert(std::string(snapshot(manager).active_ssid) == "New");
    deck_wifi_config_destroy(manager);
}

void failed_and_timed_out_candidates_preserve_the_last_active_record()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi, 1'000);
    const deck_wifi_credentials_t old_config = credentials("Old", "old-password");
    assert(deck_wifi_config_submit(manager, &old_config, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const std::vector<uint8_t> old_marker = storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER];

    const deck_wifi_credentials_t bad = credentials("Bad", "bad-password");
    assert(deck_wifi_config_submit(manager, &bad, 100) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_AUTH_FAILED));
    deck_wifi_config_snapshot_t failed = snapshot(manager);
    assert(failed.state == DECK_WIFI_CONFIG_AUTH_FAILED);
    assert(failed.has_active);
    assert(deck_wifi_config_recovery_required(&failed));
    assert(std::string(failed.active_ssid) == "Old");
    assert(storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER] == old_marker);

    const deck_wifi_credentials_t slow = credentials("Slow", "slow-password");
    assert(deck_wifi_config_submit(manager, &slow, 1'000) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(!deck_wifi_config_tick(manager, 1'999));
    assert(deck_wifi_config_tick(manager, 2'000));
    const deck_wifi_config_snapshot_t timed_out = snapshot(manager);
    assert(timed_out.state == DECK_WIFI_CONFIG_TIMED_OUT);
    assert(std::string(timed_out.active_ssid) == "Old");
    assert(storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER] == old_marker);
    assert(wifi.cancel_count == 1);
    deck_wifi_config_destroy(manager);
}

void marker_failure_never_activates_the_new_slot()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t old_config = credentials("Old", "old-password");
    assert(deck_wifi_config_submit(manager, &old_config, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const std::vector<uint8_t> old_marker = storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER];

    storage.fail_write = DECK_WIFI_STORAGE_ACTIVE_MARKER;
    const deck_wifi_credentials_t replacement = credentials("New", "new-password");
    assert(deck_wifi_config_submit(manager, &replacement, 10) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(!deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const deck_wifi_config_snapshot_t current = snapshot(manager);
    assert(current.state == DECK_WIFI_CONFIG_STORAGE_ERROR);
    assert(std::string(current.active_ssid) == "Old");
    assert(storage.values[DECK_WIFI_STORAGE_ACTIVE_MARKER] == old_marker);
    const size_t write_count = storage.writes.size();
    storage.fail_write = DECK_WIFI_STORAGE_KEY_COUNT;
    assert(deck_wifi_config_submit(manager, &replacement, 20) ==
           DECK_WIFI_SUBMIT_STORAGE_ERROR);
    assert(storage.writes.size() == write_count);
    deck_wifi_config_destroy(manager);
}

void marker_is_verified_before_the_candidate_becomes_active()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t candidate = credentials("Office", "correct-horse");
    assert(deck_wifi_config_submit(manager, &candidate, 0) == DECK_WIFI_SUBMIT_ACCEPTED);

    storage.corrupt_marker_write = true;
    assert(!deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const deck_wifi_config_snapshot_t current = snapshot(manager);
    assert(current.state == DECK_WIFI_CONFIG_STORAGE_ERROR);
    assert(!current.has_active);
    assert(current.generation == 0);
    deck_wifi_config_destroy(manager);
}

void an_uncommitted_slot_is_never_used_as_the_previous_active_record()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t old_config = credentials("Old", "old-password");
    assert(deck_wifi_config_submit(manager, &old_config, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));

    const deck_wifi_credentials_t candidate = credentials("Candidate", "candidate-password");
    assert(deck_wifi_config_submit(manager, &candidate, 10) == DECK_WIFI_SUBMIT_ACCEPTED);
    storage.fail_write = DECK_WIFI_STORAGE_ACTIVE_MARKER;
    assert(!deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    deck_wifi_config_destroy(manager);

    storage.fail_write = DECK_WIFI_STORAGE_KEY_COUNT;
    storage.values[DECK_WIFI_STORAGE_SLOT_0].back() ^= 0x80U;
    manager = create_manager(&storage, &wifi);
    const deck_wifi_config_snapshot_t recovered = snapshot(manager);
    assert(!recovered.has_active);
    assert(std::string(recovered.active_ssid).empty());
    assert(recovered.record_status == DECK_WIFI_RECORD_CORRUPT);
    deck_wifi_config_destroy(manager);
}

void restart_ignores_failed_candidate_and_recovers_committed_active()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t old_config = credentials("Old", "old-password");
    assert(deck_wifi_config_submit(manager, &old_config, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    const deck_wifi_credentials_t failed = credentials("Failed", "failed-password");
    assert(deck_wifi_config_submit(manager, &failed, 100) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTION_FAILED));
    deck_wifi_config_destroy(manager);

    FakeWifi after_restart;
    manager = create_manager(&storage, &after_restart);
    const deck_wifi_config_snapshot_t restored = snapshot(manager);
    assert(restored.state == DECK_WIFI_CONFIG_ACTIVE);
    assert(restored.has_active);
    assert(restored.has_candidate);
    assert(std::string(restored.active_ssid) == "Old");
    assert(deck_wifi_config_active_connection(manager, false));
    assert(snapshot(manager).state == DECK_WIFI_CONFIG_CONNECTION_FAILED);
    assert(snapshot(manager).has_active);
    assert(deck_wifi_config_active_connection(manager, true));
    assert(snapshot(manager).state == DECK_WIFI_CONFIG_ACTIVE);
    deck_wifi_config_destroy(manager);
}

void candidate_io_failure_preserves_active_but_keeps_storage_fault_visible()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t old_config = credentials("Old", "old-password");
    assert(deck_wifi_config_submit(manager, &old_config, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    deck_wifi_config_destroy(manager);

    storage.fail_read = DECK_WIFI_STORAGE_CANDIDATE;
    manager = create_manager(&storage, &wifi);
    const deck_wifi_config_snapshot_t current = snapshot(manager);
    assert(current.has_active);
    assert(std::string(current.active_ssid) == "Old");
    assert(current.candidate_record_status == DECK_WIFI_RECORD_IO_ERROR);
    assert(current.state == DECK_WIFI_CONFIG_STORAGE_ERROR);
    const deck_wifi_credentials_t replacement = credentials("New", "new-password");
    assert(deck_wifi_config_submit(manager, &replacement, 10) ==
           DECK_WIFI_SUBMIT_STORAGE_ERROR);
    deck_wifi_config_destroy(manager);
}

void corrupt_unknown_and_legacy_records_do_not_replace_a_valid_fallback()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t first = credentials("First", "first-password");
    const deck_wifi_credentials_t second = credentials("Second", "second-password");
    assert(deck_wifi_config_submit(manager, &first, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    assert(deck_wifi_config_submit(manager, &second, 1) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    deck_wifi_config_destroy(manager);

    storage.values[DECK_WIFI_STORAGE_SLOT_1].back() ^= 0x80U;
    manager = create_manager(&storage, &wifi);
    deck_wifi_config_snapshot_t recovered = snapshot(manager);
    assert(recovered.record_status == DECK_WIFI_RECORD_RECOVERED_PREVIOUS);
    assert(deck_wifi_config_recovery_required(&recovered));
    assert(std::string(recovered.active_ssid) == "First");
    deck_wifi_config_destroy(manager);

    storage.values.erase(DECK_WIFI_STORAGE_SLOT_0);
    storage.values[DECK_WIFI_STORAGE_SLOT_1][4] = 2;
    manager = create_manager(&storage, &wifi);
    assert(snapshot(manager).record_status == DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA);
    assert(!snapshot(manager).has_active);
    deck_wifi_config_destroy(manager);

    storage.values[DECK_WIFI_STORAGE_SLOT_1][4] = 0;
    manager = create_manager(&storage, &wifi);
    assert(snapshot(manager).record_status == DECK_WIFI_RECORD_MIGRATION_FAILED);
    assert(!snapshot(manager).has_active);
    deck_wifi_config_destroy(manager);
}

void marker_failure_after_fallback_keeps_the_recovered_record_active()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t first = credentials("First", "first-password");
    const deck_wifi_credentials_t second = credentials("Second", "second-password");
    const deck_wifi_credentials_t replacement = credentials("Third", "third-password");
    assert(deck_wifi_config_submit(manager, &first, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    assert(deck_wifi_config_submit(manager, &second, 1) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    deck_wifi_config_destroy(manager);

    storage.values[DECK_WIFI_STORAGE_SLOT_1].back() ^= 0x80U;
    manager = create_manager(&storage, &wifi);
    assert(snapshot(manager).record_status == DECK_WIFI_RECORD_RECOVERED_PREVIOUS);
    assert(std::string(snapshot(manager).active_ssid) == "First");

    storage.fail_write = DECK_WIFI_STORAGE_ACTIVE_MARKER;
    assert(deck_wifi_config_submit(manager, &replacement, 2) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(!deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));
    deck_wifi_config_destroy(manager);

    storage.fail_write = DECK_WIFI_STORAGE_KEY_COUNT;
    manager = create_manager(&storage, &wifi);
    assert(std::string(snapshot(manager).active_ssid) == "First");
    assert(snapshot(manager).generation == 1);
    deck_wifi_config_destroy(manager);
}

void invalid_input_and_wifi_start_failure_are_reported_without_secrets()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t empty = credentials("", "valid-password");
    assert(deck_wifi_config_submit(manager, &empty, 0) == DECK_WIFI_SUBMIT_INVALID_SSID);
    const deck_wifi_credentials_t short_password = credentials("Office", "short");
    assert(deck_wifi_config_submit(manager, &short_password, 0) ==
           DECK_WIFI_SUBMIT_INVALID_PASSWORD);

    wifi.begin_result = false;
    const deck_wifi_credentials_t valid = credentials("Office", "valid-password");
    assert(deck_wifi_config_submit(manager, &valid, 0) == DECK_WIFI_SUBMIT_WIFI_ERROR);
    const deck_wifi_config_snapshot_t failed = snapshot(manager);
    assert(failed.state == DECK_WIFI_CONFIG_CONNECTION_FAILED);
    assert(std::string(failed.candidate_ssid) == "Office");
    deck_wifi_config_destroy(manager);

    FakeStorage corrupt_storage;
    corrupt_storage.corrupt_candidate_write = true;
    FakeWifi untouched_wifi;
    manager = create_manager(&corrupt_storage, &untouched_wifi);
    assert(deck_wifi_config_submit(manager, &valid, 0) == DECK_WIFI_SUBMIT_STORAGE_ERROR);
    assert(untouched_wifi.begin_count == 0);
    assert(snapshot(manager).state == DECK_WIFI_CONFIG_STORAGE_ERROR);
    deck_wifi_config_destroy(manager);
}

void shared_status_names_and_credential_clearing_have_one_contract()
{
    assert(std::string(deck_wifi_config_state_name(DECK_WIFI_CONFIG_NO_ACTIVE)) ==
           "no_active");
    assert(std::string(deck_wifi_config_state_name(DECK_WIFI_CONFIG_STORAGE_ERROR)) ==
           "storage_error");
    assert(std::string(deck_wifi_record_status_name(DECK_WIFI_RECORD_EMPTY)) == "empty");
    assert(std::string(deck_wifi_record_status_name(DECK_WIFI_RECORD_MIGRATION_FAILED)) ==
           "migration_failed");

    deck_wifi_credentials_t secret = credentials("Office", "correct-horse");
    deck_wifi_credentials_clear(&secret);
    const auto *bytes = reinterpret_cast<const unsigned char *>(&secret);
    for (size_t index = 0; index < sizeof(secret); ++index) {
        assert(bytes[index] == 0);
    }
}

void confirmed_clear_removes_only_wifi_records_and_requires_no_active_validation()
{
    FakeStorage storage;
    FakeWifi wifi;
    deck_wifi_config_t *manager = create_manager(&storage, &wifi);
    const deck_wifi_credentials_t active = credentials("Office", "correct-horse");
    assert(deck_wifi_config_submit(manager, &active, 0) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_validation_result(manager, DECK_WIFI_VALIDATION_CONNECTED));

    const deck_wifi_credentials_t candidate = credentials("Candidate", "candidate-password");
    assert(deck_wifi_config_submit(manager, &candidate, 10) == DECK_WIFI_SUBMIT_ACCEPTED);
    assert(deck_wifi_config_clear(manager) == DECK_WIFI_CLEAR_CLEARED);
    const deck_wifi_config_snapshot_t cleared = snapshot(manager);
    assert(cleared.state == DECK_WIFI_CONFIG_NO_ACTIVE);
    assert(!cleared.has_active);
    assert(!cleared.has_candidate);
    assert(cleared.generation == 0);
    assert(storage.values.empty());
    assert(storage.unrelated_value == 42);
    assert(wifi.cancel_count == 1);

    deck_wifi_config_destroy(manager);
    manager = create_manager(&storage, &wifi);
    assert(snapshot(manager).state == DECK_WIFI_CONFIG_NO_ACTIVE);
    assert(!snapshot(manager).has_active);
    deck_wifi_config_destroy(manager);
}

}  // namespace

int main()
{
    candidate_is_committed_only_after_successful_validation();
    schema_v1_records_from_ticket_6_survive_the_shared_store_upgrade();
    failed_and_timed_out_candidates_preserve_the_last_active_record();
    marker_failure_never_activates_the_new_slot();
    marker_is_verified_before_the_candidate_becomes_active();
    an_uncommitted_slot_is_never_used_as_the_previous_active_record();
    restart_ignores_failed_candidate_and_recovers_committed_active();
    candidate_io_failure_preserves_active_but_keeps_storage_fault_visible();
    corrupt_unknown_and_legacy_records_do_not_replace_a_valid_fallback();
    marker_failure_after_fallback_keeps_the_recovered_record_active();
    invalid_input_and_wifi_start_failure_are_reported_without_secrets();
    shared_status_names_and_credential_clearing_have_one_contract();
    confirmed_clear_removes_only_wifi_records_and_requires_no_active_validation();
    return 0;
}
