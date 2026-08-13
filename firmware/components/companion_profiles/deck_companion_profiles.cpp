#include "deck_companion_profiles.h"

#include <cstring>
#include <memory>
#include <new>
#include <mutex>

namespace {

constexpr uint8_t kSchemaVersion = 2;
constexpr uint8_t kProtocolVersion = 1;
constexpr uint8_t kNoActiveProfile = 0xffU;
constexpr uint8_t kRecordMagic[4] = {'D', 'C', 'P', '2'};
constexpr uint8_t kMarkerMagic[4] = {'D', 'C', 'M', '2'};

struct StoredProfile {
    uint8_t profile_version;
    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    char display_name[DECK_COMPANION_DISPLAY_NAME_CAPACITY];
    char hub_address[DECK_COMPANION_HUB_ADDRESS_CAPACITY];
    char token[DECK_COMPANION_TOKEN_CAPACITY];
    char certificate_fingerprint[DECK_COMPANION_FINGERPRINT_CAPACITY];
    uint16_t certificate_der_size;
    uint8_t certificate_der[DECK_COMPANION_CERTIFICATE_DER_CAPACITY];
    int32_t priority;
    uint64_t last_success_unix_ms;
};

struct StoredSet {
    uint8_t count;
    uint8_t active_index;
    StoredProfile profiles[DECK_COMPANION_MAX_PROFILES];
};

void secure_clear(void *value, size_t size)
{
    auto *bytes = static_cast<volatile uint8_t *>(value);
    while (size != 0) {
        *bytes++ = 0;
        --size;
    }
}

bool terminated(const char *value, size_t capacity)
{
    return value != nullptr && std::memchr(value, '\0', capacity) != nullptr;
}

bool safe_ascii(const char *value, size_t capacity, bool allow_empty)
{
    if (!terminated(value, capacity)) {
        return false;
    }
    const size_t size = std::strlen(value);
    if ((!allow_empty && size == 0) || size >= capacity) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        const unsigned char byte = static_cast<unsigned char>(value[index]);
        if (byte < 0x21U || byte > 0x7eU) {
            return false;
        }
    }
    return true;
}

bool valid_base64url(const char *value, size_t expected_size)
{
    if (value == nullptr || std::strlen(value) != expected_size) {
        return false;
    }
    for (size_t index = 0; index < expected_size; ++index) {
        const char byte = value[index];
        if (!((byte >= 'A' && byte <= 'Z') || (byte >= 'a' && byte <= 'z') ||
              (byte >= '0' && byte <= '9') || byte == '_' || byte == '-')) {
            return false;
        }
    }
    return true;
}

bool valid_fingerprint(const char *value)
{
    constexpr char kPrefix[] = "sha256:";
    if (value == nullptr || std::strlen(value) != 71 ||
        std::memcmp(value, kPrefix, sizeof(kPrefix) - 1) != 0) {
        return false;
    }
    for (size_t index = sizeof(kPrefix) - 1; index < 71; ++index) {
        const char byte = value[index];
        if (!((byte >= '0' && byte <= '9') || (byte >= 'a' && byte <= 'f'))) {
            return false;
        }
    }
    return true;
}

bool valid_host_character(char byte, bool ipv6)
{
    if ((byte >= 'a' && byte <= 'z') || (byte >= 'A' && byte <= 'Z') ||
        (byte >= '0' && byte <= '9') || byte == '.' || byte == '-') {
        return true;
    }
    return ipv6 && (byte == ':' || byte == '%' || byte == '_');
}

bool valid_address(const char *address)
{
    if (!safe_ascii(address, DECK_COMPANION_HUB_ADDRESS_CAPACITY, false)) {
        return false;
    }
    const size_t size = std::strlen(address);
    size_t host_begin = 0;
    size_t host_end = 0;
    size_t port_begin = 0;
    bool ipv6 = false;
    if (address[0] == '[') {
        ipv6 = true;
        const char *closing = std::strchr(address, ']');
        if (closing == nullptr || closing == address + 1 || closing[1] != ':') {
            return false;
        }
        host_begin = 1;
        host_end = static_cast<size_t>(closing - address);
        port_begin = host_end + 2;
    } else {
        const char *colon = std::strrchr(address, ':');
        if (colon == nullptr || colon == address || std::strchr(address, ':') != colon) {
            return false;
        }
        host_end = static_cast<size_t>(colon - address);
        port_begin = host_end + 1;
    }
    if (port_begin >= size) {
        return false;
    }
    for (size_t index = host_begin; index < host_end; ++index) {
        if (!valid_host_character(address[index], ipv6)) {
            return false;
        }
    }
    uint32_t port = 0;
    for (size_t index = port_begin; index < size; ++index) {
        if (address[index] < '0' || address[index] > '9') {
            return false;
        }
        port = port * 10U + static_cast<uint32_t>(address[index] - '0');
        if (port > 65535U) {
            return false;
        }
    }
    return port != 0;
}

