#include "deck_ota_transaction.h"

#include <array>
#include <cassert>
#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

namespace {

struct FakeAdapters {
    bool signature_valid = true;
    bool flash_write_valid = true;
    bool image_valid = true;
    bool select_valid = true;
    unsigned begin_count = 0;
    unsigned finish_count = 0;
    unsigned abort_count = 0;
    unsigned select_count = 0;
    size_t hash_size = 0;
    std::vector<uint8_t> image;
    std::array<uint8_t, DECK_OTA_DIGEST_BYTES> digest{};
};

bool flash_begin(void *context, size_t image_size)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    ++fake->begin_count;
    fake->image.clear();
    fake->image.reserve(image_size);
    return true;
}

bool flash_write(void *context, const uint8_t *data, size_t size)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    if (!fake->flash_write_valid) {
        return false;
    }
    fake->image.insert(fake->image.end(), data, data + size);
    return true;
}

bool flash_finish(void *context)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    ++fake->finish_count;
    return fake->image_valid;
}

void flash_abort(void *context)
{
    ++static_cast<FakeAdapters *>(context)->abort_count;
}

bool select_boot(void *context)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    ++fake->select_count;
    return fake->select_valid;
}

bool hash_begin(void *context)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    fake->digest.fill(0);
    fake->hash_size = 0;
    return true;
}

bool hash_update(void *context, const uint8_t *data, size_t size)
{
    auto *fake = static_cast<FakeAdapters *>(context);
    for (size_t index = 0; index < size; ++index) {
        fake->digest[(fake->hash_size + index) % fake->digest.size()] ^= data[index];
    }
    fake->hash_size += size;
    return true;
}

bool hash_finish(void *context, uint8_t output[DECK_OTA_DIGEST_BYTES])
{
    std::memcpy(
        output,
        static_cast<FakeAdapters *>(context)->digest.data(),
        DECK_OTA_DIGEST_BYTES
    );
    return true;
}

void hash_abort(void *) {}

bool verify_manifest(void *context, const deck_ota_manifest_t *)
{
    return static_cast<FakeAdapters *>(context)->signature_valid;
}

std::array<uint8_t, DECK_OTA_DIGEST_BYTES> digest_for(
    const std::vector<uint8_t> &image
)
{
    std::array<uint8_t, DECK_OTA_DIGEST_BYTES> digest{};
    for (size_t index = 0; index < image.size(); ++index) {
        digest[index % digest.size()] ^= image[index];
    }
    return digest;
}

deck_ota_manifest_t manifest_for(const std::vector<uint8_t> &image)
{
    deck_ota_manifest_t manifest{};
    std::strcpy(manifest.version, "0.3.0");
    std::strcpy(manifest.board, "esp32-s3-rlcd-4.2");
    manifest.image_length = static_cast<uint32_t>(image.size());
    const auto digest = digest_for(image);
    std::memcpy(manifest.image_sha256, digest.data(), digest.size());
    manifest.signature[0] = 0xa5;
    manifest.signing_key_id = 1;
    manifest.minimum_protocol_version = 1;
    return manifest;
}

deck_ota_transaction_t *create_transaction(
    FakeAdapters *fake,
    uint64_t maximum_duration_ms = 600'000
)
{
    deck_ota_transaction_options_t options{};
    options.flash = {
        flash_begin,
        flash_write,
        flash_finish,
        flash_abort,
        select_boot,
        fake,
    };
    options.crypto = {
        hash_begin,
        hash_update,
        hash_finish,
        hash_abort,
        verify_manifest,
        fake,
    };
    options.running_version = "0.2.0-dev";
    options.board = "esp32-s3-rlcd-4.2";
    options.protocol_version = 1;
    options.partition_capacity = 1'740'800;
    options.inactivity_timeout_ms = 30'000;
    options.maximum_duration_ms = maximum_duration_ms;
    return deck_ota_transaction_create(&options);
}

void signed_image_streams_only_to_the_inactive_slot()
{
    FakeAdapters fake;
    deck_ota_transaction_t *transaction = create_transaction(&fake);
    assert(transaction != nullptr);
    const std::vector<uint8_t> image = {1, 2, 3, 4, 5, 6};
    const auto manifest = manifest_for(image);
    assert(deck_ota_transaction_offer(transaction, &manifest, 10) == DECK_OTA_OK);
    assert(deck_ota_transaction_write(
               transaction, 0, image.data(), 3, false, 20
           ) == DECK_OTA_OK);
    assert(deck_ota_transaction_write(
               transaction, 3, image.data() + 3, 3, true, 30
           ) == DECK_OTA_OK);
    assert(fake.begin_count == 1);
    assert(fake.finish_count == 1);
    assert(fake.select_count == 1);
    assert(fake.abort_count == 0);
    assert(fake.image == image);
    deck_ota_transaction_snapshot_t snapshot{};
    assert(deck_ota_transaction_snapshot(transaction, &snapshot));
    assert(snapshot.state == DECK_OTA_READY_TO_REBOOT);
    deck_ota_transaction_destroy(transaction);
}

