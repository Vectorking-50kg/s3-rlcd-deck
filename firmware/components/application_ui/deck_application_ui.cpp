#include "deck_application_ui.h"

#include <atomic>
#include <new>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_heap_caps.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wsign-conversion"
#include "lvgl.h"
#pragma GCC diagnostic pop

LV_FONT_DECLARE(lv_font_deck_m0_16);

namespace {

constexpr uint32_t kDrawRows = 24;
constexpr size_t kDrawBufferBytes = DECK_DISPLAY_WIDTH * kDrawRows * sizeof(uint16_t);
constexpr uint32_t kUiPollMs = 10;
constexpr uint32_t kUiStackBytes = 8192;
constexpr UBaseType_t kUiPriority = 5;

struct UiContext {
    deck_display_service_t *display_service;
    deck_m0_view_model_t model;
    deck_m0_view_model_t presented_model;
    char firmware_version[32];
    char page_text[768];
    lv_display_t *lv_display;
    lv_obj_t *page_label;
    uint8_t *draw_buffer_a;
    uint8_t *draw_buffer_b;
    deck_application_ui_event_fn event_callback;
    void *event_context;
    int64_t last_tick_us;
    uint64_t last_model_second;
    bool presented_model_valid;
    bool flush_waiting;
    bool ready_emitted;
    bool lvgl_initialized;
};

std::atomic_bool ui_started = false;

uint64_t monotonic_ms()
{
    return static_cast<uint64_t>(esp_timer_get_time() / 1000);
}

void notify(UiContext *context, deck_application_ui_state_t state)
{
    if (context->event_callback == nullptr) {
        return;
    }
    const deck_application_ui_event_t event = {
        state,
        deck_display_service_metrics(context->display_service),
    };
    context->event_callback(context->event_context, &event);
}

void fail_task(UiContext *context)
{
    if (context->lv_display != nullptr) {
        lv_display_delete(context->lv_display);
        context->lv_display = nullptr;
        context->page_label = nullptr;
    }
    if (context->draw_buffer_a != nullptr) {
        heap_caps_free(context->draw_buffer_a);
        context->draw_buffer_a = nullptr;
    }
    if (context->draw_buffer_b != nullptr) {
        heap_caps_free(context->draw_buffer_b);
        context->draw_buffer_b = nullptr;
    }
    if (context->lvgl_initialized) {
        lv_deinit();
        context->lvgl_initialized = false;
    }
    notify(context, DECK_APPLICATION_UI_FAILED);
    ui_started.store(false, std::memory_order_release);
    delete context;
    vTaskDelete(nullptr);
}

void display_flush(lv_display_t *lv_display, const lv_area_t *area, uint8_t *pixels)
{
    auto *context = static_cast<UiContext *>(lv_display_get_user_data(lv_display));
    const int32_t width = area->x2 - area->x1 + 1;
    const int32_t height = area->y2 - area->y1 + 1;
    if (width <= 0 || height <= 0) {
        lv_display_flush_ready(lv_display);
        return;
    }

    const deck_display_area_t display_area = {
        static_cast<int16_t>(area->x1),
        static_cast<int16_t>(area->y1),
        static_cast<int16_t>(area->x2),
        static_cast<int16_t>(area->y2),
    };
    const size_t pixel_count = static_cast<size_t>(width) * static_cast<size_t>(height);
    deck_display_service_update(
        context->display_service,
        display_area,
        reinterpret_cast<const uint16_t *>(pixels),
        pixel_count
    );

    if (!lv_display_flush_is_last(lv_display)) {
        lv_display_flush_ready(lv_display);
        return;
    }

    const deck_display_result_t submit_result =
        deck_display_service_submit(context->display_service, monotonic_ms());
    if (submit_result == DECK_DISPLAY_SUBMITTED) {
        context->flush_waiting = true;
        return;
    }
    lv_display_flush_ready(lv_display);
}

bool present_model(UiContext *context)
{
    if (context->presented_model_valid &&
        deck_m0_view_model_equal(&context->model, &context->presented_model)) {
        return true;
    }
    if (!deck_m0_view_model_format(&context->model, context->page_text, sizeof(context->page_text))) {
        return false;
    }
    lv_label_set_text(context->page_label, context->page_text);
    context->presented_model = context->model;
    context->presented_model.firmware_version = context->firmware_version;
    context->presented_model_valid = true;
    return true;
}

bool initialize_lvgl(UiContext *context)
{
    lv_init();
    context->lvgl_initialized = true;
    context->draw_buffer_a = static_cast<uint8_t *>(heap_caps_malloc(kDrawBufferBytes, MALLOC_CAP_SPIRAM));
    context->draw_buffer_b = static_cast<uint8_t *>(heap_caps_malloc(kDrawBufferBytes, MALLOC_CAP_SPIRAM));
    if (context->draw_buffer_a == nullptr || context->draw_buffer_b == nullptr) {
        return false;
    }

    context->lv_display = lv_display_create(DECK_DISPLAY_WIDTH, DECK_DISPLAY_HEIGHT);
    if (context->lv_display == nullptr) {
        return false;
    }
    lv_display_set_color_format(context->lv_display, LV_COLOR_FORMAT_RGB565);
    lv_display_set_user_data(context->lv_display, context);
    lv_display_set_flush_cb(context->lv_display, display_flush);
    lv_display_set_buffers(
        context->lv_display,
        context->draw_buffer_a,
        context->draw_buffer_b,
        kDrawBufferBytes,
        LV_DISPLAY_RENDER_MODE_PARTIAL
    );

    lv_obj_t *screen = lv_screen_active();
    lv_obj_set_style_bg_color(screen, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(screen, LV_OPA_COVER, LV_PART_MAIN);
    lv_obj_remove_flag(screen, LV_OBJ_FLAG_SCROLLABLE);

    context->page_label = lv_label_create(screen);
    if (context->page_label == nullptr) {
        return false;
    }
    lv_obj_set_width(context->page_label, DECK_DISPLAY_WIDTH - 16);
    lv_label_set_long_mode(context->page_label, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_set_style_text_font(context->page_label, &lv_font_deck_m0_16, LV_PART_MAIN);
    lv_obj_set_style_text_color(context->page_label, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_text_line_space(context->page_label, 5, LV_PART_MAIN);
    lv_obj_align(context->page_label, LV_ALIGN_TOP_LEFT, 8, 6);
    return present_model(context);
}

void update_model(UiContext *context, uint64_t now_seconds)
{
    if (now_seconds == context->last_model_second) {
        return;
    }
    context->last_model_second = now_seconds;
    context->model.uptime_seconds = now_seconds;
    context->model.minimum_free_heap_bytes = esp_get_minimum_free_heap_size();
    context->model.refresh_count = deck_display_service_metrics(context->display_service).completed_frames;
    present_model(context);
}

void ui_task(void *task_context)
{
    auto *context = static_cast<UiContext *>(task_context);
    if (!initialize_lvgl(context)) {
        fail_task(context);
        return;
    }

    context->last_tick_us = esp_timer_get_time();
    while (true) {
        const int64_t now_us = esp_timer_get_time();
        const int64_t elapsed_ms = (now_us - context->last_tick_us) / 1000;
        if (elapsed_ms > 0) {
            lv_tick_inc(static_cast<uint32_t>(elapsed_ms));
            context->last_tick_us += elapsed_ms * 1000;
        }

        const deck_display_result_t display_result =
            deck_display_service_poll(context->display_service, static_cast<uint64_t>(now_us / 1000));
        if (context->flush_waiting &&
            (display_result == DECK_DISPLAY_COMPLETED || display_result == DECK_DISPLAY_RECOVERING ||
             display_result == DECK_DISPLAY_START_FAILED)) {
            context->flush_waiting = false;
            lv_display_flush_ready(context->lv_display);
        }
        if (display_result == DECK_DISPLAY_COMPLETED) {
            if (!context->ready_emitted) {
                context->ready_emitted = true;
                notify(context, DECK_APPLICATION_UI_READY);
            } else {
                notify(context, DECK_APPLICATION_UI_FRAME_COMPLETED);
            }
        }
        if (display_result == DECK_DISPLAY_COMPLETED || display_result == DECK_DISPLAY_RECOVERED) {
            (void)deck_display_service_submit(
                context->display_service,
                static_cast<uint64_t>(now_us / 1000)
            );
        }

        update_model(context, static_cast<uint64_t>(now_us / 1'000'000));
        lv_timer_handler();
        vTaskDelay(pdMS_TO_TICKS(kUiPollMs));
    }
}

}  // namespace

bool deck_application_ui_start(
    deck_display_service_t *display,
    const deck_m0_view_model_t *initial_model,
    deck_application_ui_event_fn event_callback,
    void *event_context
)
{
    if (display == nullptr || initial_model == nullptr || initial_model->firmware_version == nullptr ||
        ui_started.exchange(true, std::memory_order_acq_rel)) {
        return false;
    }

    auto *context = new (std::nothrow) UiContext{};
    if (context == nullptr) {
        ui_started.store(false, std::memory_order_release);
        return false;
    }
    const int version_size =
        snprintf(context->firmware_version, sizeof(context->firmware_version), "%s", initial_model->firmware_version);
    if (version_size < 0 || static_cast<size_t>(version_size) >= sizeof(context->firmware_version)) {
        delete context;
        ui_started.store(false, std::memory_order_release);
        return false;
    }

    context->display_service = display;
    context->model = *initial_model;
    context->model.firmware_version = context->firmware_version;
    context->event_callback = event_callback;
    context->event_context = event_context;
    if (xTaskCreatePinnedToCore(ui_task, "deck_ui", kUiStackBytes, context, kUiPriority, nullptr, 0) != pdPASS) {
        delete context;
        ui_started.store(false, std::memory_order_release);
        return false;
    }
    return true;
}
