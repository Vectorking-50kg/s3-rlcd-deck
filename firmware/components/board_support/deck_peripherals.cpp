#include "deck_peripherals.h"

#include <new>
#include <stddef.h>
#include <stdint.h>

#include "driver/gpio.h"
#include "driver/i2c_master.h"
#include "esp_rom_sys.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

namespace {

constexpr gpio_num_t kI2cSda = GPIO_NUM_13;
constexpr gpio_num_t kI2cScl = GPIO_NUM_14;
constexpr gpio_num_t kKey = GPIO_NUM_18;
constexpr gpio_num_t kBoot = GPIO_NUM_0;
constexpr uint16_t kRtcAddress = 0x51;
constexpr uint16_t kShtc3Address = 0x70;
constexpr uint32_t kI2cFrequencyHz = 400'000;
constexpr int kI2cTimeoutMs = 50;
constexpr uint32_t kPeripheralPollMs = 1'000;
constexpr int16_t kDefaultTemperatureOffsetTenthsC = -40;
constexpr uint32_t kInputTaskStackBytes = 3'072;
constexpr uint32_t kI2cTaskStackBytes = 4'096;
constexpr uint32_t kPublisherTaskStackBytes = 4'096;
constexpr UBaseType_t kInputTaskPriority = 4;
constexpr UBaseType_t kI2cTaskPriority = 3;
constexpr UBaseType_t kPublisherTaskPriority = 2;
constexpr TickType_t kSampleTicks = pdMS_TO_TICKS(10);

struct DeviceContext {
    i2c_master_dev_handle_t handle;
};

bool transmit(void *context, const uint8_t *data, size_t size)
{
    auto *device = static_cast<DeviceContext *>(context);
    return device != nullptr && device->handle != nullptr &&
           i2c_master_transmit(device->handle, data, size, kI2cTimeoutMs) == ESP_OK;
}

bool receive(void *context, uint8_t *data, size_t size)
{
    auto *device = static_cast<DeviceContext *>(context);
    return device != nullptr && device->handle != nullptr &&
           i2c_master_receive(device->handle, data, size, kI2cTimeoutMs) == ESP_OK;
}

bool transmit_receive(
    void *context,
    const uint8_t *write_data,
    size_t write_size,
    uint8_t *read_data,
    size_t read_size
)
{
    auto *device = static_cast<DeviceContext *>(context);
    return device != nullptr && device->handle != nullptr &&
           i2c_master_transmit_receive(
               device->handle,
               write_data,
               write_size,
               read_data,
               read_size,
               kI2cTimeoutMs
           ) == ESP_OK;
}

void delay_us(void *, uint32_t duration_us)
{
    esp_rom_delay_us(duration_us);
}

bool add_device(
    i2c_master_bus_handle_t bus,
    uint16_t address,
    DeviceContext *device
)
{
    if (bus == nullptr || device == nullptr) {
        return false;
    }
    i2c_device_config_t config{};
    config.dev_addr_length = I2C_ADDR_BIT_LEN_7;
    config.device_address = address;
    config.scl_speed_hz = kI2cFrequencyHz;
    return i2c_master_bus_add_device(bus, &config, &device->handle) == ESP_OK;
}

}  // namespace

struct deck_peripherals {
    i2c_master_bus_handle_t bus;
    DeviceContext rtc;
    DeviceContext shtc3;
    deck_peripheral_monitor_t *monitor;
    deck_peripheral_snapshot_fn callback;
    void *callback_context;
    SemaphoreHandle_t state_mutex;
    QueueHandle_t snapshot_queue;
    bool buttons_available;
};

