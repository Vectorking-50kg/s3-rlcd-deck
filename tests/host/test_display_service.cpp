#include "deck_display.h"

#include <array>
#include <cassert>
#include <cstdint>
#include <cstring>

namespace {

void packs_landscape_pixels_into_controller_order()
{
    std::array<uint8_t, DECK_DISPLAY_FRAME_BYTES> frame{};
    const std::array<uint16_t, 2> pixels = {0x0000, 0xffff};
    const deck_display_area_t area = {0, 0, 1, 0};

    assert(
        deck_display_pack_rgb565(frame.data(), frame.size(), area, pixels.data(), pixels.size()) ==
        DECK_DISPLAY_PACK_CHANGED
    );

    assert(frame[74] == 0x01);
    assert(frame[0] == 0x00);
    assert(frame[75] == 0x00);
}

void maps_bottom_left_pixels_to_the_high_bits()
{
    std::array<uint8_t, DECK_DISPLAY_FRAME_BYTES> frame{};
    const std::array<uint16_t, 3> pixels = {0xffff, 0xffff, 0xffff};
    const deck_display_area_t area = {0, 299, 2, 299};

    assert(
        deck_display_pack_rgb565(frame.data(), frame.size(), area, pixels.data(), pixels.size()) ==
        DECK_DISPLAY_PACK_CHANGED
    );

    assert(frame[0] == 0xc0);
    assert(frame[75] == 0x80);
}

void rejects_invalid_areas_without_touching_the_frame()
{
    std::array<uint8_t, DECK_DISPLAY_FRAME_BYTES> frame{};
    frame.fill(0xa5);
    const auto unchanged = frame;
    const std::array<uint16_t, 2> pixels = {0xffff, 0xffff};
    const deck_display_area_t area = {399, 299, 400, 299};

    assert(
        deck_display_pack_rgb565(frame.data(), frame.size(), area, pixels.data(), pixels.size()) ==
        DECK_DISPLAY_PACK_INVALID_ARGUMENT
    );
    assert(frame == unchanged);
}

void reports_unchanged_when_pixels_match_the_frame()
{
    std::array<uint8_t, DECK_DISPLAY_FRAME_BYTES> frame{};
    const std::array<uint16_t, 1> pixels = {0x0000};
    const deck_display_area_t area = {20, 20, 20, 20};

    assert(
        deck_display_pack_rgb565(frame.data(), frame.size(), area, pixels.data(), pixels.size()) ==
        DECK_DISPLAY_PACK_UNCHANGED
    );
}

struct FakePanel {
    bool accept_transfer = true;
    const uint8_t *frame = nullptr;
    size_t frame_size = 0;
    deck_display_transfer_done_fn done = nullptr;
    void *done_context = nullptr;
    uint32_t starts = 0;
    std::array<uint8_t, DECK_DISPLAY_FRAME_BYTES> completed_frame{};

