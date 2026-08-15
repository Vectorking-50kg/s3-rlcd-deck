#include "deck_ota_transaction.h"

#include <cstring>
#include <new>

struct deck_ota_transaction {
    deck_ota_transaction_options_t options{};
    deck_ota_manifest_t manifest{};
    deck_ota_transaction_snapshot_t snapshot{};
    uint64_t started_ms = 0;
    uint64_t last_activity_ms = 0;
    bool flash_open = false;
    bool hash_open = false;
};

namespace {

bool terminated(const char *value, size_t capacity)
{
    return value != nullptr && std::memchr(value, '\0', capacity) != nullptr;
}

bool safe_version(const char *value)
{
    if (!terminated(value, DECK_OTA_VERSION_CAPACITY)) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size == 0 || size >= 32U || value[0] < '0' ||
        value[0] > '9') {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        const char byte = value[index];
        const bool alpha_numeric =
            (byte >= '0' && byte <= '9') || (byte >= 'A' && byte <= 'Z') ||
            (byte >= 'a' && byte <= 'z');
        if (!alpha_numeric && byte != '.' && byte != '-' && byte != '+') {
            return false;
        }
    }
    return true;
}

bool safe_board(const char *value)
{
    if (!terminated(value, DECK_OTA_BOARD_CAPACITY)) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size == 0 || size >= DECK_OTA_BOARD_CAPACITY) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        const char byte = value[index];
        if (!((byte >= 'a' && byte <= 'z') || (byte >= '0' && byte <= '9') ||
              byte == '.' || byte == '-')) {
            return false;
        }
    }
    return true;
}

bool parse_semantic_core(const char *version, uint32_t output[3])
{
    if (!safe_version(version)) {
        return false;
    }
    const char *cursor = version;
    for (size_t part = 0; part < 3; ++part) {
        if (*cursor < '0' || *cursor > '9' ||
            (*cursor == '0' && cursor[1] >= '0' && cursor[1] <= '9')) {
            return false;
        }
        uint32_t value = 0;
        do {
            const uint32_t digit = static_cast<uint32_t>(*cursor - '0');
            if (value > (UINT32_MAX - digit) / 10U) {
                return false;
            }
            value = value * 10U + digit;
            ++cursor;
        } while (*cursor >= '0' && *cursor <= '9');
        output[part] = value;
        if (part < 2) {
            if (*cursor++ != '.') {
                return false;
            }
        }
    }
    return *cursor == '\0' || *cursor == '-' || *cursor == '+';
}

bool strictly_newer(const char *candidate, const char *running)
{
    uint32_t candidate_parts[3]{};
    uint32_t running_parts[3]{};
    if (!parse_semantic_core(candidate, candidate_parts) ||
        !parse_semantic_core(running, running_parts)) {
        return false;
    }
    for (size_t index = 0; index < 3; ++index) {
        if (candidate_parts[index] != running_parts[index]) {
            return candidate_parts[index] > running_parts[index];
        }
    }
    return false;
}

bool digest_equal(const uint8_t *left, const uint8_t *right)
{
    uint8_t difference = 0;
    for (size_t index = 0; index < DECK_OTA_DIGEST_BYTES; ++index) {
        difference |= static_cast<uint8_t>(left[index] ^ right[index]);
    }
    return difference == 0;
}

void close_open_resources(deck_ota_transaction_t *transaction)
{
    if (transaction->flash_open) {
        transaction->options.flash.abort(transaction->options.flash.context);
        transaction->flash_open = false;
    }
    if (transaction->hash_open) {
        transaction->options.crypto.hash_abort(transaction->options.crypto.context);
        transaction->hash_open = false;
    }
}

deck_ota_result_t fail(
    deck_ota_transaction_t *transaction,
    deck_ota_result_t result
)
{
    close_open_resources(transaction);
    transaction->snapshot.state = DECK_OTA_FAILED;
    transaction->snapshot.result = result;
    return result;
}

}  // namespace