bool address_port(const char *address, uint16_t *output)
{
    if (output == nullptr || !valid_address(address)) {
        return false;
    }
    const char *separator = std::strrchr(address, ':');
    uint32_t port = 0;
    for (const char *current = separator + 1; *current != '\0'; ++current) {
        port = port * 10U + static_cast<uint32_t>(*current - '0');
    }
    *output = static_cast<uint16_t>(port);
    return true;
}

bool valid_pairing_address(const char *address, uint16_t expected_port)
{
    uint16_t port = 0;
    if (!address_port(address, &port) || port != expected_port) {
        return false;
    }
    unsigned octet = 0;
    unsigned consumed = 0;
    const int matched = std::sscanf(
        address,
        "192.168.4.%u:%*u%n",
        &octet,
        &consumed
    );
    return matched == 1 && consumed > 0 &&
           static_cast<size_t>(consumed) == std::strlen(address) && octet >= 2U &&
           octet <= 254U;
}

bool valid_code(const char *code)
{
    if (!terminated(code, DECK_COMPANION_PAIRING_CODE_CAPACITY) ||
        std::strlen(code) != DECK_COMPANION_PAIRING_CODE_CAPACITY - 1) {
        return false;
    }
    for (size_t index = 0; index < DECK_COMPANION_PAIRING_CODE_CAPACITY - 1; ++index) {
        if (code[index] < '0' || code[index] > '9') {
            return false;
        }
    }
    return true;
}

void write_u32(uint8_t *output, uint32_t value)
{
    output[0] = static_cast<uint8_t>(value & 0xffU);
    output[1] = static_cast<uint8_t>((value >> 8U) & 0xffU);
    output[2] = static_cast<uint8_t>((value >> 16U) & 0xffU);
    output[3] = static_cast<uint8_t>((value >> 24U) & 0xffU);
}

void write_u16(uint8_t *output, uint16_t value)
{
    output[0] = static_cast<uint8_t>(value & 0xffU);
    output[1] = static_cast<uint8_t>((value >> 8U) & 0xffU);
}

uint16_t read_u16(const uint8_t *input)
{
    return static_cast<uint16_t>(input[0]) |
           static_cast<uint16_t>(static_cast<uint16_t>(input[1]) << 8U);
}

uint32_t read_u32(const uint8_t *input)
{
    return static_cast<uint32_t>(input[0]) |
           static_cast<uint32_t>(input[1]) << 8U |
           static_cast<uint32_t>(input[2]) << 16U |
           static_cast<uint32_t>(input[3]) << 24U;
}

void write_u64(uint8_t *output, uint64_t value)
{
    for (unsigned index = 0; index < 8; ++index) {
        output[index] = static_cast<uint8_t>((value >> (index * 8U)) & 0xffU);
    }
}

uint64_t read_u64(const uint8_t *input)
{
    uint64_t value = 0;
    for (unsigned index = 0; index < 8; ++index) {
        value |= static_cast<uint64_t>(input[index]) << (index * 8U);
    }
    return value;
}

bool append_string(
    uint8_t *output,
    size_t capacity,
    size_t *offset,
    const char *value,
    size_t value_capacity
)
{
    if (!terminated(value, value_capacity)) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size > UINT8_MAX || *offset + 1 + size > capacity) {
        return false;
    }
    output[(*offset)++] = static_cast<uint8_t>(size);
    std::memcpy(output + *offset, value, size);
    *offset += size;
    return true;
}

bool read_string(
    const uint8_t *input,
    size_t size,
    size_t *offset,
    char *value,
    size_t capacity
)
{
    if (*offset >= size) {
        return false;
    }
    const size_t value_size = input[(*offset)++];
    if (value_size >= capacity || *offset + value_size > size) {
        return false;
    }
    std::memcpy(value, input + *offset, value_size);
    value[value_size] = '\0';
    *offset += value_size;
    return true;
}