    void complete()
    {
        assert(frame != nullptr);
        std::memcpy(completed_frame.data(), frame, frame_size);
        done(done_context);
    }
};

bool start_transfer(
    void *context,
    const uint8_t *frame,
    size_t frame_size,
    deck_display_transfer_done_fn done,
    void *done_context
)
{
    auto &panel = *static_cast<FakePanel *>(context);
    ++panel.starts;
    if (!panel.accept_transfer) {
        return false;
    }
    panel.frame = frame;
    panel.frame_size = frame_size;
    panel.done = done;
    panel.done_context = done_context;
    return true;
}

void owns_the_frame_until_async_completion()
{
    FakePanel panel;
    const deck_display_panel_adapter_t adapter = {start_transfer, &panel};
    deck_display_service_t *display = deck_display_service_create(adapter, 50);
    assert(display != nullptr);

    const uint16_t black = 0x0000;
    const uint16_t white = 0xffff;
    const deck_display_area_t top_left = {0, 0, 0, 0};
    assert(deck_display_service_update(display, top_left, &black, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_submit(display, 100) == DECK_DISPLAY_SUBMITTED);
    assert(panel.frame_size == DECK_DISPLAY_FRAME_BYTES);
    assert(panel.frame[74] == 0xfd);

    assert(deck_display_service_update(display, top_left, &white, 1) == DECK_DISPLAY_UPDATED);
    assert(panel.frame[74] == 0xfd);
    assert(deck_display_service_submit(display, 110) == DECK_DISPLAY_IN_FLIGHT);

    panel.complete();
    assert(deck_display_service_poll(display, 111) == DECK_DISPLAY_COMPLETED);
    assert(deck_display_service_submit(display, 112) == DECK_DISPLAY_SUBMITTED);
    assert(panel.frame[74] == 0xff);
    panel.complete();
    assert(deck_display_service_poll(display, 113) == DECK_DISPLAY_COMPLETED);
    assert(deck_display_service_submit(display, 114) == DECK_DISPLAY_UNCHANGED);

    const deck_display_metrics_t metrics = deck_display_service_metrics(display);
    assert(metrics.submitted_frames == 2);
    assert(metrics.completed_frames == 2);
    assert(metrics.coalesced_submissions == 1);
    assert(metrics.transfer_timeouts == 0);
    assert(deck_display_service_destroy(display));
}

void counts_a_timeout_once_and_accepts_late_completion()
{
    FakePanel panel;
    const deck_display_panel_adapter_t adapter = {start_transfer, &panel};
    deck_display_service_t *display = deck_display_service_create(adapter, 50);
    assert(display != nullptr);

    const uint16_t black = 0x0000;
    const uint16_t white = 0xffff;
    const deck_display_area_t top_left = {0, 0, 0, 0};
    const deck_display_area_t bottom_left = {0, 299, 0, 299};
    assert(deck_display_service_update(display, top_left, &black, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_submit(display, 100) == DECK_DISPLAY_SUBMITTED);
    panel.complete();
    assert(deck_display_service_poll(display, 101) == DECK_DISPLAY_COMPLETED);
    assert(panel.completed_frame[74] == 0xfd);

    assert(deck_display_service_update(display, top_left, &white, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_submit(display, 200) == DECK_DISPLAY_SUBMITTED);
    assert(deck_display_service_update(display, bottom_left, &black, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_poll(display, 249) == DECK_DISPLAY_IN_FLIGHT);
    assert(deck_display_service_poll(display, 250) == DECK_DISPLAY_TIMED_OUT);
    assert(panel.completed_frame[74] == 0xfd);
    assert(deck_display_service_poll(display, 300) == DECK_DISPLAY_IN_FLIGHT);
    assert(deck_display_service_metrics(display).transfer_timeouts == 1);

    panel.complete();
    assert(deck_display_service_poll(display, 301) == DECK_DISPLAY_COMPLETED);
    assert(deck_display_service_submit(display, 302) == DECK_DISPLAY_SUBMITTED);
    assert(panel.frame[74] == 0xff);
    assert(panel.frame[0] == 0x7f);
    panel.complete();
    assert(deck_display_service_poll(display, 303) == DECK_DISPLAY_COMPLETED);
    assert(deck_display_service_metrics(display).completed_frames == 3);
    assert(deck_display_service_destroy(display));
}

void keeps_a_dirty_frame_when_starting_the_transfer_fails()
{
    FakePanel panel;
    panel.accept_transfer = false;
    const deck_display_panel_adapter_t adapter = {start_transfer, &panel};
    deck_display_service_t *display = deck_display_service_create(adapter, 50);
    assert(display != nullptr);

    const uint16_t black = 0x0000;
    const deck_display_area_t bottom_left = {0, 299, 0, 299};
    assert(deck_display_service_update(display, bottom_left, &black, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_submit(display, 10) == DECK_DISPLAY_START_FAILED);
    assert(deck_display_service_metrics(display).start_failures == 1);

    panel.accept_transfer = true;
    assert(deck_display_service_submit(display, 11) == DECK_DISPLAY_SUBMITTED);
    panel.complete();
    assert(deck_display_service_poll(display, 12) == DECK_DISPLAY_COMPLETED);
    assert(panel.starts == 2);
    assert(deck_display_service_destroy(display));
}

void refuses_to_destroy_an_in_flight_transfer()
{
    FakePanel panel;
    const deck_display_panel_adapter_t adapter = {start_transfer, &panel};
    deck_display_service_t *display = deck_display_service_create(adapter, 50);
    assert(display != nullptr);

    const uint16_t black = 0x0000;
    const deck_display_area_t top_left = {0, 0, 0, 0};
    assert(deck_display_service_update(display, top_left, &black, 1) == DECK_DISPLAY_UPDATED);
    assert(deck_display_service_submit(display, 100) == DECK_DISPLAY_SUBMITTED);
    assert(!deck_display_service_destroy(display));

    panel.complete();
    assert(deck_display_service_poll(display, 101) == DECK_DISPLAY_COMPLETED);
    assert(deck_display_service_destroy(display));
}

}  // namespace

int main()
{
    packs_landscape_pixels_into_controller_order();
    maps_bottom_left_pixels_to_the_high_bits();
    rejects_invalid_areas_without_touching_the_frame();
    reports_unchanged_when_pixels_match_the_frame();
    owns_the_frame_until_async_completion();
    counts_a_timeout_once_and_accepts_late_completion();
    keeps_a_dirty_frame_when_starting_the_transfer_fails();
    refuses_to_destroy_an_in_flight_transfer();
    return 0;
}