deck_ota_transaction_t *deck_ota_transaction_create(
    const deck_ota_transaction_options_t *options
)
{
    if (options == nullptr || options->flash.begin == nullptr ||
        options->flash.write == nullptr || options->flash.finish == nullptr ||
        options->flash.abort == nullptr || options->flash.select_boot == nullptr ||
        options->crypto.hash_begin == nullptr ||
        options->crypto.hash_update == nullptr ||
        options->crypto.hash_finish == nullptr ||
        options->crypto.hash_abort == nullptr ||
        options->crypto.verify_manifest == nullptr ||
        !safe_version(options->running_version) || !safe_board(options->board) ||
        options->protocol_version == 0 || options->partition_capacity == 0 ||
        options->partition_capacity > UINT32_MAX ||
        options->inactivity_timeout_ms == 0 ||
        options->maximum_duration_ms == 0) {
        return nullptr;
    }
    auto *transaction = new (std::nothrow) deck_ota_transaction_t{};
    if (transaction == nullptr) {
        return nullptr;
    }
    transaction->options = *options;
    transaction->snapshot = {DECK_OTA_IDLE, DECK_OTA_OK, 0, 0};
    return transaction;
}

void deck_ota_transaction_destroy(deck_ota_transaction_t *transaction)
{
    if (transaction != nullptr) {
        close_open_resources(transaction);
        std::memset(&transaction->manifest, 0, sizeof(transaction->manifest));
        delete transaction;
    }
}

deck_ota_result_t deck_ota_transaction_offer(
    deck_ota_transaction_t *transaction,
    const deck_ota_manifest_t *manifest,
    uint64_t now_ms
)
{
    if (transaction == nullptr || manifest == nullptr) {
        return DECK_OTA_INVALID_MANIFEST;
    }
    if (transaction->snapshot.state != DECK_OTA_IDLE) {
        return DECK_OTA_BUSY;
    }
    if (!safe_version(manifest->version) || !safe_board(manifest->board) ||
        manifest->image_length == 0 || manifest->signing_key_id == 0 ||
        manifest->minimum_protocol_version == 0) {
        return fail(transaction, DECK_OTA_INVALID_MANIFEST);
    }
    if (std::strcmp(manifest->board, transaction->options.board) != 0) {
        return fail(transaction, DECK_OTA_WRONG_BOARD);
    }
    if (manifest->minimum_protocol_version > transaction->options.protocol_version) {
        return fail(transaction, DECK_OTA_INCOMPATIBLE_PROTOCOL);
    }
    if (!strictly_newer(manifest->version, transaction->options.running_version)) {
        return fail(transaction, DECK_OTA_DOWNGRADE_REJECTED);
    }
    if (manifest->image_length > transaction->options.partition_capacity) {
        return fail(transaction, DECK_OTA_IMAGE_TOO_LARGE);
    }
    if (!transaction->options.crypto.verify_manifest(
            transaction->options.crypto.context,
            manifest
        )) {
        return fail(transaction, DECK_OTA_SIGNATURE_REJECTED);
    }
    if (!transaction->options.crypto.hash_begin(
            transaction->options.crypto.context
        )) {
        return fail(transaction, DECK_OTA_FLASH_FAILURE);
    }
    transaction->hash_open = true;
    if (!transaction->options.flash.begin(
            transaction->options.flash.context,
            manifest->image_length
        )) {
        return fail(transaction, DECK_OTA_FLASH_FAILURE);
    }
    transaction->flash_open = true;
    transaction->manifest = *manifest;
    transaction->snapshot = {
        DECK_OTA_RECEIVING,
        DECK_OTA_OK,
        0,
        manifest->image_length,
    };
    transaction->last_activity_ms = now_ms;
    transaction->started_ms = now_ms;
    return DECK_OTA_OK;
}

