#include "deck_transaction_store.h"

#include <array>
#include <cstring>
#include <new>
#include <vector>

namespace {

constexpr size_t kRecordHeaderSize = 12;
constexpr size_t kChecksumSize = 4;
constexpr size_t kMarkerSize = 20;

enum class DecodeResult : uint8_t {
    valid,
    corrupt,
    unsupported,
    migration_failed,
};

struct DecodedMarker {
    uint8_t active_slot;
    uint32_t active_generation;
    bool has_previous;
    uint8_t previous_slot;
    uint32_t previous_generation;
};

struct OwnedRecord {
    std::vector<uint8_t> payload;
    uint32_t generation = 0;
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

deck_transaction_record_status_t status_for_decode(DecodeResult result)
{
    switch (result) {
        case DecodeResult::unsupported:
            return DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA;
        case DecodeResult::migration_failed:
            return DECK_TRANSACTION_RECORD_MIGRATION_FAILED;
        case DecodeResult::corrupt:
        default:
            return DECK_TRANSACTION_RECORD_CORRUPT;
    }
}

deck_transaction_storage_key_t slot_key(uint8_t slot)
{
    return slot == 0 ? DECK_TRANSACTION_STORAGE_SLOT_0
                     : DECK_TRANSACTION_STORAGE_SLOT_1;
}

}  // namespace

struct deck_transaction_store {
    deck_transaction_store_options_t options;
    deck_transaction_record_status_t record_status = DECK_TRANSACTION_RECORD_EMPTY;
    deck_transaction_record_status_t candidate_record_status =
        DECK_TRANSACTION_RECORD_EMPTY;
    OwnedRecord active{};
    OwnedRecord candidate{};
    uint8_t active_slot = 0;
    bool has_active = false;
    bool has_candidate = false;
    bool storage_faulted = false;
    bool marker_present = false;
    bool marker_matches_active = false;
};

namespace {

size_t encode_record(
    const deck_transaction_store_t *store,
    const uint8_t *payload,
    size_t payload_size,
    uint32_t generation,
    std::vector<uint8_t> *output
)
{
    if (store == nullptr || payload == nullptr || output == nullptr || payload_size == 0 ||
        payload_size < store->options.payload_length_excluded_prefix ||
        payload_size > store->options.payload_capacity || generation == 0) {
        return 0;
    }
    output->assign(kRecordHeaderSize + payload_size + kChecksumSize, 0);
    std::memcpy(output->data(), store->options.record_magic, 4);
    (*output)[4] = store->options.schema_version;
    const size_t encoded_payload_size =
        payload_size - store->options.payload_length_excluded_prefix;
    (*output)[6] = static_cast<uint8_t>(encoded_payload_size & 0xffU);
    (*output)[7] = static_cast<uint8_t>((encoded_payload_size >> 8U) & 0xffU);
    write_u32(output->data() + 8, generation);
    std::memcpy(output->data() + kRecordHeaderSize, payload, payload_size);
    const size_t checksum_offset = kRecordHeaderSize + payload_size;
    write_u32(output->data() + checksum_offset, crc32(output->data(), checksum_offset));
    return checksum_offset + kChecksumSize;
}

DecodeResult decode_record(
    const deck_transaction_store_t *store,
    const uint8_t *input,
    size_t size,
    OwnedRecord *record
)
{
    if (store == nullptr || input == nullptr || record == nullptr ||
        size < kRecordHeaderSize + kChecksumSize ||
        std::memcmp(input, store->options.record_magic, 4) != 0) {
        return DecodeResult::corrupt;
    }
    if (input[4] > store->options.schema_version) {
        return DecodeResult::unsupported;
    }
    if (input[4] < store->options.schema_version) {
        return DecodeResult::migration_failed;
    }
    const size_t encoded_payload_size = static_cast<size_t>(input[6]) |
                                        static_cast<size_t>(input[7]) << 8U;
    if (encoded_payload_size > store->options.payload_capacity -
                                   store->options.payload_length_excluded_prefix) {
        return DecodeResult::corrupt;
    }
    const size_t payload_size = encoded_payload_size +
                                store->options.payload_length_excluded_prefix;
    const size_t checksum_offset = kRecordHeaderSize + payload_size;
    const uint32_t generation = read_u32(input + 8);
    if (input[5] != 0 || payload_size == 0 ||
        payload_size > store->options.payload_capacity ||
        checksum_offset + kChecksumSize != size || generation == 0 ||
        read_u32(input + checksum_offset) != crc32(input, checksum_offset) ||
        !store->options.validate_payload(
            store->options.payload_context,
            input + kRecordHeaderSize,
            payload_size
        )) {
        return DecodeResult::corrupt;
    }
    record->payload.assign(
        input + kRecordHeaderSize,
        input + kRecordHeaderSize + payload_size
    );
    record->generation = generation;
    return DecodeResult::valid;
}

void encode_marker(
    const deck_transaction_store_t *store,
    uint8_t active_slot,
    uint32_t active_generation,
    bool has_previous,
    uint8_t previous_slot,
    uint32_t previous_generation,
    std::array<uint8_t, kMarkerSize> *output
)
{
    output->fill(0);
    std::memcpy(output->data(), store->options.marker_magic, 4);
    (*output)[4] = store->options.schema_version;
    (*output)[5] = active_slot;
    write_u32(output->data() + 6, active_generation);
    (*output)[10] = has_previous ? 1U : 0U;
    (*output)[11] = has_previous ? previous_slot : 0U;
    write_u32(output->data() + 12, has_previous ? previous_generation : 0U);
    write_u32(output->data() + 16, crc32(output->data(), 16));
}

DecodeResult decode_marker(
    const deck_transaction_store_t *store,
    const uint8_t *input,
    size_t size,
    DecodedMarker *marker
)
{
    if (store == nullptr || input == nullptr || marker == nullptr || size != kMarkerSize ||
        std::memcmp(input, store->options.marker_magic, 4) != 0) {
        return DecodeResult::corrupt;
    }
    if (input[4] > store->options.schema_version) {
        return DecodeResult::unsupported;
    }
    if (input[4] < store->options.schema_version) {
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

deck_transaction_storage_result_t read_encoded_record(
    const deck_transaction_store_t *store,
    deck_transaction_storage_key_t key,
    OwnedRecord *record,
    DecodeResult *decode_result
)
{
    std::vector<uint8_t> encoded(
        kRecordHeaderSize + store->options.payload_capacity + kChecksumSize
    );
    size_t size = 0;
    const deck_transaction_storage_result_t result = store->options.storage.read(
        store->options.storage.context,
        key,
        encoded.data(),
        encoded.size(),
        &size
    );
    if (result == DECK_TRANSACTION_STORAGE_OK) {
        *decode_result = decode_record(store, encoded.data(), size, record);
    }
    return result;
}

bool records_equal(
    const OwnedRecord &left,
    const OwnedRecord &right
)
{
    return left.generation == right.generation && left.payload == right.payload;
}

void set_storage_failure(deck_transaction_store_t *store)
{
    store->storage_faulted = true;
}

void load_candidate(deck_transaction_store_t *store)
{
    DecodeResult decoded = DecodeResult::corrupt;
    const deck_transaction_storage_result_t result = read_encoded_record(
        store,
        DECK_TRANSACTION_STORAGE_CANDIDATE,
        &store->candidate,
        &decoded
    );
    if (result == DECK_TRANSACTION_STORAGE_NOT_FOUND) {
        return;
    }
    if (result == DECK_TRANSACTION_STORAGE_ERROR) {
        store->candidate_record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
        set_storage_failure(store);
        return;
    }
    store->candidate_record_status = status_for_decode(decoded);
    if (decoded == DecodeResult::valid) {
        store->candidate_record_status = DECK_TRANSACTION_RECORD_VALID;
        store->has_candidate = true;
    }
}

void load_active(deck_transaction_store_t *store)
{
    std::array<uint8_t, kMarkerSize> marker_bytes{};
    size_t marker_size = 0;
    const deck_transaction_storage_result_t marker_result = store->options.storage.read(
        store->options.storage.context,
        DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
        marker_bytes.data(),
        marker_bytes.size(),
        &marker_size
    );
    if (marker_result == DECK_TRANSACTION_STORAGE_NOT_FOUND) {
        return;
    }
    if (marker_result == DECK_TRANSACTION_STORAGE_ERROR) {
        store->record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
        set_storage_failure(store);
        return;
    }
    store->marker_present = true;
    DecodedMarker marker{};
    const DecodeResult marker_decode = decode_marker(
        store,
        marker_bytes.data(),
        marker_size,
        &marker
    );
    if (marker_decode != DecodeResult::valid) {
        store->record_status = status_for_decode(marker_decode);
        return;
    }

    OwnedRecord selected{};
    DecodeResult selected_decode = DecodeResult::corrupt;
    const deck_transaction_storage_result_t selected_read = read_encoded_record(
        store,
        slot_key(marker.active_slot),
        &selected,
        &selected_decode
    );
    if (selected_read == DECK_TRANSACTION_STORAGE_OK &&
        selected_decode == DecodeResult::valid &&
        selected.generation == marker.active_generation) {
        store->active = selected;
        store->active_slot = marker.active_slot;
        store->has_active = true;
        store->record_status = DECK_TRANSACTION_RECORD_VALID;
        store->marker_matches_active = true;
        return;
    }

    OwnedRecord previous{};
    DecodeResult previous_decode = DecodeResult::corrupt;
    deck_transaction_storage_result_t previous_read =
        DECK_TRANSACTION_STORAGE_NOT_FOUND;
    if (marker.has_previous) {
        previous_read = read_encoded_record(
            store,
            slot_key(marker.previous_slot),
            &previous,
            &previous_decode
        );
    }
    if (marker.has_previous && previous_read == DECK_TRANSACTION_STORAGE_OK &&
        previous_decode == DecodeResult::valid &&
        previous.generation == marker.previous_generation) {
        store->active = previous;
        store->active_slot = marker.previous_slot;
        store->has_active = true;
        store->record_status = DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS;
        if (selected_read == DECK_TRANSACTION_STORAGE_ERROR) {
            set_storage_failure(store);
        }
        return;
    }
    if (selected_read == DECK_TRANSACTION_STORAGE_ERROR ||
        previous_read == DECK_TRANSACTION_STORAGE_ERROR) {
        store->record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
        set_storage_failure(store);
    } else if (selected_read == DECK_TRANSACTION_STORAGE_OK) {
        store->record_status = selected_decode == DecodeResult::valid
                                   ? DECK_TRANSACTION_RECORD_CORRUPT
                                   : status_for_decode(selected_decode);
    } else {
        store->record_status = DECK_TRANSACTION_RECORD_CORRUPT;
    }
}

bool rollback_marker(
    deck_transaction_store_t *store,
    bool had_marker,
    const std::array<uint8_t, kMarkerSize> &marker,
    size_t marker_size
)
{
    if (had_marker) {
        return store->options.storage.write(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
            marker.data(),
            marker_size
        );
    }
    return store->options.storage.erase(
        store->options.storage.context,
        DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
    );
}

bool write_and_verify_marker(
    deck_transaction_store_t *store,
    const std::array<uint8_t, kMarkerSize> &marker
)
{
    if (!store->options.storage.write(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
            marker.data(),
            marker.size()
        )) {
        return false;
    }
    std::array<uint8_t, kMarkerSize> verified{};
    size_t verified_size = 0;
    return store->options.storage.read(
               store->options.storage.context,
               DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
               verified.data(),
               verified.size(),
               &verified_size
           ) == DECK_TRANSACTION_STORAGE_OK &&
           verified_size == marker.size() &&
           std::memcmp(verified.data(), marker.data(), marker.size()) == 0;
}

}  // namespace

deck_transaction_store_t *deck_transaction_store_create(
    const deck_transaction_store_options_t *options
)
{
    if (options == nullptr || options->storage.read == nullptr ||
        options->storage.write == nullptr || options->storage.erase == nullptr ||
        options->validate_payload == nullptr || options->schema_version == 0) {
        return nullptr;
    }
    auto *store = new (std::nothrow) deck_transaction_store_t{};
    if (store == nullptr) {
        return nullptr;
    }
    store->options = *options;
    if (store->options.payload_capacity == 0) {
        store->options.payload_capacity = DECK_TRANSACTION_PAYLOAD_CAPACITY;
    }
    if (store->options.payload_capacity > DECK_TRANSACTION_MAX_PAYLOAD_CAPACITY ||
        store->options.payload_length_excluded_prefix >
            store->options.payload_capacity) {
        delete store;
        return nullptr;
    }
    load_candidate(store);
    load_active(store);
    return store;
}

void deck_transaction_store_destroy(deck_transaction_store_t *store)
{
    if (store != nullptr) {
        volatile uint8_t *bytes = store->active.payload.data();
        for (size_t index = 0; index < store->active.payload.size(); ++index) {
            bytes[index] = 0;
        }
        bytes = store->candidate.payload.data();
        for (size_t index = 0; index < store->candidate.payload.size(); ++index) {
            bytes[index] = 0;
        }
    }
    delete store;
}

deck_transaction_update_result_t deck_transaction_store_stage(
    deck_transaction_store_t *store,
    const uint8_t *payload,
    size_t size
)
{
    if (store == nullptr || payload == nullptr || size == 0 ||
        size < store->options.payload_length_excluded_prefix ||
        size > store->options.payload_capacity ||
        !store->options.validate_payload(store->options.payload_context, payload, size)) {
        return DECK_TRANSACTION_INVALID_PAYLOAD;
    }
    if (store->storage_faulted) {
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }
    uint32_t generation = store->has_active ? store->active.generation + 1 : 1;
    if (generation == 0) {
        generation = 1;
    }
    std::vector<uint8_t> encoded;
    const size_t encoded_size = encode_record(store, payload, size, generation, &encoded);
    if (encoded_size == 0 ||
        !store->options.storage.write(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_CANDIDATE,
            encoded.data(),
            encoded_size
        )) {
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }
    OwnedRecord verified{};
    DecodeResult decoded = DecodeResult::corrupt;
    if (read_encoded_record(
            store,
            DECK_TRANSACTION_STORAGE_CANDIDATE,
            &verified,
            &decoded
        ) != DECK_TRANSACTION_STORAGE_OK ||
        decoded != DecodeResult::valid || verified.generation != generation ||
        verified.payload.size() != size ||
        std::memcmp(verified.payload.data(), payload, size) != 0) {
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }
    store->candidate = verified;
    store->has_candidate = true;
    store->candidate_record_status = DECK_TRANSACTION_RECORD_VALID;
    return DECK_TRANSACTION_UPDATED;
}

deck_transaction_update_result_t deck_transaction_store_commit(
    deck_transaction_store_t *store
)
{
    if (store == nullptr || !store->has_candidate) {
        return DECK_TRANSACTION_NO_CANDIDATE;
    }
    if (store->storage_faulted) {
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }

    std::array<uint8_t, kMarkerSize> previous_marker{};
    size_t previous_marker_size = 0;
    bool had_marker = false;
    if (store->has_active) {
        encode_marker(
            store,
            store->active_slot,
            store->active.generation,
            false,
            0,
            0,
            &previous_marker
        );
        previous_marker_size = previous_marker.size();
        had_marker = true;
        if (!store->marker_matches_active) {
            if (!write_and_verify_marker(store, previous_marker)) {
                set_storage_failure(store);
                return DECK_TRANSACTION_STORAGE_FAILURE;
            }
            store->marker_present = true;
            store->marker_matches_active = true;
        }
    } else if (store->marker_present) {
        if (!store->options.storage.erase(
                store->options.storage.context,
                DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
            )) {
            set_storage_failure(store);
            return DECK_TRANSACTION_STORAGE_FAILURE;
        }
        store->marker_present = false;
    }

    const uint8_t next_slot = store->has_active
                                  ? static_cast<uint8_t>(1U - store->active_slot)
                                  : 0U;
    std::vector<uint8_t> encoded;
    const size_t encoded_size = encode_record(
        store,
        store->candidate.payload.data(),
        store->candidate.payload.size(),
        store->candidate.generation,
        &encoded
    );
    if (encoded_size == 0 ||
        !store->options.storage.write(
            store->options.storage.context,
            slot_key(next_slot),
            encoded.data(),
            encoded_size
        )) {
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }
    OwnedRecord verified{};
    DecodeResult decoded = DecodeResult::corrupt;
    if (read_encoded_record(store, slot_key(next_slot), &verified, &decoded) !=
            DECK_TRANSACTION_STORAGE_OK ||
        decoded != DecodeResult::valid || !records_equal(verified, store->candidate)) {
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }

    std::array<uint8_t, kMarkerSize> marker{};
    encode_marker(
        store,
        next_slot,
        verified.generation,
        store->has_active,
        store->active_slot,
        store->has_active ? store->active.generation : 0,
        &marker
    );
    if (!store->options.storage.write(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
            marker.data(),
            marker.size()
        )) {
        (void)rollback_marker(store, had_marker, previous_marker, previous_marker_size);
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }
    std::array<uint8_t, kMarkerSize> verified_marker{};
    size_t verified_marker_size = 0;
    if (store->options.storage.read(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
            verified_marker.data(),
            verified_marker.size(),
            &verified_marker_size
        ) != DECK_TRANSACTION_STORAGE_OK ||
        verified_marker_size != marker.size() ||
        std::memcmp(verified_marker.data(), marker.data(), marker.size()) != 0) {
        (void)rollback_marker(store, had_marker, previous_marker, previous_marker_size);
        set_storage_failure(store);
        return DECK_TRANSACTION_STORAGE_FAILURE;
    }

    store->active = verified;
    store->active_slot = next_slot;
    store->has_active = true;
    store->record_status = DECK_TRANSACTION_RECORD_VALID;
    store->marker_present = true;
    store->marker_matches_active = true;
    if (store->options.storage.erase(
            store->options.storage.context,
            DECK_TRANSACTION_STORAGE_CANDIDATE
        )) {
        store->candidate = OwnedRecord{};
        store->has_candidate = false;
        store->candidate_record_status = DECK_TRANSACTION_RECORD_EMPTY;
    } else {
        store->candidate_record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
    }
    return DECK_TRANSACTION_UPDATED;
}

bool deck_transaction_store_clear(deck_transaction_store_t *store)
{
    if (store == nullptr) {
        return false;
    }
    bool erased = store->options.storage.erase(
        store->options.storage.context,
        DECK_TRANSACTION_STORAGE_ACTIVE_MARKER
    );
    erased = store->options.storage.erase(
                 store->options.storage.context,
                 DECK_TRANSACTION_STORAGE_CANDIDATE
             ) &&
             erased;
    erased = store->options.storage.erase(
                 store->options.storage.context,
                 DECK_TRANSACTION_STORAGE_SLOT_0
             ) &&
             erased;
    erased = store->options.storage.erase(
                 store->options.storage.context,
                 DECK_TRANSACTION_STORAGE_SLOT_1
             ) &&
             erased;
    if (!erased) {
        set_storage_failure(store);
        return false;
    }
    store->record_status = DECK_TRANSACTION_RECORD_EMPTY;
    store->candidate_record_status = DECK_TRANSACTION_RECORD_EMPTY;
    store->active = OwnedRecord{};
    store->candidate = OwnedRecord{};
    store->active_slot = 0;
    store->has_active = false;
    store->has_candidate = false;
    store->storage_faulted = false;
    store->marker_present = false;
    store->marker_matches_active = false;
    return true;
}

bool deck_transaction_store_snapshot(
    const deck_transaction_store_t *store,
    deck_transaction_store_snapshot_t *snapshot
)
{
    if (store == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = {};
    snapshot->record_status = store->record_status;
    snapshot->candidate_record_status = store->candidate_record_status;
    snapshot->active = {
        store->active.payload.empty() ? nullptr : store->active.payload.data(),
        store->active.payload.size(),
        store->active.generation,
    };
    snapshot->candidate = {
        store->candidate.payload.empty() ? nullptr : store->candidate.payload.data(),
        store->candidate.payload.size(),
        store->candidate.generation,
    };
    snapshot->active_slot = store->active_slot;
    snapshot->has_active = store->has_active;
    snapshot->has_candidate = store->has_candidate;
    snapshot->storage_faulted = store->storage_faulted;
    return true;
}