bool append_bytes(
    uint8_t *output,
    size_t capacity,
    size_t *offset,
    const uint8_t *value,
    size_t value_size
)
{
    if (value == nullptr || value_size == 0 || value_size > UINT16_MAX ||
        *offset + 2 + value_size > capacity) {
        return false;
    }
    write_u16(output + *offset, static_cast<uint16_t>(value_size));
    *offset += 2;
    std::memcpy(output + *offset, value, value_size);
    *offset += value_size;
    return true;
}

bool read_bytes(
    const uint8_t *input,
    size_t size,
    size_t *offset,
    uint8_t *value,
    size_t capacity,
    uint16_t *value_size
)
{
    if (*offset + 2 > size || value == nullptr || value_size == nullptr) {
        return false;
    }
    const uint16_t decoded_size = read_u16(input + *offset);
    *offset += 2;
    if (decoded_size == 0 || decoded_size > capacity ||
        *offset + decoded_size > size) {
        return false;
    }
    std::memcpy(value, input + *offset, decoded_size);
    *offset += decoded_size;
    *value_size = decoded_size;
    return true;
}

bool valid_profile(const StoredProfile &profile)
{
    return profile.profile_version == DECK_COMPANION_PROFILE_VERSION &&
           valid_fingerprint(profile.profile_id) &&
           safe_ascii(profile.display_name, sizeof(profile.display_name), false) &&
           valid_address(profile.hub_address) &&
           terminated(profile.token, sizeof(profile.token)) &&
           valid_base64url(profile.token, 43) &&
           valid_fingerprint(profile.certificate_fingerprint) &&
           profile.certificate_der_size != 0 &&
           profile.certificate_der_size <= sizeof(profile.certificate_der) &&
           std::strcmp(profile.profile_id, profile.certificate_fingerprint) == 0;
}

bool valid_set(const StoredSet &set)
{
    if (set.count > DECK_COMPANION_MAX_PROFILES ||
        (set.count == 0 && set.active_index != kNoActiveProfile) ||
        (set.count != 0 && set.active_index >= set.count)) {
        return false;
    }
    for (size_t index = 0; index < set.count; ++index) {
        if (!valid_profile(set.profiles[index])) {
            return false;
        }
        for (size_t other = 0; other < index; ++other) {
            if (std::strcmp(
                    set.profiles[index].profile_id,
                    set.profiles[other].profile_id
                ) == 0) {
                return false;
            }
        }
    }
    return true;
}

size_t encode_set(const StoredSet &set, uint8_t *output, size_t capacity)
{
    if (output == nullptr || capacity < 2 || !valid_set(set)) {
        return 0;
    }
    size_t offset = 0;
    output[offset++] = set.count;
    output[offset++] = set.active_index;
    for (size_t index = 0; index < set.count; ++index) {
        const StoredProfile &profile = set.profiles[index];
        if (offset + 13 > capacity) {
            return 0;
        }
        output[offset++] = profile.profile_version;
        write_u32(output + offset, static_cast<uint32_t>(profile.priority));
        offset += 4;
        write_u64(output + offset, profile.last_success_unix_ms);
        offset += 8;
        if (!append_string(output, capacity, &offset, profile.profile_id, sizeof(profile.profile_id)) ||
            !append_string(output, capacity, &offset, profile.display_name, sizeof(profile.display_name)) ||
            !append_string(output, capacity, &offset, profile.hub_address, sizeof(profile.hub_address)) ||
            !append_string(output, capacity, &offset, profile.token, sizeof(profile.token)) ||
            !append_string(output, capacity, &offset, profile.certificate_fingerprint, sizeof(profile.certificate_fingerprint)) ||
            !append_bytes(output, capacity, &offset, profile.certificate_der, profile.certificate_der_size)) {
            return 0;
        }
    }
    return offset;
}

