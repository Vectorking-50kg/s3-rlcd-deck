#include "deck_ai_snapshot_store.h"

#include <algorithm>
#include <array>
#include <atomic>
#include <cassert>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

namespace {

struct FakeStorage {
    std::map<deck_transaction_storage_key_t, std::vector<uint8_t>> values;
    std::vector<deck_transaction_storage_key_t> writes;
    deck_transaction_storage_key_t fail_write = DECK_TRANSACTION_STORAGE_KEY_COUNT;
    std::mutex mutex;
    std::condition_variable write_condition;
    std::condition_variable read_condition;
    bool block_reads = false;
    bool read_started = false;
    bool block_writes = false;
    bool write_started = false;
};

struct FakeProvider {
    FakeStorage *storage = nullptr;
    std::mutex mutex;
    std::condition_variable condition;
    bool blocked = false;
    bool open_started = false;
    size_t fail_open_count = 0;
    size_t close_count = 0;
};

deck_transaction_storage_result_t read_storage(
    void *context,
    deck_transaction_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    std::unique_lock<std::mutex> lock(storage->mutex);
    if (storage->block_reads) {
        storage->read_started = true;
        storage->read_condition.notify_all();
        storage->read_condition.wait(
            lock,
            [storage]() { return !storage->block_reads; }
        );
    }
    const auto value = storage->values.find(key);
    if (value == storage->values.end()) {
        return DECK_TRANSACTION_STORAGE_NOT_FOUND;
    }
    if (output == nullptr || size == nullptr || capacity < value->second.size()) {
        return DECK_TRANSACTION_STORAGE_ERROR;
    }
    std::memcpy(output, value->second.data(), value->second.size());
    *size = value->second.size();
    return DECK_TRANSACTION_STORAGE_OK;
}

bool write_storage(
    void *context,
    deck_transaction_storage_key_t key,
    const uint8_t *data,
    size_t size
)
{
    auto *storage = static_cast<FakeStorage *>(context);
    std::unique_lock<std::mutex> lock(storage->mutex);
    storage->writes.push_back(key);
    if (storage->block_writes) {
        storage->write_started = true;
        storage->write_condition.notify_all();
        storage->write_condition.wait(
            lock,
            [storage]() { return !storage->block_writes; }
        );
    }
    if (key == storage->fail_write || data == nullptr || size == 0) {
        return false;
    }
    storage->values[key] = std::vector<uint8_t>(data, data + size);
    return true;
}

bool erase_storage(void *context, deck_transaction_storage_key_t key)
{
    auto *storage = static_cast<FakeStorage *>(context);
    const std::lock_guard<std::mutex> lock(storage->mutex);
    storage->values.erase(key);
    return true;
}

bool open_provider_storage(
    void *context,
    deck_transaction_storage_adapter_t *adapter,
    void **handle
)
{
    auto *provider = static_cast<FakeProvider *>(context);
    std::unique_lock<std::mutex> lock(provider->mutex);
    provider->open_started = true;
    provider->condition.notify_all();
    provider->condition.wait(lock, [provider]() { return !provider->blocked; });
    if (provider->fail_open_count != 0) {
        --provider->fail_open_count;
        return false;
    }
    *adapter = {
        read_storage,
        write_storage,
        erase_storage,
        provider->storage,
    };
    *handle = provider;
    return true;
}

void close_provider_storage(void *context, void *handle)
{
    auto *provider = static_cast<FakeProvider *>(context);
    assert(handle == provider);
    const std::lock_guard<std::mutex> lock(provider->mutex);
    ++provider->close_count;
}

deck_ai_snapshot_store_t *create_store_async(FakeStorage *storage)
{
    deck_ai_snapshot_store_options_t options{};
    options.storage = {read_storage, write_storage, erase_storage, storage};
    return deck_ai_snapshot_store_create(&options);
}

deck_ai_snapshot_store_t *create_store(FakeStorage *storage)
{
    deck_ai_snapshot_store_t *store = create_store_async(storage);
    assert(store != nullptr);
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    return store;
}

deck_ai_snapshot_store_t *create_provider_store_async(FakeProvider *provider)
{
    deck_ai_snapshot_store_options_t options{};
    options.provider = {open_provider_storage, close_provider_storage, provider};
    return deck_ai_snapshot_store_create(&options);
}

std::string document_at(const char *timestamp)
{
    return std::string(
               "{\"type\":\"snapshot.ai\",\"protocol_version\":1,"
               "\"schema_version\":{\"major\":1,\"minor\":0},\"generated_at\":\""
           ) +
           timestamp +
           "\",\"timezone\":null,\"provider_order\":[],\"providers\":[],"
           "\"sessions\":[],\"next_refresh_seconds\":5}";
}

std::string conflicting_document_at(const char *timestamp)
{
    std::string document = document_at(timestamp);
    const size_t value = document.find("\"next_refresh_seconds\":5");
    assert(value != std::string::npos);
    document.replace(value, std::strlen("\"next_refresh_seconds\":5"),
                     "\"next_refresh_seconds\":6");
    return document;
}

uint64_t generated_at(const std::string &document)
{
    deck_ai_snapshot_metadata_t metadata{};
    assert(deck_ai_snapshot_validate(
               document.data(), document.size(), &metadata
           ) == DECK_AI_SNAPSHOT_ACCEPTED);
    return metadata.generated_at_unix_ms;
}

deck_ai_snapshot_store_update_result_t apply_and_wait(
    deck_ai_snapshot_store_t *store,
    const std::string &document,
    uint64_t trusted_utc_ms
)
{
    const deck_ai_snapshot_store_update_result_t result =
        deck_ai_snapshot_store_apply(
            store,
            document.data(),
            document.size(),
            trusted_utc_ms
        );
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    return result;
}

size_t write_count(FakeStorage *storage)
{
    const std::lock_guard<std::mutex> lock(storage->mutex);
    return storage->writes.size();
}

void fail_writes_to(
    FakeStorage *storage,
    deck_transaction_storage_key_t key
)
{
    const std::lock_guard<std::mutex> lock(storage->mutex);
    storage->fail_write = key;
}

struct VisibleSnapshot {
    std::string document;
    deck_ai_snapshot_store_snapshot_t state;
};

VisibleSnapshot copy_snapshot(
    deck_ai_snapshot_store_t *store,
    uint64_t now_utc_ms,
    bool online
)
{
    std::array<char, DECK_AI_SNAPSHOT_MAX_BYTES> output{};
    size_t size = 0;
    deck_ai_snapshot_store_snapshot_t snapshot{};
    assert(deck_ai_snapshot_store_copy(
        store,
        now_utc_ms,
        online,
        output.data(),
        output.size(),
        &size,
        &snapshot
    ));
    return {std::string(output.data(), size), snapshot};
}

void memory_updates_are_independent_of_flash_throttling()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(store != nullptr);
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string soon = document_at("2026-08-13T12:05:00Z");
    const std::string due = document_at("2026-08-13T12:31:00Z");

