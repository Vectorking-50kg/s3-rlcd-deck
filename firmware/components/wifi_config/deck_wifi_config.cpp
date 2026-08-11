#include "deck_wifi_config.h"

#include <array>
#include <cstring>
#include <new>

namespace {

constexpr std::array<uint8_t, 4> kRecordMagic = {'D', 'W', 'F', '1'};
constexpr std::array<uint8_t, 4> kMarkerMagic = {'D', 'W', 'M', '1'};
constexpr uint8_t kSchemaVersion = 1;
constexpr size_t kRecordHeaderSize = 14;
constexpr size_t kChecksumSize = 4;
constexpr size_t kRecordCapacity =
    kRecordHeaderSize + DECK_WIFI_SSID_CAPACITY - 1 + DECK_WIFI_PASSWORD_CAPACITY - 1 +
    kChecksumSize;
constexpr size_t kMarkerSize = 20;

enum class DecodeResult : uint8_t {
    valid,
    corrupt,
    unsupported,
    migration_failed,
};

struct DecodedRecord {
    deck_wifi_credentials_t credentials;
    uint32_t generation;
};

struct DecodedMarker {
    uint8_t active_slot;
    uint32_t active_generation;
    bool has_previous;
    uint8_t previous_slot;
    uint32_t previous_generation;
};

uint32_t read_u32(const uint8_t *input)
{
    return static_cast<uint32_t>(input[0]) |
           static_cast<uint32_t>(input[1]) << 8U |
           static_cast<uint32_t>(input[2]) << 16U |
           static_cast<uint32_t>(input[3]) << 24U;
}

void write_u32(uint8_t *output, uint32_t value)
{
    output[0] = static_cast<uint8_t>(value & 0xffU);
    output[1] = static_cast<uint8_t>((value >> 8U) & 0xffU);
    output[2] = static_cast<uint8_t>((value >> 16U) & 0xffU);
    output[3] = static_cast<uint8_t>((value >> 24U) & 0xffU);
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

bool valid_text(const char *value, size_t capacity, size_t minimum, size_t maximum, size_t *size)
{
    if (value == nullptr || size == nullptr) {
        return false;
    }
    const size_t length = strnlen(value, capacity);
    if (length == capacity || length < minimum || length > maximum) {
        return false;
    }
    for (size_t index = 0; index < length; ++index) {
        const unsigned char byte = static_cast<unsigned char>(value[index]);
        if (byte < 0x20U || byte == 0x7fU) {
            return false;
        }
    }
    *size = length;
    return true;
}

bool valid_credentials(
    const deck_wifi_credentials_t *credentials,
    size_t *ssid_size,
    size_t *password_size
)
{
    if (credentials == nullptr ||
        !valid_text(
            credentials->ssid,
            sizeof(credentials->ssid),
            1,
            DECK_WIFI_SSID_CAPACITY - 1,
            ssid_size
        )) {
        return false;
    }
    const size_t length = strnlen(credentials->password, sizeof(credentials->password));
    if (length == sizeof(credentials->password) ||
        (length != 0 && (length < 8 || length > DECK_WIFI_PASSWORD_CAPACITY - 1))) {
        return false;
    }
    for (size_t index = 0; index < length; ++index) {
        const unsigned char byte = static_cast<unsigned char>(credentials->password[index]);
        if (byte < 0x20U || byte == 0x7fU) {
            return false;
        }
    }
    *password_size = length;
    return true;
}

size_t encode_record(
    const deck_wifi_credentials_t &credentials,
    uint32_t generation,
    std::array<uint8_t, kRecordCapacity> *output
)
{
    size_t ssid_size = 0;
    size_t password_size = 0;
    if (output == nullptr ||
        !valid_credentials(&credentials, &ssid_size, &password_size)) {
        return 0;
    }
    output->fill(0);
    std::memcpy(output->data(), kRecordMagic.data(), kRecordMagic.size());
    (*output)[4] = kSchemaVersion;
    const size_t payload_size = ssid_size + password_size;
    (*output)[6] = static_cast<uint8_t>(payload_size & 0xffU);
    (*output)[7] = static_cast<uint8_t>((payload_size >> 8U) & 0xffU);
    write_u32(output->data() + 8, generation);
    (*output)[12] = static_cast<uint8_t>(ssid_size);
    (*output)[13] = static_cast<uint8_t>(password_size);
    std::memcpy(output->data() + kRecordHeaderSize, credentials.ssid, ssid_size);
    std::memcpy(
        output->data() + kRecordHeaderSize + ssid_size,
        credentials.password,
        password_size
    );
    const size_t checksum_offset = kRecordHeaderSize + payload_size;
    write_u32(output->data() + checksum_offset, crc32(output->data(), checksum_offset));
    return checksum_offset + kChecksumSize;
}

DecodeResult decode_record(const uint8_t *input, size_t size, DecodedRecord *record)
{
    if (input == nullptr || record == nullptr || size < kRecordHeaderSize + kChecksumSize ||
        std::memcmp(input, kRecordMagic.data(), kRecordMagic.size()) != 0) {
        return DecodeResult::corrupt;
    }
    if (input[4] > kSchemaVersion) {
        return DecodeResult::unsupported;
    }
    if (input[4] < kSchemaVersion) {
        return DecodeResult::migration_failed;
    }
    const size_t payload_size = static_cast<size_t>(input[6]) |
                                static_cast<size_t>(input[7]) << 8U;
    const size_t ssid_size = input[12];
    const size_t password_size = input[13];
    const size_t checksum_offset = kRecordHeaderSize + payload_size;
    if (payload_size != ssid_size + password_size ||
        ssid_size == 0 || ssid_size >= DECK_WIFI_SSID_CAPACITY ||
        password_size >= DECK_WIFI_PASSWORD_CAPACITY ||
        (password_size != 0 && password_size < 8) ||
        checksum_offset + kChecksumSize != size ||
        read_u32(input + checksum_offset) != crc32(input, checksum_offset)) {
        return DecodeResult::corrupt;
    }
    *record = {};
    std::memcpy(record->credentials.ssid, input + kRecordHeaderSize, ssid_size);
    std::memcpy(
        record->credentials.password,
        input + kRecordHeaderSize + ssid_size,
        password_size
    );
    size_t validated_ssid_size = 0;
    size_t validated_password_size = 0;
    if (!valid_credentials(
            &record->credentials,
            &validated_ssid_size,
            &validated_password_size
        ) ||
        validated_ssid_size != ssid_size || validated_password_size != password_size) {
        return DecodeResult::corrupt;
    }
    record->generation = read_u32(input + 8);
    return record->generation == 0 ? DecodeResult::corrupt : DecodeResult::valid;
}

deck_wifi_record_status_t record_status(DecodeResult result)
{
    switch (result) {
        case DecodeResult::unsupported:
            return DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA;
        case DecodeResult::migration_failed:
            return DECK_WIFI_RECORD_MIGRATION_FAILED;
        case DecodeResult::corrupt:
        default:
            return DECK_WIFI_RECORD_CORRUPT;
    }
}

void encode_marker(
    uint8_t active_slot,
    uint32_t active_generation,
    bool has_previous,
    uint8_t previous_slot,
    uint32_t previous_generation,
    std::array<uint8_t, kMarkerSize> *output
)
{
    output->fill(0);
    std::memcpy(output->data(), kMarkerMagic.data(), kMarkerMagic.size());
    (*output)[4] = kSchemaVersion;
    (*output)[5] = active_slot;
    write_u32(output->data() + 6, active_generation);
    (*output)[10] = has_previous ? 1U : 0U;
    (*output)[11] = has_previous ? previous_slot : 0U;
    write_u32(output->data() + 12, has_previous ? previous_generation : 0U);
    write_u32(output->data() + 16, crc32(output->data(), 16));
}

DecodeResult decode_marker(const uint8_t *input, size_t size, DecodedMarker *marker)
{
    if (input == nullptr || marker == nullptr || size != kMarkerSize ||
        std::memcmp(input, kMarkerMagic.data(), kMarkerMagic.size()) != 0) {
        return DecodeResult::corrupt;
    }
    if (input[4] > kSchemaVersion) {
        return DecodeResult::unsupported;
    }
    if (input[4] < kSchemaVersion) {
        return DecodeResult::migration_failed;
    }
    const uint8_t active_slot = input[5];
    const uint32_t active_generation = read_u32(input + 6);
    const uint8_t has_previous = input[10];
    const uint8_t previous_slot = input[11];
    const uint32_t previous_generation = read_u32(input + 12);
    if (active_slot > 1 || active_generation == 0 || has_previous > 1 ||
        (has_previous == 0 && (previous_slot != 0 || previous_generation != 0)) ||
        (has_previous == 1 &&
         (previous_slot > 1 || previous_slot == active_slot || previous_generation == 0)) ||
        read_u32(input + 16) != crc32(input, 16)) {
        return DecodeResult::corrupt;
    }
    *marker = {
        active_slot,
        active_generation,
        has_previous == 1,
        previous_slot,
        previous_generation,
    };
    return DecodeResult::valid;
}

deck_wifi_storage_key_t slot_key(uint8_t slot)
{
    return slot == 0 ? DECK_WIFI_STORAGE_SLOT_0 : DECK_WIFI_STORAGE_SLOT_1;
}

}  // namespace

const char *deck_wifi_config_state_name(deck_wifi_config_state_t state)
{
    switch (state) {
        case DECK_WIFI_CONFIG_ACTIVE:
            return "active";
        case DECK_WIFI_CONFIG_VALIDATING:
            return "validating";
        case DECK_WIFI_CONFIG_AUTH_FAILED:
            return "auth_failed";
        case DECK_WIFI_CONFIG_TIMED_OUT:
            return "timed_out";
        case DECK_WIFI_CONFIG_CONNECTION_FAILED:
            return "connection_failed";
        case DECK_WIFI_CONFIG_STORAGE_ERROR:
            return "storage_error";
        case DECK_WIFI_CONFIG_NO_ACTIVE:
        default:
            return "no_active";
    }
}

const char *deck_wifi_record_status_name(deck_wifi_record_status_t status)
{
    switch (status) {
        case DECK_WIFI_RECORD_VALID:
            return "valid";
        case DECK_WIFI_RECORD_RECOVERED_PREVIOUS:
            return "recovered_previous";
        case DECK_WIFI_RECORD_CORRUPT:
            return "corrupt";
        case DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA:
            return "unsupported_schema";
        case DECK_WIFI_RECORD_MIGRATION_FAILED:
            return "migration_failed";
        case DECK_WIFI_RECORD_IO_ERROR:
            return "io_error";
        case DECK_WIFI_RECORD_EMPTY:
        default:
            return "empty";
    }
}

void deck_wifi_credentials_clear(deck_wifi_credentials_t *credentials)
{
    if (credentials == nullptr) {
        return;
    }
    volatile char *bytes = reinterpret_cast<volatile char *>(credentials);
    for (size_t index = 0; index < sizeof(*credentials); ++index) {
        bytes[index] = 0;
    }
}

struct deck_wifi_config {
    deck_wifi_config_options_t options;
    deck_wifi_config_state_t state = DECK_WIFI_CONFIG_NO_ACTIVE;
    deck_wifi_record_status_t record_status = DECK_WIFI_RECORD_EMPTY;
    deck_wifi_record_status_t candidate_record_status = DECK_WIFI_RECORD_EMPTY;
    deck_wifi_credentials_t active{};
    deck_wifi_credentials_t candidate{};
    uint32_t generation = 0;
    uint32_t candidate_generation = 0;
    uint64_t validation_started_ms = 0;
    uint8_t active_slot = 0;
    bool has_active = false;
    bool has_candidate = false;
    bool storage_faulted = false;
};

namespace {

void set_storage_failure(deck_wifi_config_t *config)
{
    config->storage_faulted = true;
    config->state = DECK_WIFI_CONFIG_STORAGE_ERROR;
}

deck_wifi_storage_result_t read_blob(
    deck_wifi_config_t *config,
    deck_wifi_storage_key_t key,
    std::array<uint8_t, kRecordCapacity> *buffer,
    size_t *size
)
{
    return config->options.storage.read(
        config->options.storage.context,
        key,
        buffer->data(),
        buffer->size(),
        size
    );
}

deck_wifi_storage_result_t read_record(
    deck_wifi_config_t *config,
    deck_wifi_storage_key_t key,
    DecodedRecord *record,
    DecodeResult *decode_result
)
{
    std::array<uint8_t, kRecordCapacity> bytes{};
    size_t size = 0;
    const deck_wifi_storage_result_t result = read_blob(config, key, &bytes, &size);
    if (result == DECK_WIFI_STORAGE_OK) {
        *decode_result = decode_record(bytes.data(), size, record);
    }
    return result;
}

void load_candidate(deck_wifi_config_t *config)
{
    DecodedRecord candidate{};
    DecodeResult decoded = DecodeResult::corrupt;
    const deck_wifi_storage_result_t result = read_record(
        config,
        DECK_WIFI_STORAGE_CANDIDATE,
        &candidate,
        &decoded
    );
    if (result == DECK_WIFI_STORAGE_NOT_FOUND) {
        return;
    }
    if (result == DECK_WIFI_STORAGE_ERROR) {
        config->candidate_record_status = DECK_WIFI_RECORD_IO_ERROR;
        set_storage_failure(config);
        return;
    }
    if (decoded != DecodeResult::valid) {
        config->candidate_record_status = record_status(decoded);
        return;
    }
    config->candidate = candidate.credentials;
    config->candidate_generation = candidate.generation;
    config->has_candidate = true;
    config->candidate_record_status = DECK_WIFI_RECORD_VALID;
}

void load_active(deck_wifi_config_t *config)
{
    std::array<uint8_t, kRecordCapacity> marker{};
    size_t marker_size = 0;
    const deck_wifi_storage_result_t marker_read = read_blob(
        config,
        DECK_WIFI_STORAGE_ACTIVE_MARKER,
        &marker,
        &marker_size
    );
    if (marker_read == DECK_WIFI_STORAGE_NOT_FOUND) {
        return;
    }
    if (marker_read == DECK_WIFI_STORAGE_ERROR) {
        config->record_status = DECK_WIFI_RECORD_IO_ERROR;
        set_storage_failure(config);
        return;
    }
    DecodedMarker decoded_marker{};
    const DecodeResult marker_result = decode_marker(
        marker.data(),
        marker_size,
        &decoded_marker
    );
    if (marker_result != DecodeResult::valid) {
        config->record_status = record_status(marker_result);
        return;
    }

    DecodedRecord selected{};
    DecodeResult selected_result = DecodeResult::corrupt;
    const deck_wifi_storage_result_t selected_read = read_record(
        config,
        slot_key(decoded_marker.active_slot),
        &selected,
        &selected_result
    );
    if (selected_read == DECK_WIFI_STORAGE_OK && selected_result == DecodeResult::valid &&
        selected.generation == decoded_marker.active_generation) {
        config->active = selected.credentials;
        config->generation = selected.generation;
        config->active_slot = decoded_marker.active_slot;
        config->has_active = true;
        config->state = DECK_WIFI_CONFIG_ACTIVE;
        config->record_status = DECK_WIFI_RECORD_VALID;
        return;
    }

    DecodedRecord fallback{};
    DecodeResult fallback_result = DecodeResult::corrupt;
    deck_wifi_storage_result_t fallback_read = DECK_WIFI_STORAGE_NOT_FOUND;
    if (decoded_marker.has_previous) {
        fallback_read = read_record(
            config,
            slot_key(decoded_marker.previous_slot),
            &fallback,
            &fallback_result
        );
    }
    if (decoded_marker.has_previous && fallback_read == DECK_WIFI_STORAGE_OK &&
        fallback_result == DecodeResult::valid &&
        fallback.generation == decoded_marker.previous_generation) {
        config->active = fallback.credentials;
        config->generation = fallback.generation;
        config->active_slot = decoded_marker.previous_slot;
        config->has_active = true;
        config->state = DECK_WIFI_CONFIG_ACTIVE;
        config->record_status = DECK_WIFI_RECORD_RECOVERED_PREVIOUS;
        return;
    }
    if (selected_read == DECK_WIFI_STORAGE_ERROR || fallback_read == DECK_WIFI_STORAGE_ERROR) {
        config->record_status = DECK_WIFI_RECORD_IO_ERROR;
        set_storage_failure(config);
    } else if (selected_read == DECK_WIFI_STORAGE_OK) {
        config->record_status = selected_result == DecodeResult::valid
                                    ? DECK_WIFI_RECORD_CORRUPT
                                    : record_status(selected_result);
    } else {
        config->record_status = DECK_WIFI_RECORD_CORRUPT;
    }
}

bool commit_candidate(deck_wifi_config_t *config)
{
    const uint8_t next_slot = config->has_active && config->active_slot == 0 ? 1 : 0;
    std::array<uint8_t, kRecordCapacity> record{};
    const size_t record_size = encode_record(
        config->candidate,
        config->candidate_generation,
        &record
    );
    if (record_size == 0 ||
        !config->options.storage.write(
            config->options.storage.context,
            slot_key(next_slot),
            record.data(),
            record_size
        )) {
        set_storage_failure(config);
        return false;
    }

    DecodedRecord verified{};
    DecodeResult verified_result = DecodeResult::corrupt;
    if (read_record(
            config,
            slot_key(next_slot),
            &verified,
            &verified_result
        ) != DECK_WIFI_STORAGE_OK ||
        verified_result != DecodeResult::valid ||
        verified.generation != config->candidate_generation ||
        std::memcmp(&verified.credentials, &config->candidate, sizeof(config->candidate)) != 0) {
        set_storage_failure(config);
        return false;
    }

    std::array<uint8_t, kMarkerSize> marker{};
    encode_marker(
        next_slot,
        verified.generation,
        config->has_active,
        config->active_slot,
        config->generation,
        &marker
    );
    if (!config->options.storage.write(
            config->options.storage.context,
            DECK_WIFI_STORAGE_ACTIVE_MARKER,
            marker.data(),
            marker.size()
        )) {
        set_storage_failure(config);
        return false;
    }
    std::array<uint8_t, kRecordCapacity> verified_marker{};
    size_t verified_marker_size = 0;
    if (read_blob(
            config,
            DECK_WIFI_STORAGE_ACTIVE_MARKER,
            &verified_marker,
            &verified_marker_size
        ) != DECK_WIFI_STORAGE_OK ||
        verified_marker_size != marker.size() ||
        std::memcmp(verified_marker.data(), marker.data(), marker.size()) != 0) {
        set_storage_failure(config);
        return false;
    }

    config->active = config->candidate;
    config->generation = verified.generation;
    config->active_slot = next_slot;
    config->has_active = true;
    config->record_status = DECK_WIFI_RECORD_VALID;
    config->state = DECK_WIFI_CONFIG_ACTIVE;
    if (config->options.storage.erase(
            config->options.storage.context,
            DECK_WIFI_STORAGE_CANDIDATE
        )) {
        deck_wifi_credentials_clear(&config->candidate);
        config->candidate_generation = 0;
        config->has_candidate = false;
        config->candidate_record_status = DECK_WIFI_RECORD_EMPTY;
    } else {
        config->candidate_record_status = DECK_WIFI_RECORD_IO_ERROR;
    }
    return true;
}

}  // namespace

deck_wifi_config_t *deck_wifi_config_create(const deck_wifi_config_options_t *options)
{
    if (options == nullptr || options->storage.read == nullptr ||
        options->storage.write == nullptr || options->storage.erase == nullptr ||
        options->validation.begin == nullptr || options->validation.cancel == nullptr ||
        options->validation_timeout_ms == 0) {
        return nullptr;
    }
    auto *config = new (std::nothrow) deck_wifi_config_t{};
    if (config == nullptr) {
        return nullptr;
    }
    config->options = *options;
    load_candidate(config);
    load_active(config);
    if (config->storage_faulted) {
        config->state = DECK_WIFI_CONFIG_STORAGE_ERROR;
    }
    return config;
}

void deck_wifi_config_destroy(deck_wifi_config_t *config)
{
    if (config != nullptr) {
        deck_wifi_credentials_clear(&config->active);
        deck_wifi_credentials_clear(&config->candidate);
    }
    delete config;
}

deck_wifi_submit_result_t deck_wifi_config_submit(
    deck_wifi_config_t *config,
    const deck_wifi_credentials_t *credentials,
    uint64_t now_ms
)
{
    if (config == nullptr || credentials == nullptr) {
        return DECK_WIFI_SUBMIT_INVALID_SSID;
    }
    if (config->storage_faulted) {
        return DECK_WIFI_SUBMIT_STORAGE_ERROR;
    }
    if (config->state == DECK_WIFI_CONFIG_VALIDATING) {
        return DECK_WIFI_SUBMIT_BUSY;
    }
    size_t ssid_size = 0;
    size_t password_size = 0;
    if (!valid_text(
            credentials->ssid,
            sizeof(credentials->ssid),
            1,
            DECK_WIFI_SSID_CAPACITY - 1,
            &ssid_size
        )) {
        return DECK_WIFI_SUBMIT_INVALID_SSID;
    }
    if (!valid_credentials(credentials, &ssid_size, &password_size)) {
        return DECK_WIFI_SUBMIT_INVALID_PASSWORD;
    }

    uint32_t candidate_generation = config->generation + 1;
    if (candidate_generation == 0) {
        candidate_generation = 1;
    }
    std::array<uint8_t, kRecordCapacity> record{};
    const size_t record_size = encode_record(*credentials, candidate_generation, &record);
    if (record_size == 0 ||
        !config->options.storage.write(
            config->options.storage.context,
            DECK_WIFI_STORAGE_CANDIDATE,
            record.data(),
            record_size
        )) {
        set_storage_failure(config);
        return DECK_WIFI_SUBMIT_STORAGE_ERROR;
    }
    DecodedRecord verified{};
    DecodeResult verified_result = DecodeResult::corrupt;
    if (read_record(
            config,
            DECK_WIFI_STORAGE_CANDIDATE,
            &verified,
            &verified_result
        ) != DECK_WIFI_STORAGE_OK ||
        verified_result != DecodeResult::valid ||
        verified.generation != candidate_generation ||
        std::memcmp(&verified.credentials, credentials, sizeof(*credentials)) != 0) {
        set_storage_failure(config);
        return DECK_WIFI_SUBMIT_STORAGE_ERROR;
    }
    config->candidate = *credentials;
    config->candidate_generation = candidate_generation;
    config->has_candidate = true;
    config->candidate_record_status = DECK_WIFI_RECORD_VALID;
    if (!config->options.validation.begin(config->options.validation.context, credentials)) {
        config->state = DECK_WIFI_CONFIG_CONNECTION_FAILED;
        return DECK_WIFI_SUBMIT_WIFI_ERROR;
    }
    config->validation_started_ms = now_ms;
    config->state = DECK_WIFI_CONFIG_VALIDATING;
    return DECK_WIFI_SUBMIT_ACCEPTED;
}

bool deck_wifi_config_validation_result(
    deck_wifi_config_t *config,
    deck_wifi_validation_result_t result
)
{
    if (config == nullptr || config->state != DECK_WIFI_CONFIG_VALIDATING) {
        return false;
    }
    switch (result) {
        case DECK_WIFI_VALIDATION_CONNECTED:
            return commit_candidate(config);
        case DECK_WIFI_VALIDATION_AUTH_FAILED:
            config->state = DECK_WIFI_CONFIG_AUTH_FAILED;
            return true;
        case DECK_WIFI_VALIDATION_CONNECTION_FAILED:
        default:
            config->state = DECK_WIFI_CONFIG_CONNECTION_FAILED;
            return true;
    }
}

bool deck_wifi_config_tick(deck_wifi_config_t *config, uint64_t now_ms)
{
    if (config == nullptr || config->state != DECK_WIFI_CONFIG_VALIDATING ||
        now_ms - config->validation_started_ms < config->options.validation_timeout_ms) {
        return false;
    }
    config->options.validation.cancel(config->options.validation.context);
    config->state = DECK_WIFI_CONFIG_TIMED_OUT;
    return true;
}

bool deck_wifi_config_active_connection(deck_wifi_config_t *config, bool connected)
{
    if (config == nullptr || !config->has_active ||
        config->state == DECK_WIFI_CONFIG_VALIDATING || config->storage_faulted) {
        return false;
    }
    config->state = connected ? DECK_WIFI_CONFIG_ACTIVE
                              : DECK_WIFI_CONFIG_CONNECTION_FAILED;
    return true;
}

bool deck_wifi_config_snapshot(
    const deck_wifi_config_t *config,
    deck_wifi_config_snapshot_t *snapshot
)
{
    if (config == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = {};
    snapshot->state = config->state;
    snapshot->record_status = config->record_status;
    snapshot->candidate_record_status = config->candidate_record_status;
    snapshot->has_active = config->has_active;
    snapshot->has_candidate = config->has_candidate;
    snapshot->generation = config->generation;
    std::memcpy(snapshot->active_ssid, config->active.ssid, sizeof(snapshot->active_ssid));
    std::memcpy(
        snapshot->candidate_ssid,
        config->candidate.ssid,
        sizeof(snapshot->candidate_ssid)
    );
    return true;
}

bool deck_wifi_config_recovery_required(const deck_wifi_config_snapshot_t *snapshot)
{
    return snapshot == nullptr || !snapshot->has_active ||
           snapshot->state != DECK_WIFI_CONFIG_ACTIVE ||
           snapshot->record_status != DECK_WIFI_RECORD_VALID ||
           snapshot->has_candidate ||
           snapshot->candidate_record_status != DECK_WIFI_RECORD_EMPTY;
}

bool deck_wifi_config_active_credentials(
    const deck_wifi_config_t *config,
    deck_wifi_credentials_t *credentials
)
{
    if (config == nullptr || credentials == nullptr || !config->has_active) {
        return false;
    }
    *credentials = config->active;
    return true;
}