bool decode_set(const uint8_t *input, size_t size, StoredSet *set)
{
    if (input == nullptr || set == nullptr || size < 2) {
        return false;
    }
    *set = {};
    size_t offset = 0;
    set->count = input[offset++];
    set->active_index = input[offset++];
    if (set->count > DECK_COMPANION_MAX_PROFILES) {
        return false;
    }
    for (size_t index = 0; index < set->count; ++index) {
        if (offset + 13 > size) {
            return false;
        }
        StoredProfile &profile = set->profiles[index];
        profile.profile_version = input[offset++];
        profile.priority = static_cast<int32_t>(read_u32(input + offset));
        offset += 4;
        profile.last_success_unix_ms = read_u64(input + offset);
        offset += 8;
        if (!read_string(input, size, &offset, profile.profile_id, sizeof(profile.profile_id)) ||
            !read_string(input, size, &offset, profile.display_name, sizeof(profile.display_name)) ||
            !read_string(input, size, &offset, profile.hub_address, sizeof(profile.hub_address)) ||
            !read_string(input, size, &offset, profile.token, sizeof(profile.token)) ||
            !read_string(input, size, &offset, profile.certificate_fingerprint, sizeof(profile.certificate_fingerprint)) ||
            !read_bytes(input, size, &offset, profile.certificate_der, sizeof(profile.certificate_der), &profile.certificate_der_size)) {
            secure_clear(set, sizeof(*set));
            return false;
        }
    }
    const bool valid = offset == size && valid_set(*set);
    if (!valid) {
        secure_clear(set, sizeof(*set));
    }
    return valid;
}

bool validate_payload(void *, const uint8_t *payload, size_t size)
{
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr) {
        return false;
    }
    const bool valid = decode_set(payload, size, set.get());
    secure_clear(set.get(), sizeof(*set));
    return valid;
}

deck_companion_record_status_t map_status(deck_transaction_record_status_t status)
{
    switch (status) {
        case DECK_TRANSACTION_RECORD_VALID:
            return DECK_COMPANION_RECORD_VALID;
        case DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS:
            return DECK_COMPANION_RECORD_RECOVERED_PREVIOUS;
        case DECK_TRANSACTION_RECORD_CORRUPT:
            return DECK_COMPANION_RECORD_CORRUPT;
        case DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA:
            return DECK_COMPANION_RECORD_UNSUPPORTED_SCHEMA;
        case DECK_TRANSACTION_RECORD_MIGRATION_FAILED:
            return DECK_COMPANION_RECORD_MIGRATION_FAILED;
        case DECK_TRANSACTION_RECORD_IO_ERROR:
            return DECK_COMPANION_RECORD_IO_ERROR;
        case DECK_TRANSACTION_RECORD_EMPTY:
        default:
            return DECK_COMPANION_RECORD_EMPTY;
    }
}

bool valid_profile_id_argument(const char *profile_id)
{
    return profile_id != nullptr &&
           terminated(profile_id, DECK_COMPANION_PROFILE_ID_CAPACITY) &&
           valid_fingerprint(profile_id);
}

int find_profile(const StoredSet &set, const char *profile_id)
{
    for (size_t index = 0; index < set.count; ++index) {
        if (std::strcmp(set.profiles[index].profile_id, profile_id) == 0) {
            return static_cast<int>(index);
        }
    }
    return -1;
}

bool copy_string(char *output, size_t capacity, const char *input)
{
    if (output == nullptr || input == nullptr) {
        return false;
    }
    const size_t size = std::strlen(input);
    if (size >= capacity) {
        return false;
    }
    std::memcpy(output, input, size + 1);
    return true;
}

}  // namespace

struct deck_companion_profiles {
    deck_transaction_store_t *store;
    deck_companion_pairing_adapter_t pairing;
    mutable std::mutex mutex;
};

namespace {

bool load_set(
    const deck_companion_profiles_t *profiles,
    deck_transaction_store_snapshot_t *stored,
    StoredSet *set
)
{
    if (!deck_transaction_store_snapshot(profiles->store, stored)) {
        return false;
    }
    if (!stored->has_active) {
        *set = {};
        set->active_index = kNoActiveProfile;
        return true;
    }
    return decode_set(stored->active.payload, stored->active.payload_size, set);
}

bool commit_set(deck_companion_profiles_t *profiles, const StoredSet &set)
{
    std::unique_ptr<uint8_t[]> payload(
        new (std::nothrow) uint8_t[DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY]
    );
    if (payload == nullptr) {
        return false;
    }
    const size_t size = encode_set(
        set,
        payload.get(),
        DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY
    );
    const bool updated = size != 0 &&
                         deck_transaction_store_stage(
                             profiles->store,
                             payload.get(),
                             size
                         ) ==
                             DECK_TRANSACTION_UPDATED &&
                         deck_transaction_store_commit(profiles->store) ==
                             DECK_TRANSACTION_UPDATED;
    secure_clear(payload.get(), DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY);
    return updated;
}

deck_companion_profile_update_result_t update_field(
    deck_companion_profiles_t *profiles,
    const char *profile_id,
    int32_t priority,
    uint64_t unix_ms,
    bool update_priority
)
{
    if (profiles == nullptr || !valid_profile_id_argument(profile_id)) {
        return DECK_COMPANION_PROFILE_INVALID_ARGUMENT;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);
    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get())) {
        secure_clear(&stored, sizeof(stored));
        return DECK_COMPANION_PROFILE_STORAGE_FAILURE;
    }
    const int found = find_profile(*set, profile_id);
    if (found < 0) {
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PROFILE_NOT_FOUND;
    }
    StoredProfile &profile = set->profiles[static_cast<size_t>(found)];
    if (update_priority) {
        profile.priority = priority;
    } else {
        profile.last_success_unix_ms = unix_ms;
    }
    const bool committed = commit_set(profiles, *set);
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return committed ? DECK_COMPANION_PROFILE_UPDATED
                     : DECK_COMPANION_PROFILE_STORAGE_FAILURE;
}

}  // namespace

