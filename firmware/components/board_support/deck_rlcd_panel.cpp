// SPDX-License-Identifier: Apache-2.0
// RLCD register sequence adapted from Waveshare ESP32-S3-RLCD-4.2 example commit
// 9f8da2c12be0934ba108daa1174c0282cd57a03a. The implementation and ownership
// model are original to S3 RLCD Deck; no vendor UI, image, font, or button code is used.

#include "deck_rlcd_panel.h"

#include <atomic>
#include <initializer_list>
#include <new>
#include <stddef.h>
#include <stdint.h>

#include "driver/gpio.h"
#include "driver/spi_master.h"
#include "esp_err.h"
#include "esp_lcd_io_spi.h"
#include "esp_lcd_panel_io.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

namespace {

constexpr gpio_num_t kMosi = GPIO_NUM_12;
constexpr gpio_num_t kClock = GPIO_NUM_11;
constexpr gpio_num_t kDataCommand = GPIO_NUM_5;
constexpr gpio_num_t kChipSelect = GPIO_NUM_40;
constexpr gpio_num_t kReset = GPIO_NUM_41;
constexpr spi_host_device_t kSpiHost = SPI3_HOST;
constexpr uint32_t kPixelClockHz = 10'000'000;
const char *const kTag = "deck_rlcd";

}  // namespace

struct deck_rlcd_panel {
    esp_lcd_panel_io_handle_t io = nullptr;
    std::atomic<deck_display_transfer_done_fn> done = nullptr;
    std::atomic<void *> done_context = nullptr;
    bool bus_initialized = false;
};

namespace {

bool send_command(deck_rlcd_panel_t *panel, uint8_t command, std::initializer_list<uint8_t> parameters)
{
    if (esp_lcd_panel_io_tx_param(panel->io, command, nullptr, 0) != ESP_OK) {
        return false;
    }
    for (const uint8_t parameter : parameters) {
        if (esp_lcd_panel_io_tx_param(panel->io, -1, &parameter, sizeof(parameter)) != ESP_OK) {
            return false;
        }
    }
    return true;
}

bool color_transfer_done(
    esp_lcd_panel_io_handle_t,
    esp_lcd_panel_io_event_data_t *,
    void *context
)
{
    auto *panel = static_cast<deck_rlcd_panel_t *>(context);
    deck_display_transfer_done_fn done = panel->done.exchange(nullptr, std::memory_order_acq_rel);
    void *done_context = panel->done_context.exchange(nullptr, std::memory_order_acq_rel);
    if (done != nullptr) {
        done(done_context);
    }
    return false;
}

bool start_transfer(
    void *context,
    const uint8_t *frame,
    size_t frame_size,
    deck_display_transfer_done_fn done,
    void *done_context
)
{
    auto *panel = static_cast<deck_rlcd_panel_t *>(context);
    if (panel == nullptr || panel->io == nullptr || frame == nullptr || frame_size != DECK_DISPLAY_FRAME_BYTES ||
        done == nullptr || panel->done.load(std::memory_order_acquire) != nullptr) {
        return false;
    }
    if (!send_command(panel, 0x2a, {0x12, 0x2a}) || !send_command(panel, 0x2b, {0x00, 0xc7}) ||
        !send_command(panel, 0x2c, {})) {
        return false;
    }

    panel->done_context.store(done_context, std::memory_order_relaxed);
    panel->done.store(done, std::memory_order_release);
    if (esp_lcd_panel_io_tx_color(panel->io, -1, frame, frame_size) != ESP_OK) {
        panel->done.store(nullptr, std::memory_order_release);
        panel->done_context.store(nullptr, std::memory_order_release);
        return false;
    }
    return true;
}

void reset_panel()
{
    gpio_set_level(kReset, 1);
    vTaskDelay(pdMS_TO_TICKS(50));
    gpio_set_level(kReset, 0);
    vTaskDelay(pdMS_TO_TICKS(20));
    gpio_set_level(kReset, 1);
    vTaskDelay(pdMS_TO_TICKS(50));
}

}  // namespace

