#include "deck_companion_link_message.h"
#include "deck_companion_link_frame.h"

#include <algorithm>
#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <iterator>
#include <map>
#include <string>
#include <vector>

namespace {

std::string read_file(const std::string &path)
{
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    return std::string(
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    );
}

struct FakeStorage {
    std::map<deck_transaction_storage_key_t, std::vector<uint8_t>> values;
};

deck_transaction_storage_result_t read_storage(
    void *context,
    deck_transaction_storage_key_t key,
    uint8_t *output,
    size_t capacity,
    size_t *size
)
{
    const auto &values = static_cast<FakeStorage *>(context)->values;
    const auto value = values.find(key);
    if (value == values.end()) {
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
    if (data == nullptr || size == 0) {
        return false;
    }
    static_cast<FakeStorage *>(context)->values[key] =
        std::vector<uint8_t>(data, data + size);
    return true;
}

bool erase_storage(void *context, deck_transaction_storage_key_t key)
{
    static_cast<FakeStorage *>(context)->values.erase(key);
    return true;
}

deck_ai_snapshot_store_t *create_store(FakeStorage *storage)
{
    deck_ai_snapshot_store_options_t options{};
    options.storage = {read_storage, write_storage, erase_storage, storage};
    return deck_ai_snapshot_store_create(&options);
}

std::string snapshot_at(const char *timestamp)
{
    return std::string(
               "{\"type\":\"snapshot.ai\",\"protocol_version\":1,"
               "\"schema_version\":{\"major\":1,\"minor\":0},\"generated_at\":\""
           ) +
           timestamp +
           "\",\"timezone\":null,\"provider_order\":[],\"providers\":[],"
           "\"sessions\":[],\"next_refresh_seconds\":5}";
}

uint64_t timestamp(const std::string &snapshot)
{
    deck_ai_snapshot_metadata_t metadata{};
    assert(deck_ai_snapshot_validate(
               snapshot.data(), snapshot.size(), &metadata
           ) == DECK_AI_SNAPSHOT_ACCEPTED);
    return metadata.generated_at_unix_ms;
}

void heartbeat_and_snapshot_share_one_fail_closed_dispatch()
{
    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    assert(store != nullptr);
    deck_device_heartbeat_t heartbeat{};
    constexpr char heartbeat_message[] =
        "{\"type\":\"device.heartbeat\",\"protocol_version\":1,"
        "\"utc\":\"2026-08-13T12:00:00Z\",\"monotonic_ms\":42,"
        "\"tx_queue_depth\":0,\"tx_queue_capacity\":1,"
        "\"rx_queue_depth\":0,\"rx_queue_capacity\":1}";
    assert(deck_companion_link_accept_server_message(
               store,
               heartbeat_message,
               sizeof(heartbeat_message) - 1U,
               0,
               0,
               false,
               &heartbeat
           ) == DECK_COMPANION_SERVER_HEARTBEAT);
    assert(heartbeat.monotonic_ms == 42U);

    const std::string accepted = snapshot_at("2026-08-13T12:00:00Z");
    assert(deck_companion_link_accept_server_message(
               store,
               accepted.data(),
               accepted.size(),
               timestamp(accepted),
               heartbeat.monotonic_ms,
               true,
               &heartbeat
           ) == DECK_COMPANION_SERVER_AI_SNAPSHOT);

    const std::string rejected = snapshot_at("2026-08-13T12:01:00Z");
    assert(deck_companion_link_accept_server_message(
               store,
               rejected.data(),
               rejected.size(),
               timestamp(accepted),
               heartbeat.monotonic_ms,
               true,
               &heartbeat
           ) == DECK_COMPANION_SERVER_INVALID_MESSAGE);

    std::array<char, DECK_AI_SNAPSHOT_MAX_BYTES> copy{};
    size_t copy_size = 0;
    deck_ai_snapshot_store_snapshot_t state{};
    assert(deck_ai_snapshot_store_copy(
        store,
        timestamp(accepted),
        true,
        copy.data(),
        copy.size(),
        &copy_size,
        &state
    ));
    assert(std::string(copy.data(), copy_size) == accepted);
    deck_ai_snapshot_store_destroy(store);
}

void production_frame_reassembly_accepts_large_and_continuation_messages()
{
    std::array<char, DECK_AI_SNAPSHOT_MAX_BYTES> buffer{};
    deck_companion_link_frame_t frame{};
    deck_companion_link_frame_init(&frame, buffer.data(), buffer.size());

    const std::string large = read_file(
        std::string(DECK_REPOSITORY_ROOT) +
        "/protocol/fixtures/ai-snapshot-v1/valid-full.json"
    );
    assert(large.size() > 1'024U);
    for (size_t offset = 0; offset < large.size(); offset += 1'024U) {
        const size_t size = std::min<size_t>(1'024U, large.size() - offset);
        const auto result = deck_companion_link_frame_accept(
            &frame,
            static_cast<int>(large.size()),
            static_cast<int>(offset),
            1,
            true,
            large.data() + offset,
            size
        );
        assert(result == (offset + size == large.size()
                              ? DECK_COMPANION_LINK_FRAME_COMPLETE
                              : DECK_COMPANION_LINK_FRAME_PARTIAL));
    }
    assert(frame.message_size == large.size());
    assert(std::string(buffer.data(), frame.message_size) == large);

    FakeStorage storage;
    deck_ai_snapshot_store_t *store = create_store(&storage);
    deck_ai_snapshot_metadata_t metadata{};
    assert(deck_ai_snapshot_validate(
               buffer.data(), frame.message_size, &metadata
           ) == DECK_AI_SNAPSHOT_ACCEPTED);
    deck_device_heartbeat_t heartbeat{};
    assert(deck_companion_link_accept_server_message(
               store,
               buffer.data(),
               frame.message_size,
               metadata.generated_at_unix_ms,
               42,
               true,
               &heartbeat
           ) == DECK_COMPANION_SERVER_AI_SNAPSHOT);
    deck_ai_snapshot_store_destroy(store);

    deck_companion_link_frame_reset(&frame);
    const std::string near_limit(DECK_AI_SNAPSHOT_MAX_BYTES - 1U, 'z');
    const size_t first_frame_size = 7'001U;
    for (size_t offset = 0; offset < first_frame_size; offset += 1'024U) {
        const size_t size = std::min<size_t>(1'024U, first_frame_size - offset);
        assert(deck_companion_link_frame_accept(
                   &frame,
                   static_cast<int>(first_frame_size),
                   static_cast<int>(offset),
                   1,
                   false,
                   near_limit.data() + offset,
                   size
               ) == DECK_COMPANION_LINK_FRAME_PARTIAL);
    }
    const size_t second_frame_size = near_limit.size() - first_frame_size;
    for (size_t offset = 0; offset < second_frame_size; offset += 1'024U) {
        const size_t size = std::min<size_t>(1'024U, second_frame_size - offset);
        const auto result = deck_companion_link_frame_accept(
            &frame,
            static_cast<int>(second_frame_size),
            static_cast<int>(offset),
            0,
            true,
            near_limit.data() + first_frame_size + offset,
            size
        );
        assert(result == (offset + size == second_frame_size
                              ? DECK_COMPANION_LINK_FRAME_COMPLETE
                              : DECK_COMPANION_LINK_FRAME_PARTIAL));
    }
    assert(frame.message_size == near_limit.size());
    assert(std::string(buffer.data(), frame.message_size) == near_limit);
}

}  // namespace

int main()
{
    heartbeat_and_snapshot_share_one_fail_closed_dispatch();
    production_frame_reassembly_accepts_large_and_continuation_messages();
    return 0;
}