deck_companion_profiles_t *deck_companion_profiles_create(
    const deck_companion_profiles_options_t *options
)
{
    if (options == nullptr || options->pairing.redeem == nullptr) {
        return nullptr;
    }
    deck_transaction_store_options_t store_options{};
    store_options.storage = options->storage;
    std::memcpy(store_options.record_magic, kRecordMagic, sizeof(kRecordMagic));
    std::memcpy(store_options.marker_magic, kMarkerMagic, sizeof(kMarkerMagic));
    store_options.schema_version = kSchemaVersion;
    store_options.payload_capacity = DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY;
    store_options.validate_payload = validate_payload;

    auto *profiles = new (std::nothrow) deck_companion_profiles_t{};
    if (profiles == nullptr) {
        return nullptr;
    }
    profiles->pairing = options->pairing;
    profiles->store = deck_transaction_store_create(&store_options);
    if (profiles->store == nullptr) {
        delete profiles;
        return nullptr;
    }
    return profiles;
}

void deck_companion_profiles_destroy(deck_companion_profiles_t *profiles)
{
    if (profiles != nullptr) {
        deck_transaction_store_destroy(profiles->store);
        secure_clear(&profiles->pairing, sizeof(profiles->pairing));
    }
    delete profiles;
}

deck_companion_pair_result_t deck_companion_profiles_pair(
    deck_companion_profiles_t *profiles,
    const deck_companion_pair_request_t *request
)
{
    uint16_t hub_port = 0;
    if (profiles == nullptr || request == nullptr ||
        !terminated(request->hub_address, sizeof(request->hub_address)) ||
        !terminated(request->pairing_address, sizeof(request->pairing_address)) ||
        !address_port(request->hub_address, &hub_port) ||
        !valid_pairing_address(request->pairing_address, hub_port)) {
        return DECK_COMPANION_PAIR_INVALID_ADDRESS;
    }
    if (!deck_companion_pairing_code_valid(request->code)) {
        return DECK_COMPANION_PAIR_INVALID_CODE;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);

    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get()) ||
        stored.storage_faulted) {
        secure_clear(&stored, sizeof(stored));
        if (set != nullptr) {
            secure_clear(set.get(), sizeof(*set));
        }
        return DECK_COMPANION_PAIR_STORAGE_FAILURE;
    }
    std::unique_ptr<deck_companion_pairing_credential_t> credential(
        new (std::nothrow) deck_companion_pairing_credential_t{}
    );
    if (credential == nullptr) {
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PAIR_STORAGE_FAILURE;
    }
    if (!profiles->pairing.redeem(
            profiles->pairing.context,
            request->hub_address,
            request->pairing_address,
            request->code,
            credential.get()
        )) {
        secure_clear(credential.get(), sizeof(*credential));
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PAIR_REDEEM_FAILED;
    }
    const bool credential_valid =
        terminated(credential->token, sizeof(credential->token)) &&
        terminated(
            credential->certificate_fingerprint,
            sizeof(credential->certificate_fingerprint)
        ) &&
        valid_base64url(credential->token, 43) &&
        valid_fingerprint(credential->certificate_fingerprint) &&
        credential->certificate_der_size != 0 &&
        credential->certificate_der_size <= sizeof(credential->certificate_der) &&
        credential->protocol_version == kProtocolVersion;
    if (!credential_valid) {
        secure_clear(credential.get(), sizeof(*credential));
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PAIR_INVALID_CREDENTIAL;
    }

    int found = find_profile(*set, credential->certificate_fingerprint);
    if (found < 0 && set->count >= DECK_COMPANION_MAX_PROFILES) {
        secure_clear(credential.get(), sizeof(*credential));
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PAIR_CAPACITY_REACHED;
    }
    if (found < 0) {
        found = set->count;
        ++set->count;
    }
    StoredProfile &profile = set->profiles[static_cast<size_t>(found)];
    const int32_t previous_priority = profile.priority;
    const uint64_t previous_success = profile.last_success_unix_ms;
    profile = {};
    profile.profile_version = DECK_COMPANION_PROFILE_VERSION;
    profile.priority = previous_priority;
    profile.last_success_unix_ms = previous_success;
    const bool copied =
        copy_string(profile.profile_id, sizeof(profile.profile_id), credential->certificate_fingerprint) &&
        copy_string(profile.display_name, sizeof(profile.display_name), request->hub_address) &&
        copy_string(profile.hub_address, sizeof(profile.hub_address), request->hub_address) &&
        copy_string(profile.token, sizeof(profile.token), credential->token) &&
        copy_string(profile.certificate_fingerprint, sizeof(profile.certificate_fingerprint), credential->certificate_fingerprint);
    if (copied) {
        profile.certificate_der_size = static_cast<uint16_t>(credential->certificate_der_size);
        std::memcpy(
            profile.certificate_der,
            credential->certificate_der,
            credential->certificate_der_size
        );
    }
    set->active_index = static_cast<uint8_t>(found);
    const bool committed = copied && commit_set(profiles, *set);
    secure_clear(credential.get(), sizeof(*credential));
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return committed ? DECK_COMPANION_PAIR_PAIRED
                     : DECK_COMPANION_PAIR_STORAGE_FAILURE;
}

