#include "deck_display.h"

#include <atomic>
#include <cstring>
#include <new>
#include <stddef.h>
#include <stdint.h>

#ifdef ESP_PLATFORM
#include "esp_heap_caps.h"
#endif

enum class TransferKind : uint8_t {
    none,
    candidate,
    recovery,
};

struct deck_display_service {
    deck_display_panel_adapter_t adapter;
    uint8_t *working_frame;
    uint8_t *successful_frame;
    uint32_t timeout_ms;
    uint64_t transfer_started_ms;
    std::atomic_bool transfer_completed;
    TransferKind transfer_kind;
    bool dirty;
    bool in_flight;
    bool timeout_reported;
    bool has_successful_frame;
    deck_display_metrics_t metrics;
};

namespace {

uint8_t *allocate_frame()
{
#ifdef ESP_PLATFORM
    return static_cast<uint8_t *>(
        heap_caps_malloc(DECK_DISPLAY_FRAME_BYTES, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL)
    );
#else
    return new (std::nothrow) uint8_t[DECK_DISPLAY_FRAME_BYTES];
#endif
}

void free_frame(uint8_t *frame)
{
#ifdef ESP_PLATFORM
    heap_caps_free(frame);
#else
    delete[] frame;
#endif
}

void transfer_done(void *context)
{
    auto *display = static_cast<deck_display_service_t *>(context);
    display->transfer_completed.store(true, std::memory_order_release);
}

bool start_transfer(
    deck_display_service_t *display,
    const uint8_t *frame,
    TransferKind kind,
    uint64_t now_ms
)
{
    display->transfer_completed.store(false, std::memory_order_relaxed);
    if (!display->adapter.start_transfer(
            display->adapter.context,
            frame,
            DECK_DISPLAY_FRAME_BYTES,
            transfer_done,
            display
        )) {
        ++display->metrics.start_failures;
        return false;
    }

    display->transfer_kind = kind;
    display->in_flight = true;
    display->timeout_reported = false;
    display->transfer_started_ms = now_ms;
    ++display->metrics.submitted_frames;
    if (kind == TransferKind::recovery) {
        ++display->metrics.recovery_submissions;
    }
    return true;
}

}  // namespace

deck_display_pack_result_t deck_display_pack_rgb565(
    uint8_t *frame,
    size_t frame_size,
    deck_display_area_t area,
    const uint16_t *pixels,
    size_t pixel_count
)
{
    if (frame == nullptr || pixels == nullptr || frame_size != DECK_DISPLAY_FRAME_BYTES || area.x1 < 0 ||
        area.y1 < 0 || area.x2 < area.x1 || area.y2 < area.y1 || area.x2 >= DECK_DISPLAY_WIDTH ||
        area.y2 >= DECK_DISPLAY_HEIGHT) {
        return DECK_DISPLAY_PACK_INVALID_ARGUMENT;
    }

    const size_t area_width = static_cast<size_t>(area.x2 - area.x1 + 1);
    const size_t area_height = static_cast<size_t>(area.y2 - area.y1 + 1);
    if (pixel_count != area_width * area_height) {
        return DECK_DISPLAY_PACK_INVALID_ARGUMENT;
    }

    bool changed = false;
    size_t pixel_index = 0;
    for (int32_t y = area.y1; y <= area.y2; ++y) {
        for (int32_t x = area.x1; x <= area.x2; ++x) {
            const int32_t inverted_y = DECK_DISPLAY_HEIGHT - 1 - y;
            const size_t byte_x = static_cast<size_t>(x / 2);
            const size_t block_y = static_cast<size_t>(inverted_y / 4);
            const size_t frame_index = byte_x * static_cast<size_t>(DECK_DISPLAY_HEIGHT / 4) + block_y;
            const uint8_t local_x = static_cast<uint8_t>(x % 2);
            const uint8_t local_y = static_cast<uint8_t>(inverted_y % 4);
            const uint8_t bit = static_cast<uint8_t>(7U - (static_cast<unsigned>(local_y) * 2U + local_x));
            const uint8_t mask = static_cast<uint8_t>(1U << bit);
            const bool white = pixels[pixel_index] >= 0x7fffU;
            const uint8_t previous = frame[frame_index];
            frame[frame_index] = white ? static_cast<uint8_t>(previous | mask)
                                       : static_cast<uint8_t>(previous & static_cast<uint8_t>(~mask));
            changed = changed || frame[frame_index] != previous;
            ++pixel_index;
        }
    }

    return changed ? DECK_DISPLAY_PACK_CHANGED : DECK_DISPLAY_PACK_UNCHANGED;
}