namespace {

void release_unstarted(deck_peripherals_t *peripherals)
{
    if (peripherals == nullptr) {
        return;
    }
    deck_peripheral_monitor_destroy(peripherals->monitor);
    if (peripherals->state_mutex != nullptr) {
        vSemaphoreDelete(peripherals->state_mutex);
    }
    if (peripherals->snapshot_queue != nullptr) {
        vQueueDelete(peripherals->snapshot_queue);
    }
    if (peripherals->rtc.handle != nullptr) {
        (void)i2c_master_bus_rm_device(peripherals->rtc.handle);
    }
    if (peripherals->shtc3.handle != nullptr) {
        (void)i2c_master_bus_rm_device(peripherals->shtc3.handle);
    }
    if (peripherals->bus != nullptr) {
        (void)i2c_del_master_bus(peripherals->bus);
    }
    delete peripherals;
}

void queue_snapshot_locked(deck_peripherals_t *peripherals)
{
    deck_peripheral_snapshot_t snapshot{};
    if (deck_peripheral_monitor_snapshot(peripherals->monitor, &snapshot)) {
        (void)xQueueOverwrite(peripherals->snapshot_queue, &snapshot);
    }
}

void publisher_task(void *task_context)
{
    auto *peripherals = static_cast<deck_peripherals_t *>(task_context);
    deck_peripheral_snapshot_t snapshot{};
    while (true) {
        if (xQueueReceive(peripherals->snapshot_queue, &snapshot, portMAX_DELAY) == pdTRUE) {
            peripherals->callback(peripherals->callback_context, &snapshot);
        }
    }
}

void input_task(void *task_context)
{
    auto *peripherals = static_cast<deck_peripherals_t *>(task_context);
    while (true) {
        const bool key_high = !peripherals->buttons_available || gpio_get_level(kKey) != 0;
        const bool boot_high = !peripherals->buttons_available || gpio_get_level(kBoot) != 0;
        const uint64_t now_ms = static_cast<uint64_t>(esp_timer_get_time() / 1'000);
        if (xSemaphoreTake(peripherals->state_mutex, portMAX_DELAY) == pdTRUE) {
            if (deck_peripheral_monitor_sample_inputs(
                    peripherals->monitor,
                    key_high,
                    boot_high,
                    now_ms
                )) {
                queue_snapshot_locked(peripherals);
            }
            xSemaphoreGive(peripherals->state_mutex);
        }
        vTaskDelay(kSampleTicks);
    }
}

void i2c_task(void *task_context)
{
    auto *peripherals = static_cast<deck_peripherals_t *>(task_context);
    (void)ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
    while (true) {
        const uint64_t now_ms = static_cast<uint64_t>(esp_timer_get_time() / 1'000);
        bool poll_due = false;
        if (xSemaphoreTake(peripherals->state_mutex, portMAX_DELAY) == pdTRUE) {
            poll_due = deck_peripheral_monitor_poll_due(peripherals->monitor, now_ms);
            xSemaphoreGive(peripherals->state_mutex);
        }
        if (poll_due) {
            deck_peripheral_measurement_t measurement{};
            if (deck_peripheral_monitor_measure(peripherals->monitor, &measurement) &&
                xSemaphoreTake(peripherals->state_mutex, portMAX_DELAY) == pdTRUE) {
                (void)deck_peripheral_monitor_apply(peripherals->monitor, &measurement);
                queue_snapshot_locked(peripherals);
                xSemaphoreGive(peripherals->state_mutex);
            }
        }
        vTaskDelay(kSampleTicks);
    }
}

}  // namespace

deck_peripherals_t *deck_peripherals_start(
    deck_peripheral_snapshot_fn callback,
    void *callback_context
)
{
    if (callback == nullptr) {
        return nullptr;
    }
    auto *peripherals = new (std::nothrow) deck_peripherals_t{};
    if (peripherals == nullptr) {
        return nullptr;
    }
    peripherals->callback = callback;
    peripherals->callback_context = callback_context;
    peripherals->state_mutex = xSemaphoreCreateMutex();
    peripherals->snapshot_queue = xQueueCreate(1, sizeof(deck_peripheral_snapshot_t));
    if (peripherals->state_mutex == nullptr || peripherals->snapshot_queue == nullptr) {
        release_unstarted(peripherals);
        return nullptr;
    }

    gpio_config_t button_config{};
    button_config.pin_bit_mask = (1ULL << static_cast<uint32_t>(kKey)) |
                                 (1ULL << static_cast<uint32_t>(kBoot));
    button_config.mode = GPIO_MODE_INPUT;
    button_config.pull_up_en = GPIO_PULLUP_ENABLE;
    button_config.pull_down_en = GPIO_PULLDOWN_DISABLE;
    button_config.intr_type = GPIO_INTR_DISABLE;
    peripherals->buttons_available = gpio_config(&button_config) == ESP_OK;

    i2c_master_bus_config_t bus_config{};
    bus_config.i2c_port = I2C_NUM_0;
    bus_config.sda_io_num = kI2cSda;
    bus_config.scl_io_num = kI2cScl;
    bus_config.clk_source = I2C_CLK_SRC_DEFAULT;
    bus_config.glitch_ignore_cnt = 7;
    bus_config.flags.enable_internal_pullup = true;
    if (i2c_new_master_bus(&bus_config, &peripherals->bus) == ESP_OK) {
        (void)add_device(peripherals->bus, kRtcAddress, &peripherals->rtc);
        (void)add_device(peripherals->bus, kShtc3Address, &peripherals->shtc3);
    }

    const deck_peripheral_monitor_config_t monitor_config = {
        {nullptr, nullptr, transmit_receive, nullptr, &peripherals->rtc},
        {transmit, receive, nullptr, delay_us, &peripherals->shtc3},
        kDefaultTemperatureOffsetTenthsC,
        kPeripheralPollMs,
        peripherals->buttons_available,
    };
    peripherals->monitor = deck_peripheral_monitor_create(&monitor_config);
    if (peripherals->monitor == nullptr) {
        release_unstarted(peripherals);
        return nullptr;
    }

    TaskHandle_t publisher_task_handle = nullptr;
    if (xTaskCreatePinnedToCore(
            publisher_task,
            "deck_publish",
            kPublisherTaskStackBytes,
            peripherals,
            kPublisherTaskPriority,
            &publisher_task_handle,
            1
        ) != pdPASS) {
        release_unstarted(peripherals);
        return nullptr;
    }

    TaskHandle_t i2c_task_handle = nullptr;
    if (xTaskCreatePinnedToCore(
            i2c_task,
            "deck_i2c",
            kI2cTaskStackBytes,
            peripherals,
            kI2cTaskPriority,
            &i2c_task_handle,
            1
        ) != pdPASS) {
        vTaskDelete(publisher_task_handle);
        release_unstarted(peripherals);
        return nullptr;
    }
    if (xTaskCreatePinnedToCore(
            input_task,
            "deck_input",
            kInputTaskStackBytes,
            peripherals,
            kInputTaskPriority,
            nullptr,
            1
        ) != pdPASS) {
        vTaskDelete(i2c_task_handle);
        vTaskDelete(publisher_task_handle);
        release_unstarted(peripherals);
        return nullptr;
    }
    xTaskNotifyGive(i2c_task_handle);
    return peripherals;
}
