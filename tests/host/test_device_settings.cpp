#include "deck_device_settings.h"

#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <map>
#include <vector>

namespace {

struct FakeStorage {
    std::map<deck_device_settings_storage_key_t, std::vector<uint8_t>> values;
    std::vector<deck_device_settings_storage_key_t> writes;
    deck_device_settings_storage_key_t fail_write = DECK_DEVICE_SETTINGS_STORAGE_KEY_COUNT;
    bool corrupt_next_marker_write = false;
};

deck_device_settings_storage_result_t read_storage(
    void *context,
    deck_device_settings_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    const auto value = storage->values.find(key);
    if (value == storage->values.end()) {
        return DECK_DEVICE_SETTINGS_STORAGE_NOT_FOUND;
    }
    if (output == nullptr || size == nullptr || capacity < value->second.size()) {
        return DECK_DEVICE_SETTINGS_STORAGE_ERROR;
    }
    std::memcpy(output, value->second.data(), value->second.size());
    *size = value->second.size();
    return DECK_DEVICE_SETTINGS_STORAGE_OK;
}

bool write_storage(
    void *context,
    deck_device_settings_storage_key_t key,
    const uint8_t *data,
    size_t size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    storage->writes.push_back(key);
    if (key == storage->fail_write || data == nullptr || size == 0) {
        return false;
    }
    storage->values[key] = std::vector<uint8_t>(data, data + size);
    if (key == DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER &&
        storage->corrupt_next_marker_write) {
        storage->values[key].back() ^= 0x40U;
        storage->corrupt_next_marker_write = false;
    }
    return true;
}

bool erase_storage(void *context, deck_device_settings_storage_key_t key)
{
    static_cast<FakeStorage *>(context)->values.erase(key);
    return true;
}

deck_device_settings_t *create_settings(FakeStorage *storage)
{
    const deck_device_settings_options_t options = {
        {read_storage, write_storage, erase_storage, storage},
    };
    return deck_device_settings_create(&options);
}

deck_device_settings_snapshot_t snapshot(deck_device_settings_t *settings)
{
    deck_device_settings_snapshot_t value{};
    assert(deck_device_settings_snapshot(settings, &value));
    return value;
}

void default_and_boundaries_are_exact_tenths()
{
    FakeStorage storage;
    deck_device_settings_t *settings = create_settings(&storage);
    assert(settings != nullptr);
    const deck_device_settings_snapshot_t initial = snapshot(settings);
    assert(initial.state == DECK_DEVICE_SETTINGS_DEFAULT);
    assert(initial.temperature_offset_tenths_c == -40);
    assert(!initial.has_active);

    assert(deck_device_settings_submit_temperature_offset(settings, -151) ==
           DECK_DEVICE_SETTINGS_INVALID_OFFSET);
    assert(deck_device_settings_submit_temperature_offset(settings, 151) ==
           DECK_DEVICE_SETTINGS_INVALID_OFFSET);
    assert(deck_device_settings_submit_temperature_offset(settings, -150) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    assert(snapshot(settings).temperature_offset_tenths_c == -150);
    assert(deck_device_settings_submit_temperature_offset(settings, 150) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    assert(snapshot(settings).temperature_offset_tenths_c == 150);
    deck_device_settings_destroy(settings);
}

void commit_is_transactional_and_survives_restart()
{
    FakeStorage storage;
    deck_device_settings_t *settings = create_settings(&storage);
    assert(settings != nullptr);
    assert(deck_device_settings_submit_temperature_offset(settings, -35) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    const deck_device_settings_snapshot_t active = snapshot(settings);
    assert(active.state == DECK_DEVICE_SETTINGS_ACTIVE);
    assert(active.record_status == DECK_DEVICE_SETTINGS_RECORD_VALID);
    assert(active.has_active);
    assert(!active.has_candidate);
    assert(active.temperature_offset_tenths_c == -35);
    assert(active.generation == 1);
    deck_device_settings_destroy(settings);

    settings = create_settings(&storage);
    assert(settings != nullptr);
    const deck_device_settings_snapshot_t restored = snapshot(settings);
    assert(restored.state == DECK_DEVICE_SETTINGS_ACTIVE);
    assert(restored.has_active);
    assert(restored.temperature_offset_tenths_c == -35);
    assert(restored.generation == 1);
    deck_device_settings_destroy(settings);
}

void marker_failure_preserves_the_old_offset_across_restart()
{
    FakeStorage storage;
    deck_device_settings_t *settings = create_settings(&storage);
    assert(settings != nullptr);
    assert(deck_device_settings_submit_temperature_offset(settings, -40) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    const std::vector<uint8_t> old_marker =
        storage.values[DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER];

    storage.fail_write = DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER;
    assert(deck_device_settings_submit_temperature_offset(settings, 25) ==
           DECK_DEVICE_SETTINGS_STORAGE_FAILURE);
    const deck_device_settings_snapshot_t failed = snapshot(settings);
    assert(failed.state == DECK_DEVICE_SETTINGS_STATE_STORAGE_ERROR);
    assert(failed.temperature_offset_tenths_c == -40);
    assert(failed.generation == 1);
    assert(failed.has_candidate);
    assert(failed.candidate_record_status == DECK_DEVICE_SETTINGS_RECORD_VALID);
    assert(storage.values[DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER] == old_marker);
    deck_device_settings_destroy(settings);

    storage.fail_write = DECK_DEVICE_SETTINGS_STORAGE_KEY_COUNT;
    settings = create_settings(&storage);
    assert(settings != nullptr);
    const deck_device_settings_snapshot_t restored = snapshot(settings);
    assert(restored.temperature_offset_tenths_c == -40);
    assert(restored.generation == 1);
    assert(restored.has_candidate);
    deck_device_settings_destroy(settings);
}

void corrupt_marker_readback_is_rolled_back_to_the_old_offset()
{
    FakeStorage storage;
    deck_device_settings_t *settings = create_settings(&storage);
    assert(settings != nullptr);
    assert(deck_device_settings_submit_temperature_offset(settings, -40) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    const std::vector<uint8_t> old_marker =
        storage.values[DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER];

    storage.corrupt_next_marker_write = true;
    assert(deck_device_settings_submit_temperature_offset(settings, 25) ==
           DECK_DEVICE_SETTINGS_STORAGE_FAILURE);
    assert(snapshot(settings).temperature_offset_tenths_c == -40);
    assert(snapshot(settings).has_candidate);
    assert(storage.values[DECK_DEVICE_SETTINGS_STORAGE_ACTIVE_MARKER] == old_marker);
    deck_device_settings_destroy(settings);

    settings = create_settings(&storage);
    assert(settings != nullptr);
    assert(snapshot(settings).temperature_offset_tenths_c == -40);
    assert(snapshot(settings).generation == 1);
    assert(snapshot(settings).has_candidate);
    deck_device_settings_destroy(settings);
}

void corrupt_selected_record_recovers_the_previous_committed_offset()
{
    FakeStorage storage;
    deck_device_settings_t *settings = create_settings(&storage);
    assert(settings != nullptr);
    assert(deck_device_settings_submit_temperature_offset(settings, -40) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    assert(deck_device_settings_submit_temperature_offset(settings, -35) ==
           DECK_DEVICE_SETTINGS_UPDATED);
    deck_device_settings_destroy(settings);

    storage.values[DECK_DEVICE_SETTINGS_STORAGE_SLOT_1].back() ^= 0x80U;
    settings = create_settings(&storage);
    assert(settings != nullptr);
    const deck_device_settings_snapshot_t recovered = snapshot(settings);
    assert(recovered.state == DECK_DEVICE_SETTINGS_ACTIVE);
    assert(recovered.record_status == DECK_DEVICE_SETTINGS_RECORD_RECOVERED_PREVIOUS);
    assert(recovered.temperature_offset_tenths_c == -40);
    assert(recovered.generation == 1);
    deck_device_settings_destroy(settings);
}

}  // namespace

int main()
{
    default_and_boundaries_are_exact_tenths();
    commit_is_transactional_and_survives_restart();
    marker_failure_preserves_the_old_offset_across_restart();
    corrupt_marker_readback_is_rolled_back_to_the_old_offset();
    corrupt_selected_record_recovers_the_previous_committed_offset();
    return 0;
}