deck_display_service_t *deck_display_service_create(
    deck_display_panel_adapter_t adapter,
    uint32_t transfer_timeout_ms
)
{
    if (adapter.start_transfer == nullptr || transfer_timeout_ms == 0) {
        return nullptr;
    }

    auto *display = new (std::nothrow) deck_display_service_t{};
    if (display == nullptr) {
        return nullptr;
    }
    display->adapter = adapter;
    display->timeout_ms = transfer_timeout_ms;
    display->working_frame = allocate_frame();
    display->successful_frame = allocate_frame();
    if (display->working_frame == nullptr || display->successful_frame == nullptr) {
        (void)deck_display_service_destroy(display);
        return nullptr;
    }
    std::memset(display->working_frame, 0xff, DECK_DISPLAY_FRAME_BYTES);
    std::memset(display->successful_frame, 0xff, DECK_DISPLAY_FRAME_BYTES);
    return display;
}

bool deck_display_service_destroy(deck_display_service_t *display)
{
    if (display == nullptr) {
        return true;
    }
    if (display->in_flight) {
        return false;
    }
    free_frame(display->working_frame);
    free_frame(display->successful_frame);
    delete display;
    return true;
}

deck_display_result_t deck_display_service_update(
    deck_display_service_t *display,
    deck_display_area_t area,
    const uint16_t *pixels,
    size_t pixel_count
)
{
    if (display == nullptr) {
        return DECK_DISPLAY_INVALID_ARGUMENT;
    }
    if (display->in_flight && display->transfer_kind == TransferKind::candidate) {
        return DECK_DISPLAY_IN_FLIGHT;
    }
    const deck_display_pack_result_t result =
        deck_display_pack_rgb565(display->working_frame, DECK_DISPLAY_FRAME_BYTES, area, pixels, pixel_count);
    if (result == DECK_DISPLAY_PACK_INVALID_ARGUMENT) {
        ++display->metrics.rejected_updates;
        return DECK_DISPLAY_INVALID_ARGUMENT;
    }
    if (result == DECK_DISPLAY_PACK_UNCHANGED) {
        return DECK_DISPLAY_UNCHANGED;
    }

    display->dirty = true;
    return DECK_DISPLAY_UPDATED;
}

deck_display_result_t deck_display_service_submit(deck_display_service_t *display, uint64_t now_ms)
{
    if (display == nullptr) {
        return DECK_DISPLAY_INVALID_ARGUMENT;
    }
    if (display->in_flight) {
        ++display->metrics.coalesced_submissions;
        return DECK_DISPLAY_IN_FLIGHT;
    }
    if (!display->dirty) {
        return DECK_DISPLAY_UNCHANGED;
    }

    if (!start_transfer(display, display->working_frame, TransferKind::candidate, now_ms)) {
        return DECK_DISPLAY_START_FAILED;
    }
    display->dirty = false;
    return DECK_DISPLAY_SUBMITTED;
}

deck_display_result_t deck_display_service_poll(deck_display_service_t *display, uint64_t now_ms)
{
    if (display == nullptr) {
        return DECK_DISPLAY_INVALID_ARGUMENT;
    }
    if (!display->in_flight) {
        return DECK_DISPLAY_UNCHANGED;
    }
    if (display->transfer_completed.exchange(false, std::memory_order_acq_rel)) {
        const TransferKind completed_kind = display->transfer_kind;
        const bool completed_after_timeout = display->timeout_reported;
        display->in_flight = false;
        display->transfer_kind = TransferKind::none;
        display->timeout_reported = false;
        ++display->metrics.completed_frames;
        if (completed_kind == TransferKind::recovery) {
            ++display->metrics.recovered_frames;
            return DECK_DISPLAY_RECOVERED;
        }
        if (completed_after_timeout) {
            display->dirty = true;
            if (!start_transfer(
                    display,
                    display->successful_frame,
                    TransferKind::recovery,
                    now_ms
                )) {
                return DECK_DISPLAY_START_FAILED;
            }
            return DECK_DISPLAY_RECOVERING;
        }
        std::memcpy(display->successful_frame, display->working_frame, DECK_DISPLAY_FRAME_BYTES);
        display->has_successful_frame = true;
        return DECK_DISPLAY_COMPLETED;
    }
    if (!display->timeout_reported && now_ms - display->transfer_started_ms >= display->timeout_ms) {
        display->timeout_reported = true;
        ++display->metrics.transfer_timeouts;
        return DECK_DISPLAY_TIMED_OUT;
    }
    return DECK_DISPLAY_IN_FLIGHT;
}

deck_display_metrics_t deck_display_service_metrics(const deck_display_service_t *display)
{
    return display == nullptr ? deck_display_metrics_t{} : display->metrics;
}

bool deck_display_service_copy_successful(
    const deck_display_service_t *display,
    uint8_t *output,
    size_t output_size
)
{
    if (display == nullptr || output == nullptr || output_size != DECK_DISPLAY_FRAME_BYTES ||
        !display->has_successful_frame) {
        return false;
    }
    std::memcpy(output, display->successful_frame, DECK_DISPLAY_FRAME_BYTES);
    return true;
}
