#include "deck_application_ui.h"
#include "deck_ui_renderer.h"
#include "deck_ui_scene.h"

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
#include "freertos/queue.h"
#include "freertos/task.h"
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wsign-conversion"
#include "lvgl.h"
#pragma GCC diagnostic pop

namespace {

constexpr uint32_t kDrawRows = 24;
constexpr size_t kDrawBufferBytes = DECK_DISPLAY_WIDTH * kDrawRows * sizeof(uint16_t);
constexpr uint32_t kUiPollMs = 10;
constexpr uint32_t kUiStackBytes = 8192;
constexpr UBaseType_t kUiPriority = 5;
static_assert(
    DECK_AI_PAGE_TOP_OFFSET +
        DECK_AI_PAGE_MAX_LINES * DECK_AI_PAGE_FONT_LINE_HEIGHT +
        (DECK_AI_PAGE_MAX_LINES - 1U) * DECK_AI_PAGE_LINE_SPACING <=
    DECK_DISPLAY_HEIGHT
);

struct UiContext {
    deck_display_service_t *display_service;
    deck_m0_view_model_t model;
    deck_ui_scene_t presented_scene;
    char firmware_version[32];
    lv_display_t *lv_display;
    deck_ui_renderer_t *renderer;
    uint8_t *draw_buffer_a;
    uint8_t *draw_buffer_b;
    deck_application_ui_event_fn event_callback;
    void *event_context;
    QueueHandle_t model_updates;
    int64_t last_tick_us;
    uint64_t last_model_second;
    bool presented_scene_valid;
    bool ready_emitted;
    bool lvgl_initialized;
};

std::atomic_bool ui_started = false;
std::atomic<QueueHandle_t> active_model_updates = nullptr;
StaticQueue_t model_update_queue_storage{};
uint8_t model_update_queue_buffer[sizeof(deck_m0_view_model_t)]{};
QueueHandle_t model_update_queue = nullptr;

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
    active_model_updates.store(nullptr, std::memory_order_release);
    if (context->lv_display != nullptr) {
        deck_ui_renderer_destroy(context->renderer);
        context->renderer = nullptr;
        lv_display_delete(context->lv_display);
        context->lv_display = nullptr;
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

void notify_completed_frame(UiContext *context)
{
    if (!context->ready_emitted) {
        context->ready_emitted = true;
        notify(context, DECK_APPLICATION_UI_READY);
    } else {
        notify(context, DECK_APPLICATION_UI_FRAME_COMPLETED);
    }
}

bool wait_for_display_transfer(UiContext *context)
{
    while (true) {
        const deck_display_result_t result =
            deck_display_service_poll(context->display_service, monotonic_ms());
        if (result == DECK_DISPLAY_COMPLETED) {
            notify_completed_frame(context);
            return true;
        }
        if (result == DECK_DISPLAY_RECOVERED) {
            const deck_display_result_t retry =
                deck_display_service_submit(context->display_service, monotonic_ms());
            if (retry != DECK_DISPLAY_SUBMITTED) {
                return retry == DECK_DISPLAY_UNCHANGED;
            }
        } else if (result == DECK_DISPLAY_START_FAILED ||
                   result == DECK_DISPLAY_INVALID_ARGUMENT ||
                   result == DECK_DISPLAY_UNCHANGED) {
            return false;
        }
        // The SPI callback is asynchronous. Yield so the idle task and Wi-Fi
        // stack keep running while LVGL retains ownership of its draw buffer.
        vTaskDelay(pdMS_TO_TICKS(1));
    }
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
        (void)wait_for_display_transfer(context);
    }
    lv_display_flush_ready(lv_display);
}

bool present_model(UiContext *context)
{
    deck_ui_scene_t scene{};
    if (!deck_ui_scene_project(&context->model, &scene)) {
        return false;
    }
    if (context->presented_scene_valid &&
        deck_ui_scene_equal(&scene, &context->presented_scene)) {
        return true;
    }
    if (!deck_ui_renderer_present(context->renderer, &scene)) {
        return false;
    }
    context->presented_scene = scene;
    context->presented_scene_valid = true;
    return true;
}

void receive_model_update(UiContext *context)
{
    const uint32_t refresh_count = context->model.refresh_count;
    const uint64_t uptime_seconds = context->model.uptime_seconds;
    const uint32_t minimum_free_heap_bytes =
        context->model.minimum_free_heap_bytes;
    if (xQueueReceive(context->model_updates, &context->model, 0) != pdTRUE) {
        return;
    }
    context->model.refresh_count = refresh_count;
    context->model.uptime_seconds = uptime_seconds;
    context->model.minimum_free_heap_bytes = minimum_free_heap_bytes;
    context->model.firmware_version = context->firmware_version;
    (void)present_model(context);
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

    context->renderer = deck_ui_renderer_create(screen);
    if (context->renderer == nullptr) {
        return false;
    }
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

        receive_model_update(context);
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
    if (model_update_queue == nullptr) {
        model_update_queue = xQueueCreateStatic(
            1,
            sizeof(deck_m0_view_model_t),
            model_update_queue_buffer,
            &model_update_queue_storage
        );
    } else {
        (void)xQueueReset(model_update_queue);
    }
    context->model_updates = model_update_queue;
    if (context->model_updates == nullptr) {
        delete context;
        ui_started.store(false, std::memory_order_release);
        return false;
    }
    active_model_updates.store(context->model_updates, std::memory_order_release);
    if (xTaskCreatePinnedToCore(ui_task, "deck_ui", kUiStackBytes, context, kUiPriority, nullptr, 0) != pdPASS) {
        active_model_updates.store(nullptr, std::memory_order_release);
        delete context;
        ui_started.store(false, std::memory_order_release);
        return false;
    }
    return true;
}

bool deck_application_ui_update(const deck_m0_view_model_t *model)
{
    if (model == nullptr || model->firmware_version == nullptr) {
        return false;
    }
    QueueHandle_t updates = active_model_updates.load(std::memory_order_acquire);
    return updates != nullptr && xQueueOverwrite(updates, model) == pdPASS;
}