deck_companion_profile_update_result_t deck_companion_profiles_select_active(
    deck_companion_profiles_t *profiles,
    const char *profile_id
)
{
    if (profiles == nullptr || !valid_profile_id_argument(profile_id)) {
        return DECK_COMPANION_PROFILE_INVALID_ARGUMENT;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);
    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get())) {
        secure_clear(&stored, sizeof(stored));
        return DECK_COMPANION_PROFILE_STORAGE_FAILURE;
    }
    const int found = find_profile(*set, profile_id);
    if (found < 0) {
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PROFILE_NOT_FOUND;
    }
    set->active_index = static_cast<uint8_t>(found);
    const bool committed = commit_set(profiles, *set);
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return committed ? DECK_COMPANION_PROFILE_UPDATED
                     : DECK_COMPANION_PROFILE_STORAGE_FAILURE;
}

deck_companion_profile_update_result_t deck_companion_profiles_revoke(
    deck_companion_profiles_t *profiles,
    const char *profile_id
)
{
    if (profiles == nullptr || !valid_profile_id_argument(profile_id)) {
        return DECK_COMPANION_PROFILE_INVALID_ARGUMENT;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);
    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get())) {
        secure_clear(&stored, sizeof(stored));
        return DECK_COMPANION_PROFILE_STORAGE_FAILURE;
    }
    const int found = find_profile(*set, profile_id);
    if (found < 0) {
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return DECK_COMPANION_PROFILE_NOT_FOUND;
    }
    const uint8_t removed = static_cast<uint8_t>(found);
    for (size_t index = removed; index + 1 < set->count; ++index) {
        set->profiles[index] = set->profiles[index + 1];
    }
    secure_clear(&set->profiles[set->count - 1], sizeof(set->profiles[0]));
    --set->count;
    if (set->count == 0) {
        set->active_index = kNoActiveProfile;
    } else if (set->active_index == removed) {
        set->active_index = 0;
    } else if (set->active_index > removed) {
        --set->active_index;
    }
    const bool committed = commit_set(profiles, *set);
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return committed ? DECK_COMPANION_PROFILE_UPDATED
                     : DECK_COMPANION_PROFILE_STORAGE_FAILURE;
}

deck_companion_profile_update_result_t deck_companion_profiles_set_priority(
    deck_companion_profiles_t *profiles,
    const char *profile_id,
    int32_t priority
)
{
    return update_field(profiles, profile_id, priority, 0, true);
}

