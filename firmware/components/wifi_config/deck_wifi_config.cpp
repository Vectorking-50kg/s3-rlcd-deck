#include "deck_wifi_config.h"

#include <array>
#include <cstring>
#include <new>

namespace {

constexpr uint8_t kSchemaVersion = 1;
constexpr uint8_t kRecordMagic[4] = {'D', 'W', 'F', '1'};
constexpr uint8_t kMarkerMagic[4] = {'D', 'W', 'M', '1'};
constexpr size_t kPayloadHeaderSize = 2;
constexpr size_t kPayloadCapacity =
    kPayloadHeaderSize + DECK_WIFI_SSID_CAPACITY - 1 + DECK_WIFI_PASSWORD_CAPACITY - 1;

bool valid_text(
    const char *value,
    size_t capacity,
    size_t minimum,
    size_t maximum,
    size_t *size
)
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

size_t encode_credentials(
    const deck_wifi_credentials_t &credentials,
    std::array<uint8_t, kPayloadCapacity> *payload
)
{
    size_t ssid_size = 0;
    size_t password_size = 0;
    if (payload == nullptr ||
        !valid_credentials(&credentials, &ssid_size, &password_size)) {
        return 0;
    }
    payload->fill(0);
    (*payload)[0] = static_cast<uint8_t>(ssid_size);
    (*payload)[1] = static_cast<uint8_t>(password_size);
    std::memcpy(payload->data() + kPayloadHeaderSize, credentials.ssid, ssid_size);
    std::memcpy(
        payload->data() + kPayloadHeaderSize + ssid_size,
        credentials.password,
        password_size
    );
    return kPayloadHeaderSize + ssid_size + password_size;
}

bool decode_credentials(
    const uint8_t *payload,
    size_t size,
    deck_wifi_credentials_t *credentials
)
{
    if (payload == nullptr || credentials == nullptr || size < kPayloadHeaderSize) {
        return false;
    }
    const size_t ssid_size = payload[0];
    const size_t password_size = payload[1];
    if (ssid_size == 0 || ssid_size >= DECK_WIFI_SSID_CAPACITY ||
        password_size >= DECK_WIFI_PASSWORD_CAPACITY ||
        (password_size != 0 && password_size < 8) ||
        kPayloadHeaderSize + ssid_size + password_size != size) {
        return false;
    }
    *credentials = {};
    std::memcpy(credentials->ssid, payload + kPayloadHeaderSize, ssid_size);
    std::memcpy(
        credentials->password,
        payload + kPayloadHeaderSize + ssid_size,
        password_size
    );
    size_t validated_ssid_size = 0;
    size_t validated_password_size = 0;
    return valid_credentials(
               credentials,
               &validated_ssid_size,
               &validated_password_size
           ) &&
           validated_ssid_size == ssid_size && validated_password_size == password_size;
}

bool validate_payload(void *, const uint8_t *payload, size_t size)
{
    deck_wifi_credentials_t credentials{};
    const bool valid = decode_credentials(payload, size, &credentials);
    deck_wifi_credentials_clear(&credentials);
    return valid;
}

deck_wifi_record_status_t map_status(deck_transaction_record_status_t status)
{
    switch (status) {
        case DECK_TRANSACTION_RECORD_VALID:
            return DECK_WIFI_RECORD_VALID;
        case DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS:
            return DECK_WIFI_RECORD_RECOVERED_PREVIOUS;
        case DECK_TRANSACTION_RECORD_CORRUPT:
            return DECK_WIFI_RECORD_CORRUPT;
        case DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA:
            return DECK_WIFI_RECORD_UNSUPPORTED_SCHEMA;
        case DECK_TRANSACTION_RECORD_MIGRATION_FAILED:
            return DECK_WIFI_RECORD_MIGRATION_FAILED;
        case DECK_TRANSACTION_RECORD_IO_ERROR:
            return DECK_WIFI_RECORD_IO_ERROR;
        case DECK_TRANSACTION_RECORD_EMPTY:
        default:
            return DECK_WIFI_RECORD_EMPTY;
    }
}

void secure_clear(void *data, size_t size)
{
    volatile uint8_t *bytes = static_cast<volatile uint8_t *>(data);
    for (size_t index = 0; index < size; ++index) {
        bytes[index] = 0;
    }
}

}  // namespace

struct deck_wifi_config {
    deck_wifi_config_options_t options;
    deck_transaction_store_t *store = nullptr;
    deck_wifi_config_state_t state = DECK_WIFI_CONFIG_NO_ACTIVE;
    deck_wifi_record_status_t record_status = DECK_WIFI_RECORD_EMPTY;
    deck_wifi_record_status_t candidate_record_status = DECK_WIFI_RECORD_EMPTY;
    deck_wifi_credentials_t active{};
    deck_wifi_credentials_t candidate{};
    uint32_t generation = 0;
    uint64_t validation_started_ms = 0;
    bool has_active = false;
    bool has_candidate = false;
    bool storage_faulted = false;
};

