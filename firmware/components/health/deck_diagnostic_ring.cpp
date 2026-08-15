#include "deck_diagnostic_ring.h"

#include <cstdarg>
#include <cstdio>
#include <mutex>

namespace {

std::mutex ring_mutex;
deck_diagnostic_event_t ring_events[DECK_DIAGNOSTIC_RING_CAPACITY]{};
size_t ring_start = 0;
size_t ring_count = 0;
uint32_t ring_dropped = 0;

bool valid_level(deck_diagnostic_level_t level)
{
    return level >= DECK_DIAGNOSTIC_LEVEL_INFO &&
           level <= DECK_DIAGNOSTIC_LEVEL_ERROR;
}

bool valid_component(deck_diagnostic_component_t component)
{
    return component >= DECK_DIAGNOSTIC_COMPONENT_SYSTEM &&
           component <= DECK_DIAGNOSTIC_COMPONENT_OTA;
}

bool valid_code(deck_diagnostic_code_t code)
{
    return code >= DECK_DIAGNOSTIC_CODE_BOOT &&
           code <= DECK_DIAGNOSTIC_CODE_QUEUE_OVERFLOW;
}

bool append_json(
    char *buffer,
    size_t capacity,
    size_t *size,
    const char *format,
    ...
)
{
    if (buffer == nullptr || size == nullptr || *size >= capacity) {
        return false;
    }
    va_list arguments;
    va_start(arguments, format);
    const int appended = std::vsnprintf(
        buffer + *size,
        capacity - *size,
        format,
        arguments
    );
    va_end(arguments);
    if (appended < 0 || static_cast<size_t>(appended) >= capacity - *size) {
        return false;
    }
    *size += static_cast<size_t>(appended);
    return true;
}

}  // namespace

void deck_diagnostic_ring_reset(void)
{
    std::lock_guard<std::mutex> lock(ring_mutex);
    for (auto &event : ring_events) {
        event = {};
    }
    ring_start = 0;
    ring_count = 0;
    ring_dropped = 0;
}

bool deck_diagnostic_ring_record(
    uint64_t monotonic_ms,
    deck_diagnostic_level_t level,
    deck_diagnostic_component_t component,
    deck_diagnostic_code_t code,
    uint32_t value
)
{
    if (!valid_level(level) || !valid_component(component) || !valid_code(code)) {
        return false;
    }
    std::lock_guard<std::mutex> lock(ring_mutex);
    if (ring_count != 0) {
        const size_t last_index =
            (ring_start + ring_count - 1U) % DECK_DIAGNOSTIC_RING_CAPACITY;
        if (monotonic_ms < ring_events[last_index].monotonic_ms) {
            monotonic_ms = ring_events[last_index].monotonic_ms;
        }
    }
    const size_t index = (ring_start + ring_count) % DECK_DIAGNOSTIC_RING_CAPACITY;
    ring_events[index] = {monotonic_ms, level, component, code, value};
    if (ring_count < DECK_DIAGNOSTIC_RING_CAPACITY) {
        ++ring_count;
    } else {
        ring_start = (ring_start + 1U) % DECK_DIAGNOSTIC_RING_CAPACITY;
        if (ring_dropped != UINT32_MAX) {
            ++ring_dropped;
        }
    }
    return true;
}

void deck_diagnostic_ring_snapshot(deck_diagnostic_snapshot_t *snapshot)
{
    if (snapshot == nullptr) {
        return;
    }
    std::lock_guard<std::mutex> lock(ring_mutex);
    *snapshot = {};
    snapshot->count = ring_count;
    snapshot->dropped = ring_dropped;
    for (size_t index = 0; index < ring_count; ++index) {
        snapshot->events[index] =
            ring_events[(ring_start + index) % DECK_DIAGNOSTIC_RING_CAPACITY];
    }
}

const char *deck_diagnostic_level_name(deck_diagnostic_level_t level)
{
    constexpr const char *names[] = {"info", "warning", "error"};
    return valid_level(level) ? names[static_cast<unsigned>(level)] : nullptr;
}

const char *deck_diagnostic_component_name(deck_diagnostic_component_t component)
{
    constexpr const char *names[] = {
        "system", "display", "wifi", "setup", "sensor", "device_link", "serial", "ota",
    };
    return valid_component(component) ? names[static_cast<unsigned>(component)] : nullptr;
}

const char *deck_diagnostic_code_name(deck_diagnostic_code_t code)
{
    constexpr const char *names[] = {
        "boot", "ready", "unavailable", "connected", "disconnected",
        "storage_error", "protocol_error", "timeout", "owner_changed",
        "update_started", "update_failed", "rollback", "queue_overflow",
    };
    return valid_code(code) ? names[static_cast<unsigned>(code)] : nullptr;
}

bool deck_diagnostic_snapshot_format(
    const deck_diagnostic_snapshot_t *snapshot,
    uint64_t request_id,
    char *output,
    size_t capacity,
    size_t *output_size
)
{
    if (snapshot == nullptr || request_id == 0 || output == nullptr ||
        capacity == 0 || output_size == nullptr ||
        snapshot->count > DECK_DIAGNOSTIC_RING_CAPACITY) {
        return false;
    }
    size_t size = 0;
    if (!append_json(
            output,
            capacity,
            &size,
            "{\"type\":\"diagnostics.snapshot\",\"protocol_version\":1,"
            "\"request_id\":%llu,\"dropped\":%u,\"events\":[",
            static_cast<unsigned long long>(request_id),
            static_cast<unsigned>(snapshot->dropped)
        )) {
        return false;
    }
    uint64_t previous = 0;
    for (size_t index = 0; index < snapshot->count; ++index) {
        const deck_diagnostic_event_t &event = snapshot->events[index];
        const char *level = deck_diagnostic_level_name(event.level);
        const char *component = deck_diagnostic_component_name(event.component);
        const char *code = deck_diagnostic_code_name(event.code);
        if (level == nullptr || component == nullptr || code == nullptr ||
            (index != 0 && event.monotonic_ms < previous) ||
            !append_json(
                output,
                capacity,
                &size,
                "%s{\"monotonic_ms\":%llu,\"level\":\"%s\","
                "\"component\":\"%s\",\"code\":\"%s\",\"value\":%u}",
                index == 0 ? "" : ",",
                static_cast<unsigned long long>(event.monotonic_ms),
                level,
                component,
                code,
                static_cast<unsigned>(event.value)
            )) {
            return false;
        }
        previous = event.monotonic_ms;
    }
    if (!append_json(output, capacity, &size, "]}")) {
        return false;
    }
    *output_size = size;
    return true;
}