deck_rlcd_panel_t *deck_rlcd_panel_create(void)
{
    auto *panel = new (std::nothrow) deck_rlcd_panel_t;
    if (panel == nullptr) {
        return nullptr;
    }

    spi_bus_config_t bus_config = {};
    bus_config.mosi_io_num = kMosi;
    bus_config.miso_io_num = GPIO_NUM_NC;
    bus_config.sclk_io_num = kClock;
    bus_config.quadwp_io_num = GPIO_NUM_NC;
    bus_config.quadhd_io_num = GPIO_NUM_NC;
    bus_config.max_transfer_sz = DECK_DISPLAY_FRAME_BYTES;
    if (spi_bus_initialize(kSpiHost, &bus_config, SPI_DMA_CH_AUTO) != ESP_OK) {
        delete panel;
        return nullptr;
    }
    panel->bus_initialized = true;

    esp_lcd_panel_io_spi_config_t io_config = {};
    io_config.cs_gpio_num = kChipSelect;
    io_config.dc_gpio_num = kDataCommand;
    io_config.spi_mode = 0;
    io_config.pclk_hz = kPixelClockHz;
    io_config.trans_queue_depth = 1;
    io_config.on_color_trans_done = color_transfer_done;
    io_config.user_ctx = panel;
    io_config.lcd_cmd_bits = 8;
    io_config.lcd_param_bits = 8;
    if (esp_lcd_new_panel_io_spi(kSpiHost, &io_config, &panel->io) != ESP_OK) {
        spi_bus_free(kSpiHost);
        delete panel;
        return nullptr;
    }

    gpio_config_t reset_config = {};
    reset_config.pin_bit_mask = 1ULL << static_cast<unsigned>(kReset);
    reset_config.mode = GPIO_MODE_OUTPUT;
    reset_config.pull_up_en = GPIO_PULLUP_ENABLE;
    reset_config.pull_down_en = GPIO_PULLDOWN_DISABLE;
    reset_config.intr_type = GPIO_INTR_DISABLE;
    if (gpio_config(&reset_config) != ESP_OK) {
        deck_rlcd_panel_destroy(panel);
        return nullptr;
    }
    gpio_set_level(kReset, 1);
    return panel;
}

bool deck_rlcd_panel_initialize(deck_rlcd_panel_t *panel)
{
    if (panel == nullptr || panel->io == nullptr) {
        return false;
    }
    reset_panel();

    const bool initialized =
        send_command(panel, 0xd6, {0x17, 0x02}) && send_command(panel, 0xd1, {0x01}) &&
        send_command(panel, 0xc0, {0x11, 0x04}) && send_command(panel, 0xc1, {0x69, 0x69, 0x69, 0x69}) &&
        send_command(panel, 0xc2, {0x19, 0x19, 0x19, 0x19}) &&
        send_command(panel, 0xc4, {0x4b, 0x4b, 0x4b, 0x4b}) &&
        send_command(panel, 0xc5, {0x19, 0x19, 0x19, 0x19}) && send_command(panel, 0xd8, {0x80, 0xe9}) &&
        send_command(panel, 0xb2, {0x02}) &&
        send_command(panel, 0xb3, {0xe5, 0xf6, 0x05, 0x46, 0x77, 0x77, 0x77, 0x77, 0x76, 0x45}) &&
        send_command(panel, 0xb4, {0x05, 0x46, 0x77, 0x77, 0x77, 0x77, 0x76, 0x45}) &&
        send_command(panel, 0x62, {0x32, 0x03, 0x1f}) && send_command(panel, 0xb7, {0x13}) &&
        send_command(panel, 0xb0, {0x64}) && send_command(panel, 0x11, {});
    if (!initialized) {
        ESP_LOGE(kTag, "controller initialization failed");
        return false;
    }

    vTaskDelay(pdMS_TO_TICKS(200));
    const bool display_on = send_command(panel, 0xc9, {0x00}) && send_command(panel, 0x36, {0x48}) &&
                            send_command(panel, 0x3a, {0x11}) && send_command(panel, 0xb9, {0x20}) &&
                            send_command(panel, 0xb8, {0x29}) && send_command(panel, 0x21, {}) &&
                            send_command(panel, 0x2a, {0x12, 0x2a}) && send_command(panel, 0x2b, {0x00, 0xc7}) &&
                            send_command(panel, 0x35, {0x00}) && send_command(panel, 0xd0, {0xff}) &&
                            send_command(panel, 0x38, {}) && send_command(panel, 0x29, {});
    if (!display_on) {
        ESP_LOGE(kTag, "controller display-on sequence failed");
    }
    return display_on;
}

deck_display_panel_adapter_t deck_rlcd_panel_adapter(deck_rlcd_panel_t *panel)
{
    return {start_transfer, panel};
}

void deck_rlcd_panel_destroy(deck_rlcd_panel_t *panel)
{
    if (panel == nullptr) {
        return;
    }
    if (panel->io != nullptr) {
        esp_lcd_panel_io_del(panel->io);
    }
    if (panel->bus_initialized) {
        spi_bus_free(kSpiHost);
    }
    delete panel;
}