namespace {

void refresh_store_state(deck_wifi_config_t *config)
{
    deck_transaction_store_snapshot_t stored{};
    if (!deck_transaction_store_snapshot(config->store, &stored)) {
        config->storage_faulted = true;
        config->state = DECK_WIFI_CONFIG_STORAGE_ERROR;
        return;
    }
    deck_wifi_credentials_clear(&config->active);
    deck_wifi_credentials_clear(&config->candidate);
    config->record_status = map_status(stored.record_status);
    config->candidate_record_status = map_status(stored.candidate_record_status);
    config->has_active = stored.has_active;
    config->has_candidate = stored.has_candidate;
    config->generation = stored.has_active ? stored.active.generation : 0;
    config->storage_faulted = stored.storage_faulted;
    if (stored.has_active &&
        !decode_credentials(
            stored.active.payload,
            stored.active.payload_size,
            &config->active
        )) {
        config->has_active = false;
        config->record_status = DECK_WIFI_RECORD_CORRUPT;
    }
    if (stored.has_candidate &&
        !decode_credentials(
            stored.candidate.payload,
            stored.candidate.payload_size,
            &config->candidate
        )) {
        config->has_candidate = false;
        config->candidate_record_status = DECK_WIFI_RECORD_CORRUPT;
    }
    secure_clear(&stored, sizeof(stored));
}

void set_storage_failure(deck_wifi_config_t *config)
{
    refresh_store_state(config);
    config->storage_faulted = true;
    config->state = DECK_WIFI_CONFIG_STORAGE_ERROR;
}

bool commit_candidate(deck_wifi_config_t *config)
{
    if (deck_transaction_store_commit(config->store) != DECK_TRANSACTION_UPDATED) {
        set_storage_failure(config);
        return false;
    }
    refresh_store_state(config);
    config->state = DECK_WIFI_CONFIG_ACTIVE;
    return true;
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
    if (credentials != nullptr) {
        secure_clear(credentials, sizeof(*credentials));
    }
}

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
    deck_transaction_store_options_t store_options{};
    store_options.storage = options->storage;
    std::memcpy(store_options.record_magic, kRecordMagic, sizeof(kRecordMagic));
    std::memcpy(store_options.marker_magic, kMarkerMagic, sizeof(kMarkerMagic));
    store_options.schema_version = kSchemaVersion;
    store_options.payload_length_excluded_prefix = kPayloadHeaderSize;
    store_options.validate_payload = validate_payload;
    config->store = deck_transaction_store_create(&store_options);
    if (config->store == nullptr) {
        delete config;
        return nullptr;
    }
    refresh_store_state(config);
    if (config->storage_faulted) {
        config->state = DECK_WIFI_CONFIG_STORAGE_ERROR;
    } else if (config->has_active) {
        config->state = DECK_WIFI_CONFIG_ACTIVE;
    }
    return config;
}

void deck_wifi_config_destroy(deck_wifi_config_t *config)
{
    if (config != nullptr) {
        deck_wifi_credentials_clear(&config->active);
        deck_wifi_credentials_clear(&config->candidate);
        deck_transaction_store_destroy(config->store);
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

    std::array<uint8_t, kPayloadCapacity> payload{};
    const size_t payload_size = encode_credentials(*credentials, &payload);
    const deck_transaction_update_result_t staged = deck_transaction_store_stage(
        config->store,
        payload.data(),
        payload_size
    );
    secure_clear(payload.data(), payload.size());
    if (staged != DECK_TRANSACTION_UPDATED) {
        set_storage_failure(config);
        return DECK_WIFI_SUBMIT_STORAGE_ERROR;
    }
    refresh_store_state(config);
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

deck_wifi_clear_result_t deck_wifi_config_clear(deck_wifi_config_t *config)
{
    if (config == nullptr) {
        return DECK_WIFI_CLEAR_STORAGE_ERROR;
    }
    if (config->state == DECK_WIFI_CONFIG_VALIDATING) {
        config->options.validation.cancel(config->options.validation.context);
    }
    if (!deck_transaction_store_clear(config->store)) {
        set_storage_failure(config);
        return DECK_WIFI_CLEAR_STORAGE_ERROR;
    }
    refresh_store_state(config);
    config->state = DECK_WIFI_CONFIG_NO_ACTIVE;
    config->validation_started_ms = 0;
    return DECK_WIFI_CLEAR_CLEARED;
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
