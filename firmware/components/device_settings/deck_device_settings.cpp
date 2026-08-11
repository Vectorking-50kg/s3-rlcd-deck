#include "deck_device_settings.h"

#include <cstring>
#include <new>

namespace {

constexpr uint8_t kSchemaVersion = 1;
constexpr uint8_t kRecordMagic[4] = {'D', 'S', 'T', '1'};
constexpr uint8_t kMarkerMagic[4] = {'D', 'S', 'M', '1'};
constexpr size_t kPayloadSize = 2;

int16_t decode_offset(const uint8_t *payload)
{
    const uint16_t value = static_cast<uint16_t>(payload[0]) |
                           static_cast<uint16_t>(
                               static_cast<uint16_t>(payload[1]) << 8U
                           );
    return static_cast<int16_t>(value);
}

void encode_offset(int16_t offset, uint8_t *payload)
{
    const uint16_t value = static_cast<uint16_t>(offset);
    payload[0] = static_cast<uint8_t>(value & 0xffU);
    payload[1] = static_cast<uint8_t>((value >> 8U) & 0xffU);
}

bool valid_offset(int16_t offset)
{
    return offset >= DECK_DEVICE_SETTINGS_MINIMUM_TEMPERATURE_OFFSET_TENTHS_C &&
           offset <= DECK_DEVICE_SETTINGS_MAXIMUM_TEMPERATURE_OFFSET_TENTHS_C;
}

bool validate_payload(void *, const uint8_t *payload, size_t size)
{
    return payload != nullptr && size == kPayloadSize && valid_offset(decode_offset(payload));
}

deck_device_settings_record_status_t map_status(
    deck_transaction_record_status_t status
)
{
    switch (status) {
        case DECK_TRANSACTION_RECORD_VALID:
            return DECK_DEVICE_SETTINGS_RECORD_VALID;
        case DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS:
            return DECK_DEVICE_SETTINGS_RECORD_RECOVERED_PREVIOUS;
        case DECK_TRANSACTION_RECORD_CORRUPT:
            return DECK_DEVICE_SETTINGS_RECORD_CORRUPT;
        case DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA:
            return DECK_DEVICE_SETTINGS_RECORD_UNSUPPORTED_SCHEMA;
        case DECK_TRANSACTION_RECORD_MIGRATION_FAILED:
            return DECK_DEVICE_SETTINGS_RECORD_MIGRATION_FAILED;
        case DECK_TRANSACTION_RECORD_IO_ERROR:
            return DECK_DEVICE_SETTINGS_RECORD_IO_ERROR;
        case DECK_TRANSACTION_RECORD_EMPTY:
        default:
            return DECK_DEVICE_SETTINGS_RECORD_EMPTY;
    }
}

}  // namespace

struct deck_device_settings {
    deck_transaction_store_t *store;
};

const char *deck_device_settings_state_name(deck_device_settings_state_t state)
{
    switch (state) {
        case DECK_DEVICE_SETTINGS_ACTIVE:
            return "active";
        case DECK_DEVICE_SETTINGS_STATE_STORAGE_ERROR:
            return "storage_error";
        case DECK_DEVICE_SETTINGS_DEFAULT:
        default:
            return "default";
    }
}

const char *deck_device_settings_record_status_name(
    deck_device_settings_record_status_t status
)
{
    switch (status) {
        case DECK_DEVICE_SETTINGS_RECORD_VALID:
            return "valid";
        case DECK_DEVICE_SETTINGS_RECORD_RECOVERED_PREVIOUS:
            return "recovered_previous";
        case DECK_DEVICE_SETTINGS_RECORD_CORRUPT:
            return "corrupt";
        case DECK_DEVICE_SETTINGS_RECORD_UNSUPPORTED_SCHEMA:
            return "unsupported_schema";
        case DECK_DEVICE_SETTINGS_RECORD_MIGRATION_FAILED:
            return "migration_failed";
        case DECK_DEVICE_SETTINGS_RECORD_IO_ERROR:
            return "io_error";
        case DECK_DEVICE_SETTINGS_RECORD_EMPTY:
        default:
            return "empty";
    }
}

deck_device_settings_t *deck_device_settings_create(
    const deck_device_settings_options_t *options
)
{
    if (options == nullptr) {
        return nullptr;
    }
    deck_transaction_store_options_t store_options{};
    store_options.storage = options->storage;
    std::memcpy(store_options.record_magic, kRecordMagic, sizeof(kRecordMagic));
    std::memcpy(store_options.marker_magic, kMarkerMagic, sizeof(kMarkerMagic));
    store_options.schema_version = kSchemaVersion;
    store_options.validate_payload = validate_payload;
    auto *settings = new (std::nothrow) deck_device_settings_t{};
    if (settings == nullptr) {
        return nullptr;
    }
    settings->store = deck_transaction_store_create(&store_options);
    if (settings->store == nullptr) {
        delete settings;
        return nullptr;
    }
    return settings;
}

void deck_device_settings_destroy(deck_device_settings_t *settings)
{
    if (settings != nullptr) {
        deck_transaction_store_destroy(settings->store);
    }
    delete settings;
}

deck_device_settings_update_result_t deck_device_settings_submit_temperature_offset(
    deck_device_settings_t *settings,
    int16_t offset
)
{
    if (settings == nullptr || !valid_offset(offset)) {
        return DECK_DEVICE_SETTINGS_INVALID_OFFSET;
    }
    uint8_t payload[kPayloadSize]{};
    encode_offset(offset, payload);
    if (deck_transaction_store_stage(settings->store, payload, sizeof(payload)) !=
        DECK_TRANSACTION_UPDATED) {
        return DECK_DEVICE_SETTINGS_STORAGE_FAILURE;
    }
    return deck_transaction_store_commit(settings->store) == DECK_TRANSACTION_UPDATED
               ? DECK_DEVICE_SETTINGS_UPDATED
               : DECK_DEVICE_SETTINGS_STORAGE_FAILURE;
}

bool deck_device_settings_snapshot(
    const deck_device_settings_t *settings,
    deck_device_settings_snapshot_t *snapshot
)
{
    if (settings == nullptr || snapshot == nullptr) {
        return false;
    }
    deck_transaction_store_snapshot_t stored{};
    if (!deck_transaction_store_snapshot(settings->store, &stored)) {
        return false;
    }
    const deck_device_settings_state_t state =
        stored.storage_faulted
            ? DECK_DEVICE_SETTINGS_STATE_STORAGE_ERROR
            : (stored.has_active ? DECK_DEVICE_SETTINGS_ACTIVE
                                 : DECK_DEVICE_SETTINGS_DEFAULT);
    *snapshot = {
        state,
        map_status(stored.record_status),
        map_status(stored.candidate_record_status),
        stored.has_active,
        stored.has_candidate,
        stored.has_active ? stored.active.generation : 0,
        stored.has_active
            ? decode_offset(stored.active.payload)
            : static_cast<int16_t>(
                  DECK_DEVICE_SETTINGS_DEFAULT_TEMPERATURE_OFFSET_TENTHS_C
              ),
    };
    return true;
}