deck_companion_profile_update_result_t deck_companion_profiles_record_success(
    deck_companion_profiles_t *profiles,
    const char *profile_id,
    uint64_t unix_ms
)
{
    if (unix_ms == 0) {
        return DECK_COMPANION_PROFILE_INVALID_ARGUMENT;
    }
    return update_field(profiles, profile_id, 0, unix_ms, false);
}

bool deck_companion_profiles_snapshot(
    const deck_companion_profiles_t *profiles,
    deck_companion_profiles_snapshot_t *snapshot
)
{
    if (profiles == nullptr || snapshot == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);
    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get())) {
        secure_clear(&stored, sizeof(stored));
        return false;
    }
    *snapshot = {};
    snapshot->record_status = map_status(stored.record_status);
    snapshot->candidate_record_status = map_status(stored.candidate_record_status);
    snapshot->storage_faulted = stored.storage_faulted;
    snapshot->generation = stored.has_active ? stored.active.generation : 0;
    snapshot->count = set->count;
    snapshot->has_active = set->count != 0;
    for (size_t index = 0; index < set->count; ++index) {
        const StoredProfile &source = set->profiles[index];
        deck_companion_profile_view_t &target = snapshot->profiles[index];
        target.profile_version = source.profile_version;
        target.priority = source.priority;
        target.last_success_unix_ms = source.last_success_unix_ms;
        if (!copy_string(target.profile_id, sizeof(target.profile_id), source.profile_id) ||
            !copy_string(target.display_name, sizeof(target.display_name), source.display_name) ||
            !copy_string(target.hub_address, sizeof(target.hub_address), source.hub_address) ||
            !copy_string(target.certificate_fingerprint, sizeof(target.certificate_fingerprint), source.certificate_fingerprint)) {
            secure_clear(snapshot, sizeof(*snapshot));
            secure_clear(&stored, sizeof(stored));
            secure_clear(set.get(), sizeof(*set));
            return false;
        }
    }
    if (set->count != 0 && !copy_string(
                              snapshot->active_profile_id,
                              sizeof(snapshot->active_profile_id),
                              set->profiles[set->active_index].profile_id
                          )) {
        secure_clear(snapshot, sizeof(*snapshot));
        secure_clear(&stored, sizeof(stored));
        secure_clear(set.get(), sizeof(*set));
        return false;
    }
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return true;
}

bool deck_companion_profiles_active_secret(
    const deck_companion_profiles_t *profiles,
    deck_companion_profile_secret_t *secret
)
{
    if (profiles == nullptr || secret == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(profiles->mutex);
    *secret = {};
    deck_transaction_store_snapshot_t stored{};
    std::unique_ptr<StoredSet> set(new (std::nothrow) StoredSet{});
    if (set == nullptr || !load_set(profiles, &stored, set.get()) || set->count == 0) {
        secure_clear(&stored, sizeof(stored));
        if (set != nullptr) {
            secure_clear(set.get(), sizeof(*set));
        }
        return false;
    }
    const StoredProfile &profile = set->profiles[set->active_index];
    const bool copied =
        copy_string(secret->profile_id, sizeof(secret->profile_id), profile.profile_id) &&
        copy_string(secret->hub_address, sizeof(secret->hub_address), profile.hub_address) &&
        copy_string(secret->token, sizeof(secret->token), profile.token) &&
        copy_string(secret->certificate_fingerprint, sizeof(secret->certificate_fingerprint), profile.certificate_fingerprint);
    if (copied) {
        secret->certificate_der_size = profile.certificate_der_size;
        std::memcpy(
            secret->certificate_der,
            profile.certificate_der,
            profile.certificate_der_size
        );
    }
    secret->protocol_version = kProtocolVersion;
    if (!copied) {
        secure_clear(secret, sizeof(*secret));
    }
    secure_clear(&stored, sizeof(stored));
    secure_clear(set.get(), sizeof(*set));
    return copied;
}

void deck_companion_profile_secret_clear(deck_companion_profile_secret_t *secret)
{
    if (secret != nullptr) {
        secure_clear(secret, sizeof(*secret));
    }
}

bool deck_companion_hub_address_valid(const char *hub_address)
{
    return valid_address(hub_address);
}

bool deck_companion_hub_address_port(const char *hub_address, uint16_t *port)
{
    return address_port(hub_address, port);
}

bool deck_companion_pairing_code_valid(const char *pairing_code)
{
    return valid_code(pairing_code);
}