void invalid_offer_never_erases_flash()
{
    const std::vector<uint8_t> image = {1, 2, 3};
    for (int failure = 0; failure < 7; ++failure) {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        if (failure == 0) {
            std::strcpy(manifest.board, "another-board");
        } else if (failure == 1) {
            manifest.minimum_protocol_version = 2;
        } else if (failure == 2) {
            std::strcpy(manifest.version, "0.1.9");
        } else if (failure == 3) {
            manifest.image_length = 1'740'801;
        } else if (failure == 4) {
            fake.signature_valid = false;
        } else if (failure == 5) {
            std::strcpy(manifest.version, "0.2.0-other");
        } else {
            std::strcpy(manifest.version, "1.123456789012345678901234567890");
        }
        assert(deck_ota_transaction_offer(transaction, &manifest, 10) != DECK_OTA_OK);
        assert(fake.begin_count == 0);
        assert(fake.select_count == 0);
        deck_ota_transaction_destroy(transaction);
    }
}

void interruption_mismatch_and_flash_failure_keep_old_boot_slot()
{
    const std::vector<uint8_t> image = {9, 8, 7, 6};
    {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        assert(deck_ota_transaction_offer(transaction, &manifest, 100) == DECK_OTA_OK);
        assert(deck_ota_transaction_write(
                   transaction, 0, image.data(), 2, false, 101
               ) == DECK_OTA_OK);
        assert(deck_ota_transaction_tick(transaction, 30'102) == DECK_OTA_TIMED_OUT);
        assert(fake.abort_count == 1 && fake.select_count == 0);
        deck_ota_transaction_destroy(transaction);
    }
    {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        manifest.image_sha256[0] ^= 1U;
        assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
        assert(deck_ota_transaction_write(
                   transaction, 0, image.data(), image.size(), true, 2
               ) == DECK_OTA_HASH_MISMATCH);
        assert(fake.abort_count == 1 && fake.select_count == 0);
        deck_ota_transaction_destroy(transaction);
    }
    {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
        fake.flash_write_valid = false;
        assert(deck_ota_transaction_write(
                   transaction, 0, image.data(), image.size(), true, 2
               ) == DECK_OTA_FLASH_FAILURE);
        assert(fake.abort_count == 1 && fake.select_count == 0);
        deck_ota_transaction_destroy(transaction);
    }
    {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
        fake.image_valid = false;
        assert(deck_ota_transaction_write(
                   transaction, 0, image.data(), image.size(), true, 2
               ) == DECK_OTA_IMAGE_INVALID);
        assert(fake.select_count == 0);
        deck_ota_transaction_destroy(transaction);
    }
    {
        FakeAdapters fake;
        deck_ota_transaction_t *transaction = create_transaction(&fake);
        auto manifest = manifest_for(image);
        assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
        fake.select_valid = false;
        assert(deck_ota_transaction_write(
                   transaction, 0, image.data(), image.size(), true, 2
               ) == DECK_OTA_FLASH_FAILURE);
        assert(fake.select_count == 1);
        deck_ota_transaction_destroy(transaction);
    }
}

void offsets_and_final_length_are_fail_closed()
{
    FakeAdapters fake;
    deck_ota_transaction_t *transaction = create_transaction(&fake);
    const std::vector<uint8_t> image = {1, 2, 3, 4};
    const auto manifest = manifest_for(image);
    assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
    assert(deck_ota_transaction_write(
               transaction, 1, image.data(), 2, false, 2
           ) == DECK_OTA_STALE_OFFSET);
    assert(fake.abort_count == 1 && fake.select_count == 0);
    deck_ota_transaction_destroy(transaction);
}

void continuous_activity_cannot_extend_the_total_deadline()
{
    FakeAdapters fake;
    deck_ota_transaction_t *transaction = create_transaction(&fake, 40'000);
    const std::vector<uint8_t> image = {1, 2, 3, 4};
    const auto manifest = manifest_for(image);
    assert(deck_ota_transaction_offer(transaction, &manifest, 1) == DECK_OTA_OK);
    assert(deck_ota_transaction_write(
               transaction, 0, image.data(), 2, false, 29'999
           ) == DECK_OTA_OK);
    assert(deck_ota_transaction_write(
               transaction, 2, image.data() + 2, 2, true, 40'001
           ) == DECK_OTA_TIMED_OUT);
    assert(fake.abort_count == 1 && fake.select_count == 0);
    deck_ota_transaction_destroy(transaction);
}

}  // namespace

int main()
{
    signed_image_streams_only_to_the_inactive_slot();
    invalid_offer_never_erases_flash();
    interruption_mismatch_and_flash_failure_keep_old_boot_slot();
    offsets_and_final_length_are_fail_closed();
    continuous_activity_cannot_extend_the_total_deadline();
    return 0;
}