deck_ota_result_t deck_ota_transaction_write(
    deck_ota_transaction_t *transaction,
    uint32_t offset,
    const uint8_t *data,
    size_t size,
    bool final,
    uint64_t now_ms
)
{
    if (transaction == nullptr || data == nullptr || size == 0 ||
        size > DECK_OTA_MAX_CHUNK_BYTES ||
        transaction->snapshot.state != DECK_OTA_RECEIVING) {
        return DECK_OTA_INVALID_MANIFEST;
    }
    if (now_ms < transaction->started_ms ||
        now_ms - transaction->started_ms >=
            transaction->options.maximum_duration_ms ||
        now_ms < transaction->last_activity_ms ||
        now_ms - transaction->last_activity_ms >=
            transaction->options.inactivity_timeout_ms) {
        return fail(transaction, DECK_OTA_TIMED_OUT);
    }
    if (offset != transaction->snapshot.received_bytes ||
        size > transaction->snapshot.image_length -
                   transaction->snapshot.received_bytes ||
        (final && size != transaction->snapshot.image_length -
                              transaction->snapshot.received_bytes) ||
        (!final && size == transaction->snapshot.image_length -
                               transaction->snapshot.received_bytes)) {
        return fail(transaction, DECK_OTA_STALE_OFFSET);
    }
    if (!transaction->options.crypto.hash_update(
            transaction->options.crypto.context,
            data,
            size
        ) ||
        !transaction->options.flash.write(
            transaction->options.flash.context,
            data,
            size
        )) {
        return fail(transaction, DECK_OTA_FLASH_FAILURE);
    }
    transaction->snapshot.received_bytes += static_cast<uint32_t>(size);
    transaction->last_activity_ms = now_ms;
    if (!final) {
        return DECK_OTA_OK;
    }
    uint8_t digest[DECK_OTA_DIGEST_BYTES]{};
    if (!transaction->options.crypto.hash_finish(
            transaction->options.crypto.context,
            digest
        )) {
        std::memset(digest, 0, sizeof(digest));
        return fail(transaction, DECK_OTA_FLASH_FAILURE);
    }
    transaction->hash_open = false;
    if (!digest_equal(digest, transaction->manifest.image_sha256)) {
        std::memset(digest, 0, sizeof(digest));
        return fail(transaction, DECK_OTA_HASH_MISMATCH);
    }
    std::memset(digest, 0, sizeof(digest));
    const bool image_valid = transaction->options.flash.finish(
        transaction->options.flash.context
    );
    transaction->flash_open = false;
    if (!image_valid) {
        transaction->snapshot.state = DECK_OTA_FAILED;
        transaction->snapshot.result = DECK_OTA_IMAGE_INVALID;
        return DECK_OTA_IMAGE_INVALID;
    }
    if (!transaction->options.flash.select_boot(
            transaction->options.flash.context
        )) {
        transaction->snapshot.state = DECK_OTA_FAILED;
        transaction->snapshot.result = DECK_OTA_FLASH_FAILURE;
        return DECK_OTA_FLASH_FAILURE;
    }
    transaction->snapshot.state = DECK_OTA_READY_TO_REBOOT;
    transaction->snapshot.result = DECK_OTA_OK;
    return DECK_OTA_OK;
}

deck_ota_result_t deck_ota_transaction_tick(
    deck_ota_transaction_t *transaction,
    uint64_t now_ms
)
{
    if (transaction == nullptr) {
        return DECK_OTA_INVALID_MANIFEST;
    }
    if (transaction->snapshot.state == DECK_OTA_RECEIVING &&
        (now_ms < transaction->started_ms ||
         now_ms - transaction->started_ms >=
             transaction->options.maximum_duration_ms ||
         now_ms < transaction->last_activity_ms ||
         now_ms - transaction->last_activity_ms >=
             transaction->options.inactivity_timeout_ms)) {
        return fail(transaction, DECK_OTA_TIMED_OUT);
    }
    return transaction->snapshot.result;
}

bool deck_ota_transaction_snapshot(
    const deck_ota_transaction_t *transaction,
    deck_ota_transaction_snapshot_t *snapshot
)
{
    if (transaction == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = transaction->snapshot;
    return true;
}