    assert(apply_and_wait(store, first, generated_at(first) + 1'000) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    const size_t first_write_count = write_count(&storage);
    assert(first_write_count >= 4U);
    assert(apply_and_wait(store, soon, generated_at(soon) + 1'000) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(write_count(&storage) == first_write_count);
    VisibleSnapshot visible = copy_snapshot(store, generated_at(soon), true);
    assert(visible.document == soon);
    assert(visible.state.persisted_generation == 1U);

    assert(apply_and_wait(store, due, generated_at(due) + 1'000) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    visible = copy_snapshot(store, generated_at(due), true);
    assert(visible.document == due);
    assert(visible.state.persisted_generation == 2U);
    deck_ai_snapshot_store_destroy(store);
}

void invalid_time_and_documents_preserve_the_last_valid_snapshot()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(store != nullptr);
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string older = document_at("2026-08-13T11:59:59Z");
    const std::string future = document_at("2026-08-13T12:01:00Z");
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);

    const std::string malformed = "{\"type\":\"snapshot.ai\"}";
    assert(deck_ai_snapshot_store_apply(
               store, malformed.data(), malformed.size(), generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_MALFORMED);
    std::string unsupported = first;
    const size_t major = unsupported.find("\"major\":1");
    assert(major != std::string::npos);
    unsupported[major + 8U] = '2';
    assert(deck_ai_snapshot_store_apply(
               store, unsupported.data(), unsupported.size(), generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_UNSUPPORTED_VERSION);
    std::string private_document = first;
    private_document.insert(private_document.size() - 1U, ",\"prompt\":\"secret\"");
    assert(deck_ai_snapshot_store_apply(
               store,
               private_document.data(),
               private_document.size(),
               generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_PRIVATE_DATA);
    const std::string oversized(DECK_AI_SNAPSHOT_MAX_BYTES + 1U, 'x');
    assert(deck_ai_snapshot_store_apply(
               store, oversized.data(), oversized.size(), generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_MALFORMED);
    assert(deck_ai_snapshot_store_apply(
               store, older.data(), older.size(), generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_TIME_ROLLBACK);
    assert(deck_ai_snapshot_store_apply(
               store, future.data(), future.size(), generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_INVALID_TIME);
    assert(copy_snapshot(store, generated_at(first), true).document == first);
    deck_ai_snapshot_store_destroy(store);
}

void bounded_transport_skew_is_not_mistaken_for_a_future_snapshot()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(store != nullptr);
    const std::string within_skew = document_at("2026-08-13T12:00:04Z");
    const std::string beyond_skew = document_at("2026-08-13T12:00:06Z");
    const uint64_t trusted_utc = generated_at(within_skew) - 4'000;

    assert(apply_and_wait(store, within_skew, trusted_utc) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(deck_ai_snapshot_store_apply(
               store,
               beyond_skew.data(),
               beyond_skew.size(),
               trusted_utc
           ) == DECK_AI_SNAPSHOT_STORE_INVALID_TIME);
    assert(copy_snapshot(store, generated_at(within_skew), true).document ==
           within_skew);
    deck_ai_snapshot_store_destroy(store);
}

void offline_policy_hides_the_document_at_twenty_four_hours()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    const std::string document = document_at("2026-08-13T12:00:00Z");
    const uint64_t generated = generated_at(document);
    assert(apply_and_wait(store, document, generated) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);

    VisibleSnapshot visible = copy_snapshot(
        store,
        generated + DECK_AI_SNAPSHOT_OFFLINE_LIMIT_MS - 1U,
        false
    );
    assert(visible.state.state == DECK_AI_SNAPSHOT_STORE_STALE);
    assert(visible.state.quotas_visible);
    assert(visible.document == document);

    visible = copy_snapshot(
        store,
        generated + DECK_AI_SNAPSHOT_OFFLINE_LIMIT_MS,
        false
    );
    assert(visible.state.state == DECK_AI_SNAPSHOT_STORE_UNAVAILABLE);
    assert(visible.state.has_snapshot);
    assert(!visible.state.document_visible);
    assert(!visible.state.quotas_visible);
    assert(visible.document.empty());

    visible = copy_snapshot(
        store,
        generated + DECK_AI_SNAPSHOT_OFFLINE_LIMIT_MS,
        true
    );
    assert(visible.state.state == DECK_AI_SNAPSHOT_STORE_UNAVAILABLE);
    assert(visible.document.empty());

    visible = copy_snapshot(store, generated - 1U, false);
    assert(visible.state.state == DECK_AI_SNAPSHOT_STORE_UNAVAILABLE);
    assert(visible.document.empty());
    deck_ai_snapshot_store_destroy(store);
}

void nvs_failure_keeps_new_memory_and_old_committed_flash()
{
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string next = document_at("2026-08-13T12:31:00Z");
    constexpr deck_transaction_storage_key_t kInterruptedWrites[] = {
        DECK_TRANSACTION_STORAGE_CANDIDATE,
        DECK_TRANSACTION_STORAGE_SLOT_1,
        DECK_TRANSACTION_STORAGE_ACTIVE_MARKER,
    };
    for (const deck_transaction_storage_key_t interrupted : kInterruptedWrites) {
        FakeStorage storage;
        deck_ai_snapshot_store_t *store = create_store(&storage);
        assert(apply_and_wait(store, first, generated_at(first)) ==
               DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        fail_writes_to(&storage, interrupted);
        assert(apply_and_wait(store, next, generated_at(next)) ==
               DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        VisibleSnapshot visible = copy_snapshot(store, generated_at(next), true);
        assert(visible.document == next);
        assert(visible.state.storage_faulted);
        assert(visible.state.persisted_generation == 1U);
        deck_ai_snapshot_store_destroy(store);

        fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_KEY_COUNT);
        store = create_store(&storage);
        visible = copy_snapshot(store, generated_at(next), true);
        assert(visible.document == first);
        assert(visible.state.persisted_generation == 1U);
        deck_ai_snapshot_store_destroy(store);
    }
}

void crc_corruption_recovers_previous_and_unknown_storage_schema_fails_closed()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string second = document_at("2026-08-13T12:31:00Z");
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(apply_and_wait(store, second, generated_at(second)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    deck_ai_snapshot_store_destroy(store);

    storage.values[DECK_TRANSACTION_STORAGE_SLOT_1].back() ^= 0x80U;
    store = create_store(&storage);
    VisibleSnapshot visible = copy_snapshot(store, generated_at(second), true);
    assert(visible.document == first);
    assert(visible.state.record_status == DECK_TRANSACTION_RECORD_RECOVERED_PREVIOUS);
    deck_ai_snapshot_store_destroy(store);

    storage.values[DECK_TRANSACTION_STORAGE_ACTIVE_MARKER][4] = 2U;
    store = create_store(&storage);
    visible = copy_snapshot(store, generated_at(second), false);
    assert(!visible.state.has_snapshot);
    assert(visible.state.record_status == DECK_TRANSACTION_RECORD_UNSUPPORTED_SCHEMA);
    deck_ai_snapshot_store_destroy(store);
}

void missing_storage_is_explicit_but_does_not_block_memory()
{
    deck_ai_snapshot_store_t *store = deck_ai_snapshot_store_create(nullptr);
    assert(store != nullptr);
    const std::string document = document_at("2026-08-13T12:00:00Z");
    assert(deck_ai_snapshot_store_apply(
               store, document.data(), document.size(), generated_at(document)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
    const VisibleSnapshot visible = copy_snapshot(store, generated_at(document), true);
    assert(visible.document == document);
    assert(visible.state.storage_faulted);
    deck_ai_snapshot_store_destroy(store);
}

void any_wall_clock_rollback_hides_until_the_high_water_is_recovered()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    const std::string document = document_at("2026-08-13T12:00:00Z");
    const uint64_t generated = generated_at(document);
    assert(apply_and_wait(store, document, generated) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);

    const uint64_t twenty = generated + 8U * 60U * 60U * 1'000U;
    const uint64_t thirteen = generated + 60U * 60U * 1'000U;
    assert(copy_snapshot(store, twenty, true).state.state ==
           DECK_AI_SNAPSHOT_STORE_FRESH);
    const VisibleSnapshot rolled_back = copy_snapshot(store, thirteen, true);
    assert(rolled_back.state.state == DECK_AI_SNAPSHOT_STORE_UNAVAILABLE);
    assert(rolled_back.document.empty());
    assert(copy_snapshot(store, twenty, true).state.state ==
           DECK_AI_SNAPSHOT_STORE_FRESH);
    deck_ai_snapshot_store_destroy(store);
}

void slow_flash_never_blocks_memory_publication_or_copy()
{
    FakeStorage storage;
    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_writes = true;
    }
    deck_ai_snapshot_store_t *store = create_store(&storage);
    const std::string document = document_at("2026-08-13T12:00:00Z");
    std::atomic<bool> apply_returned{false};
    std::thread publisher([&]() {
        assert(deck_ai_snapshot_store_apply(
                   store,
                   document.data(),
                   document.size(),
                   generated_at(document)
               ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        apply_returned.store(true, std::memory_order_release);
    });

    bool returned_before_flash = false;
    {
        std::unique_lock<std::mutex> lock(storage.mutex);
        assert(storage.write_condition.wait_for(
            lock,
            std::chrono::seconds(2),
            [&storage]() { return storage.write_started; }
        ));
        returned_before_flash = apply_returned.load(std::memory_order_acquire);
    }
    if (returned_before_flash) {
        assert(copy_snapshot(store, generated_at(document), true).document == document);
    }
    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_writes = false;
    }
    storage.write_condition.notify_all();
    publisher.join();
    assert(returned_before_flash);
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    deck_ai_snapshot_store_destroy(store);
}

void blocked_bootstrap_read_never_blocks_create_apply_or_copy()
{
    FakeStorage storage;
    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_reads = true;
    }
    const auto create_started = std::chrono::steady_clock::now();
    deck_ai_snapshot_store_t *store = create_store_async(&storage);
    assert(store != nullptr);
    assert(std::chrono::steady_clock::now() - create_started <
           std::chrono::milliseconds(250));

    {
        std::unique_lock<std::mutex> lock(storage.mutex);
        assert(storage.read_condition.wait_for(
            lock,
            std::chrono::seconds(2),
            [&storage]() { return storage.read_started; }
        ));
    }
    const std::string document = document_at("2026-08-13T12:00:00Z");
    const auto apply_started = std::chrono::steady_clock::now();
    assert(deck_ai_snapshot_store_apply(
               store,
               document.data(),
               document.size(),
               generated_at(document)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(std::chrono::steady_clock::now() - apply_started <
           std::chrono::milliseconds(250));
    assert(copy_snapshot(store, generated_at(document), true).document == document);

    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_reads = false;
    }
    storage.read_condition.notify_all();
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    assert(deck_ai_snapshot_store_destroy(store));
}

void blocked_storage_open_never_blocks_create_apply_or_copy()
{
    FakeStorage storage;
    FakeProvider provider;
    provider.storage = &storage;
    {
        const std::lock_guard<std::mutex> lock(provider.mutex);
        provider.blocked = true;
    }
    const auto create_started = std::chrono::steady_clock::now();
    deck_ai_snapshot_store_t *store = create_provider_store_async(&provider);
    assert(store != nullptr);
    assert(std::chrono::steady_clock::now() - create_started <
           std::chrono::milliseconds(250));
    {
        std::unique_lock<std::mutex> lock(provider.mutex);
        assert(provider.condition.wait_for(
            lock,
            std::chrono::seconds(2),
            [&provider]() { return provider.open_started; }
        ));
    }

    const std::string document = document_at("2026-08-13T12:00:00Z");
    assert(deck_ai_snapshot_store_apply(
               store,
               document.data(),
               document.size(),
               generated_at(document)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(copy_snapshot(store, generated_at(document), true).document == document);
    {
        const std::lock_guard<std::mutex> lock(provider.mutex);
        provider.blocked = false;
    }
    provider.condition.notify_all();
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    assert(deck_ai_snapshot_store_destroy(store));
    assert(provider.close_count == 1U);
}

void bootstrap_reconciliation_keeps_the_newest_authoritative_document()
{
    const std::string committed = document_at("2026-08-13T12:31:00Z");
    const std::array<std::string, 3> live_documents = {
        document_at("2026-08-13T12:00:00Z"),
        conflicting_document_at("2026-08-13T12:31:00Z"),
        document_at("2026-08-13T12:45:00Z"),
    };
    for (const std::string &live : live_documents) {
        FakeStorage storage;
        deck_ai_snapshot_store_t *store = create_store(&storage);
        assert(apply_and_wait(store, committed, generated_at(committed)) ==
               DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        assert(deck_ai_snapshot_store_destroy(store));

        {
            const std::lock_guard<std::mutex> lock(storage.mutex);
            storage.block_reads = true;
            storage.read_started = false;
        }
        store = create_store_async(&storage);
        assert(store != nullptr);
        {
            std::unique_lock<std::mutex> lock(storage.mutex);
            assert(storage.read_condition.wait_for(
                lock,
                std::chrono::seconds(2),
                [&storage]() { return storage.read_started; }
            ));
        }
        assert(deck_ai_snapshot_store_apply(
                   store,
                   live.data(),
                   live.size(),
                   generated_at(live)
               ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        {
            const std::lock_guard<std::mutex> lock(storage.mutex);
            storage.block_reads = false;
        }
        storage.read_condition.notify_all();
        assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
        const VisibleSnapshot reconciled = copy_snapshot(
            store,
            std::max(generated_at(committed), generated_at(live)),
            true
        );
        const std::string &expected = generated_at(live) > generated_at(committed)
                                          ? live
                                          : committed;
        assert(reconciled.document == expected);
        assert(reconciled.state.persisted_generation == 1U);
        assert(deck_ai_snapshot_store_destroy(store));
    }
}

void reopen_never_writes_an_obsolete_checkpoint_over_newer_flash()
{
    FakeStorage storage;
    const std::string committed = document_at("2026-08-13T12:31:00Z");
    deck_ai_snapshot_store_t *seed = create_store(&storage);
    assert(apply_and_wait(seed, committed, generated_at(committed)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(deck_ai_snapshot_store_destroy(seed));

    FakeProvider provider;
    provider.storage = &storage;
    provider.fail_open_count = 1;
    deck_ai_snapshot_store_t *store = create_provider_store_async(&provider);
    assert(store != nullptr);
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    const std::string old = document_at("2026-08-13T12:00:00Z");
    const std::string old_due = document_at("2026-08-13T12:01:00Z");
    assert(deck_ai_snapshot_store_apply(
               store,
               old.data(),
               old.size(),
               generated_at(old)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
    const size_t writes_before_reopen = write_count(&storage);
    const deck_ai_snapshot_store_update_result_t reopened =
        apply_and_wait(
            store,
            old_due,
            generated_at(old) + DECK_AI_SNAPSHOT_FLASH_INTERVAL_MS
        );
    assert(reopened == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY ||
           reopened == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
    assert(write_count(&storage) == writes_before_reopen);
    assert(copy_snapshot(store, generated_at(committed), true).document == committed);
    assert(deck_ai_snapshot_store_destroy(store));
    assert(provider.close_count == 1U);

    deck_ai_snapshot_store_t *restored = create_store(&storage);
    const VisibleSnapshot visible = copy_snapshot(
        restored,
        generated_at(committed),
        false
    );
    assert(visible.document == committed);
    assert(visible.state.persisted_generation == 1U);
    assert(deck_ai_snapshot_store_destroy(restored));
}

void live_update_during_reopen_replaces_the_captured_checkpoint()
{
    FakeStorage storage;
    FakeProvider provider;
    provider.storage = &storage;
    deck_ai_snapshot_store_t *store = create_provider_store_async(&provider);
    assert(store != nullptr);
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string failed = document_at("2026-08-13T12:31:00Z");
    const std::string captured = document_at("2026-08-13T13:02:00Z");
    const std::string newest = document_at("2026-08-13T13:10:00Z");
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_SLOT_1);
    assert(apply_and_wait(store, failed, generated_at(failed)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_KEY_COUNT);
    {
        const std::lock_guard<std::mutex> lock(provider.mutex);
        provider.blocked = true;
        provider.open_started = false;
    }
    (void)deck_ai_snapshot_store_apply(
        store,
        captured.data(),
        captured.size(),
        generated_at(captured)
    );
    {
        std::unique_lock<std::mutex> lock(provider.mutex);
        assert(provider.condition.wait_for(
            lock,
            std::chrono::seconds(2),
            [&provider]() { return provider.open_started; }
        ));
    }
    assert(deck_ai_snapshot_store_apply(
               store,
               newest.data(),
               newest.size(),
               generated_at(newest)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
    {
        const std::lock_guard<std::mutex> lock(provider.mutex);
        provider.blocked = false;
    }
    provider.condition.notify_all();
    assert(deck_ai_snapshot_store_wait_for_idle(store, 2'000));
    const VisibleSnapshot visible = copy_snapshot(store, generated_at(newest), true);
    assert(visible.document == newest);
    assert(visible.state.persisted_generation == 2U);
    assert(!visible.state.storage_faulted);
    assert(deck_ai_snapshot_store_destroy(store));
    assert(provider.close_count == 2U);
}

void failed_attempt_watermark_survives_restart()
{
    FakeStorage storage;
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string failed = document_at("2026-08-13T12:31:00Z");
    const std::string soon = document_at("2026-08-13T12:32:00Z");
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_SLOT_1);
    assert(apply_and_wait(store, failed, generated_at(failed)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    const size_t writes_after_failure = write_count(&storage);
    deck_ai_snapshot_store_destroy(store);

    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_KEY_COUNT);
    store = create_store(&storage);
    assert(apply_and_wait(store, soon, generated_at(soon)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(write_count(&storage) == writes_after_failure);
    assert(copy_snapshot(store, generated_at(soon), true).document == soon);
    deck_ai_snapshot_store_destroy(store);
}

void transient_failure_recovers_after_the_throttle_window()
{
    FakeStorage storage;
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string failed = document_at("2026-08-13T12:31:00Z");
    const std::string too_soon = document_at("2026-08-13T12:32:00Z");
    const std::string recovered = document_at("2026-08-13T13:02:00Z");
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_SLOT_1);
    assert(apply_and_wait(store, failed, generated_at(failed)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    const size_t writes_after_failure = write_count(&storage);

    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_KEY_COUNT);
    assert(apply_and_wait(store, too_soon, generated_at(too_soon)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
    assert(write_count(&storage) == writes_after_failure);
    assert(apply_and_wait(store, recovered, generated_at(recovered)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(write_count(&storage) > writes_after_failure);
    const VisibleSnapshot visible = copy_snapshot(
        store,
        generated_at(recovered),
        true
    );
    assert(visible.document == recovered);
    assert(visible.state.persisted_generation == 2U);
    assert(!visible.state.storage_faulted);
    deck_ai_snapshot_store_destroy(store);
}

void successful_reopen_resets_the_attempt_throttle_to_the_actual_write_time()
{
    FakeStorage storage;
    const std::string first = document_at("2026-08-13T12:00:00Z");
    const std::string failed = document_at("2026-08-13T12:31:00Z");
    const std::string recovered = document_at("2026-08-13T12:50:00Z");
    const std::string early = document_at("2026-08-13T13:21:00Z");
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(apply_and_wait(store, first, generated_at(first)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_SLOT_1);
    assert(apply_and_wait(
               store,
               failed,
               generated_at(failed) + 9U * 60U * 1'000U
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);

    fail_writes_to(&storage, DECK_TRANSACTION_STORAGE_KEY_COUNT);
    assert(apply_and_wait(
               store,
               recovered,
               generated_at(recovered) + 20U * 60U * 1'000U
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    const size_t writes_after_recovery = write_count(&storage);
    assert(apply_and_wait(store, early, generated_at(early)) ==
           DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(write_count(&storage) == writes_after_recovery);
    assert(copy_snapshot(store, generated_at(early), true).document == early);
    assert(deck_ai_snapshot_store_destroy(store));
}

void untrusted_attempt_watermark_starts_a_conservative_window()
{
    for (const bool erase_metadata : {false, true}) {
        FakeStorage storage;
        const std::string first = document_at("2026-08-13T12:00:00Z");
        const std::string too_soon = document_at("2026-08-13T12:31:00Z");
        const std::string due = document_at("2026-08-13T13:01:00Z");
        deck_ai_snapshot_store_t *store = create_store(&storage);
        assert(apply_and_wait(
                   store,
                   first,
                   generated_at(first) + 29U * 60U * 1'000U
               ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        assert(deck_ai_snapshot_store_destroy(store));

        {
            const std::lock_guard<std::mutex> lock(storage.mutex);
            const auto metadata = storage.values.find(
                DECK_TRANSACTION_STORAGE_METADATA
            );
            assert(metadata != storage.values.end());
            if (erase_metadata) {
                storage.values.erase(metadata);
            } else {
                storage.values[DECK_TRANSACTION_STORAGE_METADATA].back() ^= 0x80U;
            }
        }
        store = create_store(&storage);
        const size_t writes_before = write_count(&storage);
        const deck_ai_snapshot_store_update_result_t deferred =
            apply_and_wait(store, too_soon, generated_at(too_soon));
        assert(deferred == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY ||
               deferred == DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE);
        assert(write_count(&storage) == writes_before);
        assert(apply_and_wait(store, due, generated_at(due)) ==
               DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
        assert(write_count(&storage) > writes_before);
        const VisibleSnapshot visible = copy_snapshot(store, generated_at(due), true);
        assert(visible.document == due);
        assert(visible.state.persisted_generation == 2U);
        assert(!visible.state.storage_faulted);
        assert(deck_ai_snapshot_store_destroy(store));
    }
}

void immediate_destroy_drains_the_first_checkpoint()
{
    FakeStorage storage;
    const std::string first = document_at("2026-08-13T12:00:00Z");
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(deck_ai_snapshot_store_apply(
               store,
               first.data(),
               first.size(),
               generated_at(first)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    assert(deck_ai_snapshot_store_destroy(store));

    store = create_store(&storage);
    const VisibleSnapshot restored = copy_snapshot(
        store,
        generated_at(first),
        false
    );
    assert(restored.document == first);
    assert(restored.state.persisted_generation == 1U);
    deck_ai_snapshot_store_destroy(store);
}

void shutdown_is_bounded_when_the_storage_driver_stalls()
{
    FakeStorage storage;
    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_writes = true;
    }
    deck_ai_snapshot_store_t *store = create_store(&storage);
    const std::string document = document_at("2026-08-13T12:00:00Z");
    assert(deck_ai_snapshot_store_apply(
               store,
               document.data(),
               document.size(),
               generated_at(document)
           ) == DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY);
    {
        std::unique_lock<std::mutex> lock(storage.mutex);
        assert(storage.write_condition.wait_for(
            lock,
            std::chrono::seconds(2),
            [&storage]() { return storage.write_started; }
        ));
    }
    const auto started = std::chrono::steady_clock::now();
    assert(!deck_ai_snapshot_store_destroy(store));
    const auto elapsed = std::chrono::steady_clock::now() - started;
    assert(elapsed < std::chrono::seconds(3));

    {
        const std::lock_guard<std::mutex> lock(storage.mutex);
        storage.block_writes = false;
    }
    storage.write_condition.notify_all();
    assert(deck_ai_snapshot_store_destroy(store));
}

}  // namespace

int main()
{
    memory_updates_are_independent_of_flash_throttling();
    invalid_time_and_documents_preserve_the_last_valid_snapshot();
    bounded_transport_skew_is_not_mistaken_for_a_future_snapshot();
    offline_policy_hides_the_document_at_twenty_four_hours();
    nvs_failure_keeps_new_memory_and_old_committed_flash();
    crc_corruption_recovers_previous_and_unknown_storage_schema_fails_closed();
    missing_storage_is_explicit_but_does_not_block_memory();
    any_wall_clock_rollback_hides_until_the_high_water_is_recovered();
    blocked_bootstrap_read_never_blocks_create_apply_or_copy();
    blocked_storage_open_never_blocks_create_apply_or_copy();
    bootstrap_reconciliation_keeps_the_newest_authoritative_document();
    reopen_never_writes_an_obsolete_checkpoint_over_newer_flash();
    live_update_during_reopen_replaces_the_captured_checkpoint();
    slow_flash_never_blocks_memory_publication_or_copy();
    failed_attempt_watermark_survives_restart();
    transient_failure_recovers_after_the_throttle_window();
    successful_reopen_resets_the_attempt_throttle_to_the_actual_write_time();
    untrusted_attempt_watermark_starts_a_conservative_window();
    immediate_destroy_drains_the_first_checkpoint();
    shutdown_is_bounded_when_the_storage_driver_stalls();
    return 0;
}
