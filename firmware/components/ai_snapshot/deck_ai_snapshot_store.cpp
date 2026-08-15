#include "deck_ai_snapshot_store.h"

#include <chrono>
#include <condition_variable>
#include <cstring>
#include <memory>
#include <mutex>
#include <new>
#include <pthread.h>

namespace {

constexpr uint8_t kRecordMagic[4] = {'A', 'I', 'S', '1'};
constexpr uint8_t kMarkerMagic[4] = {'A', 'I', 'M', '1'};
constexpr uint8_t kAttemptMagic[4] = {'A', 'I', 'T', '1'};
constexpr uint8_t kStorageSchemaVersion = 1;
constexpr size_t kAttemptRecordSize = 28;
constexpr size_t kWorkerStackBytes = 8 * 1'024;
constexpr uint32_t kWorkerShutdownMs = 2'000;

enum class ClockObservation : uint8_t {
    valid,
    invalid,
    rollback,
};

bool storage_configured(const deck_transaction_storage_adapter_t &storage)
{
    return storage.read != nullptr && storage.write != nullptr && storage.erase != nullptr;
}

bool storage_empty(const deck_transaction_storage_adapter_t &storage)
{
    return storage.read == nullptr && storage.write == nullptr && storage.erase == nullptr &&
           storage.context == nullptr;
}

bool provider_configured(
    const deck_ai_snapshot_store_storage_provider_t &provider
)
{
    return provider.open != nullptr && provider.close != nullptr;
}

bool provider_empty(const deck_ai_snapshot_store_storage_provider_t &provider)
{
    return provider.open == nullptr && provider.close == nullptr &&
           provider.context == nullptr;
}

bool validate_payload(void *, const uint8_t *payload, size_t size)
{
    return payload != nullptr &&
           deck_ai_snapshot_validate(
               reinterpret_cast<const char *>(payload),
               size,
               nullptr
           ) == DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_store_update_result_t map_validation_result(
    deck_ai_snapshot_result_t result
)
{
    switch (result) {
        case DECK_AI_SNAPSHOT_UNSUPPORTED_VERSION:
            return DECK_AI_SNAPSHOT_STORE_UNSUPPORTED_VERSION;
        case DECK_AI_SNAPSHOT_PRIVATE_DATA:
            return DECK_AI_SNAPSHOT_STORE_PRIVATE_DATA;
        case DECK_AI_SNAPSHOT_MALFORMED:
        default:
            return DECK_AI_SNAPSHOT_STORE_MALFORMED;
    }
}

bool elapsed_at_least(uint64_t newer, uint64_t older, uint64_t interval)
{
    return newer >= older && newer - older >= interval;
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

uint64_t read_u64(const uint8_t *input)
{
    uint64_t value = 0;
    for (unsigned index = 0; index < 8; ++index) {
        value |= static_cast<uint64_t>(input[index]) << (index * 8U);
    }
    return value;
}

void write_u64(uint8_t *output, uint64_t value)
{
    for (unsigned index = 0; index < 8; ++index) {
        output[index] = static_cast<uint8_t>((value >> (index * 8U)) & 0xffU);
    }
}

}  // namespace

struct deck_ai_snapshot_store {
    std::unique_ptr<char[]> document;
    std::unique_ptr<char[]> checkpoint_document;
    size_t document_size = 0;
    deck_ai_snapshot_metadata_t metadata{};
    bool has_snapshot = false;

    deck_transaction_storage_adapter_t storage{};
    deck_transaction_storage_adapter_t configured_storage{};
    deck_ai_snapshot_store_storage_provider_t storage_provider{};
    void *storage_handle = nullptr;
    deck_transaction_store_t *persistent = nullptr;
    uint64_t persisted_generated_at_ms = 0;
    uint64_t last_persist_attempt_utc_ms = 0;
    uint64_t unknown_watermark_started_utc_ms = 0;
    uint32_t persisted_generation = 0;
    deck_transaction_record_status_t record_status = DECK_TRANSACTION_RECORD_EMPTY;
    deck_transaction_record_status_t candidate_record_status =
        DECK_TRANSACTION_RECORD_EMPTY;
    bool storage_missing = false;
    bool storage_ready = false;
    bool storage_faulted = false;
    bool unreported_storage_failure = false;
    bool persistence_needs_reopen = false;
    bool attempt_watermark_unknown = false;
    bool bootstrap_complete = false;

    size_t checkpoint_size = 0;
    uint64_t checkpoint_generated_at_ms = 0;
    uint64_t checkpoint_attempt_utc_ms = 0;
    uint64_t latest_apply_utc_ms = 0;
    bool checkpoint_pending = false;
    bool worker_busy = false;
    bool stop_requested = false;
    bool worker_started = false;
    bool worker_exited = false;
    pthread_t worker{};

    uint64_t apply_utc_high_water_ms = 0;
    bool apply_clock_rollback_latched = false;
    mutable uint64_t copy_utc_high_water_ms = 0;
    mutable bool copy_clock_rollback_latched = false;
    mutable std::mutex mutex;
    std::condition_variable work_ready;
    std::condition_variable worker_idle;
};

namespace {

ClockObservation observe_clock(
    uint64_t *high_water_ms,
    bool *rollback_latched,
    uint64_t trusted_utc_ms
)
{
    if (high_water_ms == nullptr || rollback_latched == nullptr ||
        trusted_utc_ms == 0) {
        return ClockObservation::invalid;
    }
    if (*high_water_ms != 0 && trusted_utc_ms < *high_water_ms) {
        *rollback_latched = true;
        return ClockObservation::rollback;
    }
    *high_water_ms = trusted_utc_ms;
    *rollback_latched = false;
    return ClockObservation::valid;
}

void cache_transaction_status(
    deck_ai_snapshot_store_t *store,
    const deck_transaction_store_snapshot_t &snapshot
)
{
    store->record_status = snapshot.record_status;
    store->candidate_record_status = snapshot.candidate_record_status;
    if (snapshot.storage_faulted) {
        store->storage_faulted = true;
    }
}

deck_transaction_store_t *create_transaction_store(
    const deck_transaction_storage_adapter_t &storage
)
{
    deck_transaction_store_options_t persistent_options{};
    persistent_options.storage = storage;
    std::memcpy(
        persistent_options.record_magic,
        kRecordMagic,
        sizeof(kRecordMagic)
    );
    std::memcpy(
        persistent_options.marker_magic,
        kMarkerMagic,
        sizeof(kMarkerMagic)
    );
    persistent_options.schema_version = kStorageSchemaVersion;
    persistent_options.payload_capacity = DECK_AI_SNAPSHOT_MAX_BYTES;
    persistent_options.validate_payload = validate_payload;
    return deck_transaction_store_create(&persistent_options);
}

bool decode_attempt_record(
    const uint8_t *record,
    size_t size,
    uint64_t *attempt_utc_ms,
    uint64_t *generated_at_ms
)
{
    if (record == nullptr || attempt_utc_ms == nullptr || generated_at_ms == nullptr ||
        size != kAttemptRecordSize || std::memcmp(record, kAttemptMagic, 4) != 0 ||
        record[4] != kStorageSchemaVersion || record[5] != 0 || record[6] != 0 ||
        record[7] != 0 || read_u32(record + 24) != crc32(record, 24)) {
        return false;
    }
    const uint64_t attempt = read_u64(record + 8);
    const uint64_t generated = read_u64(record + 16);
    if (attempt == 0 || generated == 0 || generated > attempt) {
        return false;
    }
    *attempt_utc_ms = attempt;
    *generated_at_ms = generated;
    return true;
}

bool write_attempt_record(
    deck_ai_snapshot_store_t *store,
    uint64_t attempt_utc_ms,
    uint64_t generated_at_ms
)
{
    uint8_t record[kAttemptRecordSize]{};
    std::memcpy(record, kAttemptMagic, sizeof(kAttemptMagic));
    record[4] = kStorageSchemaVersion;
    write_u64(record + 8, attempt_utc_ms);
    write_u64(record + 16, generated_at_ms);
    write_u32(record + 24, crc32(record, 24));
    return store->storage.write(
        store->storage.context,
        DECK_TRANSACTION_STORAGE_METADATA,
        record,
        sizeof(record)
    );
}

struct RecoveredState {
    std::unique_ptr<char[]> document;
    size_t document_size = 0;
    deck_ai_snapshot_metadata_t metadata{};
    bool has_snapshot = false;
    uint64_t persisted_generated_at_ms = 0;
    uint64_t last_persist_attempt_utc_ms = 0;
    bool attempt_watermark_unknown = false;
    uint32_t persisted_generation = 0;
    deck_transaction_record_status_t record_status = DECK_TRANSACTION_RECORD_EMPTY;
    deck_transaction_record_status_t candidate_record_status =
        DECK_TRANSACTION_RECORD_EMPTY;
    bool storage_faulted = false;
};

void load_attempt_record(
    const deck_transaction_storage_adapter_t &storage,
    const deck_transaction_store_snapshot_t &persisted,
    RecoveredState *recovered
)
{
    if (recovered == nullptr) {
        return;
    }
    uint8_t record[kAttemptRecordSize]{};
    size_t size = 0;
    const deck_transaction_storage_result_t result = storage.read(
        storage.context,
        DECK_TRANSACTION_STORAGE_METADATA,
        record,
        sizeof(record),
        &size
    );
    if (result == DECK_TRANSACTION_STORAGE_OK) {
        uint64_t attempt_utc_ms = 0;
        uint64_t generated_at_ms = 0;
        if (!decode_attempt_record(
                record,
                size,
                &attempt_utc_ms,
                &generated_at_ms
            )) {
            recovered->storage_faulted = true;
            recovered->attempt_watermark_unknown = true;
        } else {
            recovered->last_persist_attempt_utc_ms = attempt_utc_ms;
        }
    } else if (result == DECK_TRANSACTION_STORAGE_ERROR) {
        recovered->storage_faulted = true;
        recovered->attempt_watermark_unknown = true;
    } else if (result == DECK_TRANSACTION_STORAGE_NOT_FOUND &&
               (persisted.has_active || persisted.has_candidate)) {
        recovered->attempt_watermark_unknown = true;
    }

    if (!recovered->attempt_watermark_unknown && persisted.has_candidate &&
        persisted.candidate.payload != nullptr) {
        deck_ai_snapshot_metadata_t candidate{};
        if (deck_ai_snapshot_validate(
                reinterpret_cast<const char *>(persisted.candidate.payload),
                persisted.candidate.payload_size,
                &candidate
            ) == DECK_AI_SNAPSHOT_ACCEPTED &&
            candidate.generated_at_unix_ms > recovered->last_persist_attempt_utc_ms) {
            recovered->last_persist_attempt_utc_ms = candidate.generated_at_unix_ms;
        }
    }
}

RecoveredState load_persisted_snapshot(
    const deck_transaction_storage_adapter_t &storage,
    deck_transaction_store_t *persistent
)
{
    RecoveredState recovered{};
    recovered.document.reset(new (std::nothrow) char[DECK_AI_SNAPSHOT_MAX_BYTES]);
    if (persistent == nullptr || recovered.document == nullptr) {
        recovered.storage_faulted = true;
        return recovered;
    }
    deck_transaction_store_snapshot_t persisted{};
    if (!deck_transaction_store_snapshot(persistent, &persisted)) {
        recovered.storage_faulted = true;
        recovered.record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
        recovered.candidate_record_status = DECK_TRANSACTION_RECORD_IO_ERROR;
        return recovered;
    }
    recovered.record_status = persisted.record_status;
    recovered.candidate_record_status = persisted.candidate_record_status;
    recovered.storage_faulted = persisted.storage_faulted;
    load_attempt_record(storage, persisted, &recovered);
    if (!persisted.has_active || persisted.active.payload == nullptr ||
        persisted.active.payload_size > DECK_AI_SNAPSHOT_MAX_BYTES) {
        return recovered;
    }
    deck_ai_snapshot_metadata_t metadata{};
    if (deck_ai_snapshot_validate(
            reinterpret_cast<const char *>(persisted.active.payload),
            persisted.active.payload_size,
            &metadata
        ) != DECK_AI_SNAPSHOT_ACCEPTED) {
        return recovered;
    }
    std::memcpy(
        recovered.document.get(),
        persisted.active.payload,
        persisted.active.payload_size
    );
    recovered.document_size = persisted.active.payload_size;
    recovered.metadata = metadata;
    recovered.has_snapshot = true;
    recovered.persisted_generated_at_ms = metadata.generated_at_unix_ms;
    recovered.persisted_generation = persisted.active.generation;
    return recovered;
}

bool persistence_due(
    const deck_ai_snapshot_store_t *store,
    uint64_t candidate_generated_at_ms,
    uint64_t trusted_utc_ms
)
{
    if ((!store->storage_ready && !store->persistence_needs_reopen) ||
        !store->bootstrap_complete || !store->worker_started ||
        store->checkpoint_pending || store->worker_busy) {
        return false;
    }
    if (store->attempt_watermark_unknown &&
        (store->unknown_watermark_started_utc_ms == 0 ||
         !elapsed_at_least(
             trusted_utc_ms,
             store->unknown_watermark_started_utc_ms,
             DECK_AI_SNAPSHOT_FLASH_INTERVAL_MS
         ))) {
        return false;
    }
    if (store->last_persist_attempt_utc_ms != 0 &&
        !elapsed_at_least(
            trusted_utc_ms,
            store->last_persist_attempt_utc_ms,
            DECK_AI_SNAPSHOT_FLASH_INTERVAL_MS
        )) {
        return false;
    }
    return store->persisted_generated_at_ms == 0 ||
           elapsed_at_least(
               candidate_generated_at_ms,
               store->persisted_generated_at_ms,
               DECK_AI_SNAPSHOT_FLASH_INTERVAL_MS
           );
}

void mark_storage_failure(deck_ai_snapshot_store_t *store)
{
    store->storage_faulted = true;
    store->unreported_storage_failure = true;
    store->persistence_needs_reopen = true;
}

bool queue_current_checkpoint_locked(deck_ai_snapshot_store_t *store)
{
    if (!store->has_snapshot ||
        !persistence_due(
            store,
            store->metadata.generated_at_unix_ms,
            store->latest_apply_utc_ms
        )) {
        return false;
    }
    std::memcpy(
        store->checkpoint_document.get(),
        store->document.get(),
        store->document_size
    );
    store->checkpoint_size = store->document_size;
    store->checkpoint_generated_at_ms = store->metadata.generated_at_unix_ms;
    store->checkpoint_attempt_utc_ms = store->latest_apply_utc_ms;
    store->last_persist_attempt_utc_ms = store->latest_apply_utc_ms;
    store->attempt_watermark_unknown = false;
    store->unknown_watermark_started_utc_ms = 0;
    store->checkpoint_pending = true;
    return true;
}

void close_worker_storage(deck_ai_snapshot_store_t *store)
{
    deck_transaction_store_destroy(store->persistent);
    store->persistent = nullptr;
    if (store->storage_handle != nullptr && store->storage_provider.close != nullptr) {
        store->storage_provider.close(
            store->storage_provider.context,
            store->storage_handle
        );
    }
    store->storage_handle = nullptr;
    store->storage = deck_transaction_storage_adapter_t{};
}

bool open_worker_storage(
    deck_ai_snapshot_store_t *store,
    RecoveredState *recovered
)
{
    close_worker_storage(store);
    deck_transaction_storage_adapter_t storage{};
    void *handle = nullptr;
    if (provider_configured(store->storage_provider)) {
        if (!store->storage_provider.open(
                store->storage_provider.context,
                &storage,
                &handle
            ) || !storage_configured(storage)) {
            if (handle != nullptr) {
                store->storage_provider.close(
                    store->storage_provider.context,
                    handle
                );
            }
            return false;
        }
    } else {
        storage = store->configured_storage;
    }
    deck_transaction_store_t *persistent = create_transaction_store(storage);
    if (persistent == nullptr) {
        if (handle != nullptr) {
            store->storage_provider.close(
                store->storage_provider.context,
                handle
            );
        }
        return false;
    }
    RecoveredState loaded = load_persisted_snapshot(storage, persistent);
    store->storage = storage;
    store->storage_handle = handle;
    store->persistent = persistent;
    if (recovered != nullptr) {
        *recovered = std::move(loaded);
    }
    return true;
}

void publish_recovered_locked(
    deck_ai_snapshot_store_t *store,
    RecoveredState recovered,
    bool opened
)
{
    store->bootstrap_complete = true;
    store->storage_ready = opened;
    store->storage_missing = !opened;
    if (!opened) {
        mark_storage_failure(store);
        store->attempt_watermark_unknown = true;
        store->unknown_watermark_started_utc_ms = store->latest_apply_utc_ms;
        return;
    }
    store->record_status = recovered.record_status;
    store->candidate_record_status = recovered.candidate_record_status;
    store->persisted_generated_at_ms = recovered.persisted_generated_at_ms;
    store->last_persist_attempt_utc_ms = recovered.last_persist_attempt_utc_ms;
    store->attempt_watermark_unknown = recovered.attempt_watermark_unknown;
    store->unknown_watermark_started_utc_ms =
        recovered.attempt_watermark_unknown ? store->latest_apply_utc_ms : 0;
    store->persisted_generation = recovered.persisted_generation;
    store->storage_faulted = recovered.storage_faulted;
    store->persistence_needs_reopen = recovered.storage_faulted;
    if (recovered.storage_faulted) {
        store->unreported_storage_failure = true;
    }
    const bool same_timestamp = recovered.has_snapshot && store->has_snapshot &&
        recovered.metadata.generated_at_unix_ms ==
            store->metadata.generated_at_unix_ms;
    const bool conflicting_same_timestamp = same_timestamp &&
        (recovered.document_size != store->document_size ||
         std::memcmp(
             recovered.document.get(),
             store->document.get(),
             recovered.document_size
         ) != 0);
    const bool recovered_is_authoritative = recovered.has_snapshot &&
        (!store->has_snapshot ||
         recovered.metadata.generated_at_unix_ms >
             store->metadata.generated_at_unix_ms ||
         conflicting_same_timestamp);
    if (recovered_is_authoritative) {
        std::memcpy(
            store->document.get(),
            recovered.document.get(),
            recovered.document_size
        );
        store->document_size = recovered.document_size;
        store->metadata = recovered.metadata;
        store->has_snapshot = true;
    }
}

bool same_document(
    const char *left,
    size_t left_size,
    const char *right,
    size_t right_size
)
{
    return left_size == right_size &&
           (left_size == 0 || std::memcmp(left, right, left_size) == 0);
}

bool recovered_supersedes_checkpoint(
    const RecoveredState &recovered,
    uint64_t checkpoint_generated_at_ms
)
{
    return recovered.has_snapshot &&
           recovered.metadata.generated_at_unix_ms >= checkpoint_generated_at_ms;
}

bool live_supersedes_checkpoint_locked(
    const deck_ai_snapshot_store_t *store,
    uint64_t checkpoint_generated_at_ms,
    const char *checkpoint,
    size_t checkpoint_size
)
{
    return store->has_snapshot &&
        (store->metadata.generated_at_unix_ms > checkpoint_generated_at_ms ||
         (store->metadata.generated_at_unix_ms == checkpoint_generated_at_ms &&
          !same_document(
              store->document.get(),
              store->document_size,
              checkpoint,
              checkpoint_size
          )));
}

void *persistence_worker(void *argument)
{
    auto *store = static_cast<deck_ai_snapshot_store_t *>(argument);
    RecoveredState recovered{};
    const bool opened = open_worker_storage(store, &recovered);
    std::unique_lock<std::mutex> lock(store->mutex);
    publish_recovered_locked(store, std::move(recovered), opened);
    store->worker_busy = false;
    if (queue_current_checkpoint_locked(store)) {
        store->work_ready.notify_one();
    }
    store->worker_idle.notify_all();
    while (true) {
        store->work_ready.wait(lock, [store]() {
            return store->stop_requested || store->checkpoint_pending;
        });
        if (store->stop_requested && !store->checkpoint_pending) {
            break;
        }
        const size_t checkpoint_size = store->checkpoint_size;
        const uint64_t generated_at_ms = store->checkpoint_generated_at_ms;
        const uint64_t attempt_utc_ms = store->checkpoint_attempt_utc_ms;
        const bool reopen_persistence = store->persistence_needs_reopen;
        store->checkpoint_pending = false;
        store->worker_busy = true;
        lock.unlock();

        bool persisted_ok = true;
        bool checkpoint_obsolete = false;
        if (reopen_persistence) {
            RecoveredState reopened{};
            persisted_ok = open_worker_storage(store, &reopened);
            checkpoint_obsolete = persisted_ok &&
                recovered_supersedes_checkpoint(reopened, generated_at_ms);
            lock.lock();
            publish_recovered_locked(store, std::move(reopened), persisted_ok);
            checkpoint_obsolete = checkpoint_obsolete ||
                live_supersedes_checkpoint_locked(
                    store,
                    generated_at_ms,
                    store->checkpoint_document.get(),
                    checkpoint_size
                );
            if (checkpoint_obsolete) {
                store->worker_busy = false;
                const bool queued_latest = queue_current_checkpoint_locked(store);
                store->worker_idle.notify_all();
                if (queued_latest) {
                    store->work_ready.notify_one();
                }
                continue;
            }
            lock.unlock();
        }
        if (persisted_ok && store->persistent != nullptr) {
            persisted_ok = write_attempt_record(
                store,
                attempt_utc_ms,
                generated_at_ms
            );
        }
        if (persisted_ok) {
            const auto *bytes = reinterpret_cast<const uint8_t *>(
                store->checkpoint_document.get()
            );
            persisted_ok =
                deck_transaction_store_stage(
                    store->persistent,
                    bytes,
                    checkpoint_size
                ) == DECK_TRANSACTION_UPDATED &&
                deck_transaction_store_commit(store->persistent) ==
                    DECK_TRANSACTION_UPDATED;
        }
        deck_transaction_store_snapshot_t persisted{};
        const bool has_status = store->persistent != nullptr &&
            deck_transaction_store_snapshot(store->persistent, &persisted);

        lock.lock();
        if (has_status) {
            cache_transaction_status(store, persisted);
        }
        if (persisted_ok && has_status && persisted.has_active) {
            store->persisted_generated_at_ms = generated_at_ms;
            store->last_persist_attempt_utc_ms = attempt_utc_ms;
            store->persisted_generation = persisted.active.generation;
            store->storage_faulted = false;
            store->persistence_needs_reopen = false;
            store->attempt_watermark_unknown = false;
            store->unknown_watermark_started_utc_ms = 0;
        } else {
            mark_storage_failure(store);
        }
        store->worker_busy = false;
        store->worker_idle.notify_all();
    }
    lock.unlock();
    close_worker_storage(store);
    lock.lock();
    store->storage_ready = false;
    store->worker_busy = false;
    store->worker_exited = true;
    store->worker_idle.notify_all();
    return nullptr;
}

bool start_worker(deck_ai_snapshot_store_t *store)
{
    pthread_attr_t attributes{};
    if (pthread_attr_init(&attributes) != 0) {
        return false;
    }
    (void)pthread_attr_setstacksize(&attributes, kWorkerStackBytes);
    store->worker_busy = true;
    const int result = pthread_create(
        &store->worker,
        &attributes,
        persistence_worker,
        store
    );
    (void)pthread_attr_destroy(&attributes);
    store->worker_started = result == 0;
    if (!store->worker_started) {
        store->worker_busy = false;
    }
    return store->worker_started;
}

}  // namespace

deck_ai_snapshot_store_t *deck_ai_snapshot_store_create(
    const deck_ai_snapshot_store_options_t *options
)
{
    auto *store = new (std::nothrow) deck_ai_snapshot_store_t{};
    if (store == nullptr) {
        return nullptr;
    }
    store->document.reset(new (std::nothrow) char[DECK_AI_SNAPSHOT_MAX_BYTES]);
    store->checkpoint_document.reset(
        new (std::nothrow) char[DECK_AI_SNAPSHOT_MAX_BYTES]
    );
    if (store->document == nullptr || store->checkpoint_document == nullptr) {
        delete store;
        return nullptr;
    }
    if (options == nullptr ||
        (storage_empty(options->storage) && provider_empty(options->provider))) {
        store->storage_missing = true;
        return store;
    }
    const bool direct_storage = storage_configured(options->storage) &&
                                provider_empty(options->provider);
    const bool delayed_storage = storage_empty(options->storage) &&
                                 provider_configured(options->provider);
    if (!direct_storage && !delayed_storage) {
        delete store;
        return nullptr;
    }
    store->configured_storage = options->storage;
    store->storage_provider = options->provider;
    if (!start_worker(store)) {
        store->storage_missing = true;
        store->storage_faulted = true;
    }
    return store;
}

bool deck_ai_snapshot_store_destroy(deck_ai_snapshot_store_t *store)
{
    if (store == nullptr) {
        return true;
    }
    if (store->worker_started) {
        {
            std::unique_lock<std::mutex> lock(store->mutex);
            store->stop_requested = true;
            store->work_ready.notify_one();
            if (!store->worker_idle.wait_for(
                    lock,
                    std::chrono::milliseconds(kWorkerShutdownMs),
                    [store]() { return store->worker_exited; }
                )) {
                return false;
            }
        }
        (void)pthread_join(store->worker, nullptr);
        store->worker_started = false;
    }
    if (store->document != nullptr) {
        std::memset(store->document.get(), 0, DECK_AI_SNAPSHOT_MAX_BYTES);
    }
    if (store->checkpoint_document != nullptr) {
        std::memset(store->checkpoint_document.get(), 0, DECK_AI_SNAPSHOT_MAX_BYTES);
    }
    delete store;
    return true;
}

deck_ai_snapshot_store_update_result_t deck_ai_snapshot_store_apply(
    deck_ai_snapshot_store_t *store,
    const char *document,
    size_t document_size,
    uint64_t trusted_utc_ms
)
{
    if (store == nullptr) {
        return DECK_AI_SNAPSHOT_STORE_MALFORMED;
    }
    deck_ai_snapshot_metadata_t candidate{};
    const deck_ai_snapshot_result_t validation =
        deck_ai_snapshot_validate(document, document_size, &candidate);
    if (validation != DECK_AI_SNAPSHOT_ACCEPTED) {
        return map_validation_result(validation);
    }

    std::unique_lock<std::mutex> lock(store->mutex);
    const ClockObservation clock = observe_clock(
        &store->apply_utc_high_water_ms,
        &store->apply_clock_rollback_latched,
        trusted_utc_ms
    );
    if (clock == ClockObservation::invalid) {
        return DECK_AI_SNAPSHOT_STORE_INVALID_TIME;
    }
    if (clock == ClockObservation::rollback) {
        return DECK_AI_SNAPSHOT_STORE_TIME_ROLLBACK;
    }
    if (candidate.generated_at_unix_ms > trusted_utc_ms) {
        return DECK_AI_SNAPSHOT_STORE_INVALID_TIME;
    }
    if (store->has_snapshot &&
        candidate.generated_at_unix_ms < store->metadata.generated_at_unix_ms) {
        return DECK_AI_SNAPSHOT_STORE_TIME_ROLLBACK;
    }
    if (store->has_snapshot &&
        candidate.generated_at_unix_ms == store->metadata.generated_at_unix_ms) {
        return document_size == store->document_size &&
                       std::memcmp(document, store->document.get(), document_size) == 0
                   ? DECK_AI_SNAPSHOT_STORE_UNCHANGED
                   : DECK_AI_SNAPSHOT_STORE_TIME_ROLLBACK;
    }

    std::memcpy(store->document.get(), document, document_size);
    if (document_size < DECK_AI_SNAPSHOT_MAX_BYTES) {
        store->document[document_size] = '\0';
    }
    store->document_size = document_size;
    store->metadata = candidate;
    store->has_snapshot = true;
    store->latest_apply_utc_ms = trusted_utc_ms;
    if (store->attempt_watermark_unknown &&
        store->unknown_watermark_started_utc_ms == 0) {
        store->unknown_watermark_started_utc_ms = trusted_utc_ms;
    }

    if (!queue_current_checkpoint_locked(store)) {
        return !store->worker_started ||
                       (store->bootstrap_complete &&
                        (!store->storage_ready || store->storage_missing ||
                         store->storage_faulted))
                   ? DECK_AI_SNAPSHOT_STORE_ACCEPTED_STORAGE_FAILURE
                   : DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY;
    }
    lock.unlock();
    store->work_ready.notify_one();
    return DECK_AI_SNAPSHOT_STORE_ACCEPTED_MEMORY;
}

bool deck_ai_snapshot_store_wait_for_idle(
    deck_ai_snapshot_store_t *store,
    uint32_t timeout_ms
)
{
    if (store == nullptr) {
        return false;
    }
    std::unique_lock<std::mutex> lock(store->mutex);
    return store->worker_idle.wait_for(
        lock,
        std::chrono::milliseconds(timeout_ms),
        [store]() { return !store->checkpoint_pending && !store->worker_busy; }
    );
}

bool deck_ai_snapshot_store_take_storage_failure(
    deck_ai_snapshot_store_t *store
)
{
    if (store == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(store->mutex);
    const bool failed = store->unreported_storage_failure;
    store->unreported_storage_failure = false;
    return failed;
}

bool deck_ai_snapshot_store_copy(
    const deck_ai_snapshot_store_t *store,
    uint64_t now_utc_ms,
    bool companion_online,
    char *document,
    size_t document_capacity,
    size_t *document_size,
    deck_ai_snapshot_store_snapshot_t *snapshot
)
{
    if (store == nullptr || document_size == nullptr || snapshot == nullptr) {
        return false;
    }
    const std::lock_guard<std::mutex> lock(store->mutex);
    *snapshot = deck_ai_snapshot_store_snapshot_t{};
    *document_size = 0;
    snapshot->has_snapshot = store->has_snapshot;
    snapshot->persisted_generation = store->persisted_generation;
    snapshot->metadata = store->metadata;
    snapshot->record_status = store->record_status;
    snapshot->candidate_record_status = store->candidate_record_status;
    snapshot->storage_faulted = store->storage_faulted || store->storage_missing;
    if (!store->has_snapshot) {
        snapshot->state = DECK_AI_SNAPSHOT_STORE_EMPTY;
        return true;
    }

    const ClockObservation clock = observe_clock(
        &store->copy_utc_high_water_ms,
        &store->copy_clock_rollback_latched,
        now_utc_ms
    );
    if (clock == ClockObservation::valid &&
        !store->apply_clock_rollback_latched &&
        !store->copy_clock_rollback_latched &&
        now_utc_ms >= store->metadata.generated_at_unix_ms &&
        now_utc_ms - store->metadata.generated_at_unix_ms <
            DECK_AI_SNAPSHOT_OFFLINE_LIMIT_MS) {
        snapshot->state = companion_online ? DECK_AI_SNAPSHOT_STORE_FRESH
                                           : DECK_AI_SNAPSHOT_STORE_STALE;
    } else {
        snapshot->state = DECK_AI_SNAPSHOT_STORE_UNAVAILABLE;
    }
    snapshot->document_visible =
        snapshot->state == DECK_AI_SNAPSHOT_STORE_FRESH ||
        snapshot->state == DECK_AI_SNAPSHOT_STORE_STALE;
    snapshot->quotas_visible = snapshot->document_visible;
    if (!snapshot->document_visible) {
        return true;
    }
    if (document == nullptr || document_capacity < store->document_size) {
        return false;
    }
    std::memcpy(document, store->document.get(), store->document_size);
    *document_size = store->document_size;
    snapshot->document_size = store->document_size;
    return true;
}
