#include "deck_serial_service.h"

#include <atomic>
#include <new>

#include "driver/gpio.h"
#include "driver/uart.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

namespace {

constexpr uart_port_t kTargetUart = UART_NUM_1;
constexpr gpio_num_t kTargetRx = GPIO_NUM_44;
constexpr gpio_num_t kTargetTx = GPIO_NUM_17;
constexpr int kTargetRxBufferBytes = 2'048;
constexpr int kTargetTxBufferBytes = 2'048;
constexpr size_t kCommandQueueDepth = 16;
constexpr uint32_t kOwnerTaskStackBytes = 4'096;
constexpr UBaseType_t kOwnerTaskPriority = 4;
constexpr TickType_t kOwnerPollTicks = pdMS_TO_TICKS(100);
constexpr TickType_t kLifecycleTimeoutTicks = pdMS_TO_TICKS(2'000);
constexpr EventBits_t kReadyBit = BIT0;
constexpr EventBits_t kStoppedBit = BIT1;

enum class CommandKind : uint8_t {
    enter,
    exit,
    request_web,
    web_activity,
    web_disconnect,
    stop,
};

struct Command {
    CommandKind kind;
    bool enable;
    uint64_t session_id;
    uint64_t request_id;
    uint64_t lease_id;
    uint64_t control_epoch;
};

uint64_t monotonic_ms()
{
    return static_cast<uint64_t>(esp_timer_get_time() / 1'000);
}

bool set_target_tx_high_impedance(void *)
{
    gpio_config_t config{};
    config.pin_bit_mask = 1ULL << static_cast<uint32_t>(kTargetTx);
    config.mode = GPIO_MODE_INPUT;
    config.pull_up_en = GPIO_PULLUP_DISABLE;
    config.pull_down_en = GPIO_PULLDOWN_DISABLE;
    config.intr_type = GPIO_INTR_DISABLE;
    return gpio_config(&config) == ESP_OK;
}

bool install_target_uart(void *)
{
    const uart_config_t config = {
        .baud_rate = 115'200,
        .data_bits = UART_DATA_8_BITS,
        .parity = UART_PARITY_DISABLE,
        .stop_bits = UART_STOP_BITS_1,
        .flow_ctrl = UART_HW_FLOWCTRL_DISABLE,
        .rx_flow_ctrl_thresh = 0,
        .source_clk = UART_SCLK_DEFAULT,
        .flags = {},
    };
    if (uart_driver_install(
            kTargetUart,
            kTargetRxBufferBytes,
            kTargetTxBufferBytes,
            0,
            nullptr,
            0
        ) != ESP_OK) {
        return false;
    }
    if (uart_param_config(kTargetUart, &config) != ESP_OK ||
        uart_set_pin(
            kTargetUart,
            kTargetTx,
            kTargetRx,
            UART_PIN_NO_CHANGE,
            UART_PIN_NO_CHANGE
        ) != ESP_OK) {
        (void)uart_driver_delete(kTargetUart);
        (void)set_target_tx_high_impedance(nullptr);
        return false;
    }
    return true;
}

void uninstall_target_uart(void *)
{
    if (uart_is_driver_installed(kTargetUart)) {
        (void)uart_driver_delete(kTargetUart);
    }
}

// #46 supplies the bounded USB/Web queues. Keeping these owner callbacks in
// place makes queue invalidation part of the state transition, not a caller
// convention, while this slice has no payload queue to retain.
void clear_usb_tx(void *) {}
void clear_web_tx(void *) {}

}  // namespace

struct deck_serial_service {
    QueueHandle_t commands;
    SemaphoreHandle_t snapshot_mutex;
    EventGroupHandle_t lifecycle;
    TaskHandle_t owner_task;
    deck_serial_session_t *session;
    deck_serial_service_event_fn callback;
    void *callback_context;
    deck_serial_session_snapshot_t latest_snapshot;
    std::atomic<bool> stop_requested;
    std::atomic<uint64_t> control_epoch;
};

namespace {

void publish(
    deck_serial_service_t *service,
    const deck_serial_command_result_t *result
)
{
    deck_serial_service_event_t event{};
    if (!deck_serial_session_snapshot(service->session, &event.snapshot)) {
        return;
    }
    event.has_command_result = result != nullptr;
    if (result != nullptr) {
        event.command_result = *result;
    }
    if (xSemaphoreTake(service->snapshot_mutex, portMAX_DELAY) == pdTRUE) {
        service->latest_snapshot = event.snapshot;
        xSemaphoreGive(service->snapshot_mutex);
    }
    if (service->callback != nullptr) {
        service->callback(service->callback_context, &event);
    }
}

bool snapshot_changed(
    const deck_serial_session_snapshot_t &left,
    const deck_serial_session_snapshot_t &right
)
{
    return left.state != right.state || left.session_id != right.session_id ||
           left.owner_generation != right.owner_generation ||
           left.lease_id != right.lease_id ||
           left.lease_deadline_ms != right.lease_deadline_ms ||
           left.usb_tx_rejected != right.usb_tx_rejected ||
           left.uart_install_failures != right.uart_install_failures ||
           left.uart_install_failed != right.uart_install_failed ||
           left.uart_installed != right.uart_installed;
}

void process_command(deck_serial_service_t *service, const Command &command)
{
    deck_serial_command_result_t result{};
    bool has_result = false;
    const uint64_t now_ms = monotonic_ms();
    // Expiry must advance even when control producers keep the queue busy.
    deck_serial_session_tick(service->session, now_ms);
    switch (command.kind) {
        case CommandKind::enter:
            has_result = deck_serial_session_enter(
                service->session,
                command.control_epoch,
                now_ms,
                &result
            );
            break;
        case CommandKind::exit:
            has_result = deck_serial_session_exit(
                service->session,
                command.control_epoch,
                &result
            );
            break;
        case CommandKind::request_web:
            has_result = deck_serial_session_request_web(
                service->session,
                command.session_id,
                command.request_id,
                command.enable,
                now_ms,
                &result
            );
            break;
        case CommandKind::web_activity:
            (void)deck_serial_session_web_activity(
                service->session,
                command.session_id,
                command.lease_id,
                now_ms
            );
            break;
        case CommandKind::web_disconnect:
            (void)deck_serial_session_web_disconnect(
                service->session,
                command.session_id,
                command.lease_id
            );
            break;
        case CommandKind::stop:
            has_result = deck_serial_session_exit(
                service->session,
                command.control_epoch,
                &result
            );
            break;
    }
    publish(service, has_result ? &result : nullptr);
}

void owner_task(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    const deck_serial_session_config_t config = {
        {install_target_uart, uninstall_target_uart,
         [](void *adapter_context) {
             (void)set_target_tx_high_impedance(adapter_context);
         },
         clear_usb_tx, clear_web_tx, nullptr},
        DECK_SERIAL_DEFAULT_WEB_LEASE_MS,
    };
    service->session = deck_serial_session_create(&config);
    if (service->session == nullptr) {
        xEventGroupSetBits(service->lifecycle, kStoppedBit);
        vTaskSuspend(nullptr);
    }
    xEventGroupSetBits(service->lifecycle, kReadyBit);
    publish(service, nullptr);

    while (true) {
        Command command{};
        if (xQueueReceive(service->commands, &command, kOwnerPollTicks) == pdTRUE) {
            process_command(service, command);
            if (command.kind == CommandKind::stop) {
                deck_serial_session_destroy(service->session);
                service->session = nullptr;
                xEventGroupSetBits(service->lifecycle, kStoppedBit);
                vTaskSuspend(nullptr);
            }
        } else {
            deck_serial_session_snapshot_t before{};
            deck_serial_session_snapshot_t after{};
            (void)deck_serial_session_snapshot(service->session, &before);
            deck_serial_session_tick(service->session, monotonic_ms());
            (void)deck_serial_session_snapshot(service->session, &after);
            if (snapshot_changed(before, after)) {
                publish(service, nullptr);
            }
        }
    }
}

bool enqueue(deck_serial_service_t *service, const Command &command)
{
    if (service == nullptr ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    Command stamped = command;
    stamped.control_epoch = service->control_epoch.load(std::memory_order_acquire);
    return xQueueSend(service->commands, &stamped, 0) == pdTRUE;
}

uint64_t advance_control_epoch(deck_serial_service_t *service)
{
    uint64_t current = service->control_epoch.load(std::memory_order_acquire);
    while (true) {
        uint64_t next = current + 1U;
        if (next == 0) {
            next = 1;
        }
        if (service->control_epoch.compare_exchange_weak(
                current,
                next,
                std::memory_order_acq_rel,
                std::memory_order_acquire
            )) {
            return next;
        }
    }
}

void release_unstarted(deck_serial_service_t *service)
{
    if (service == nullptr) {
        return;
    }
    if (service->owner_task != nullptr) {
        vTaskDelete(service->owner_task);
    }
    if (service->session != nullptr) {
        deck_serial_session_destroy(service->session);
        service->session = nullptr;
    }
    if (service->commands != nullptr) {
        vQueueDelete(service->commands);
    }
    if (service->snapshot_mutex != nullptr) {
        vSemaphoreDelete(service->snapshot_mutex);
    }
    if (service->lifecycle != nullptr) {
        vEventGroupDelete(service->lifecycle);
    }
    (void)set_target_tx_high_impedance(nullptr);
    delete service;
}

}  // namespace

deck_serial_service_t *deck_serial_service_start(
    deck_serial_service_event_fn callback,
    void *callback_context
)
{
    if (!deck_serial_service_prepare_disarmed()) {
        return nullptr;
    }
    auto *service = new (std::nothrow) deck_serial_service_t{};
    if (service == nullptr) {
        return nullptr;
    }
    service->callback = callback;
    service->callback_context = callback_context;
    service->commands = xQueueCreate(kCommandQueueDepth, sizeof(Command));
    service->snapshot_mutex = xSemaphoreCreateMutex();
    service->lifecycle = xEventGroupCreate();
    service->stop_requested.store(false, std::memory_order_release);
    service->control_epoch.store(0, std::memory_order_release);
    if (service->commands == nullptr || service->snapshot_mutex == nullptr ||
        service->lifecycle == nullptr ||
        xTaskCreatePinnedToCore(
            owner_task,
            "serial_owner",
            kOwnerTaskStackBytes,
            service,
            kOwnerTaskPriority,
            &service->owner_task,
            1
        ) != pdPASS) {
        release_unstarted(service);
        return nullptr;
    }
    const EventBits_t ready = xEventGroupWaitBits(
        service->lifecycle,
        kReadyBit | kStoppedBit,
        pdFALSE,
        pdFALSE,
        kLifecycleTimeoutTicks
    );
    if ((ready & kReadyBit) == 0) {
        release_unstarted(service);
        return nullptr;
    }
    return service;
}

bool deck_serial_service_prepare_disarmed(void)
{
    return set_target_tx_high_impedance(nullptr);
}

bool deck_serial_service_stop(deck_serial_service_t *service)
{
    if (service == nullptr) {
        return true;
    }
    bool expected = false;
    if (service->stop_requested.compare_exchange_strong(
            expected,
            true,
            std::memory_order_acq_rel
        )) {
        const uint64_t control_epoch = advance_control_epoch(service);
        const Command command{
            CommandKind::stop, false, 0, 0, 0, control_epoch
        };
        if (xQueueSendToFront(
                service->commands,
                &command,
                kOwnerPollTicks
            ) != pdTRUE) {
            service->stop_requested.store(false, std::memory_order_release);
            return false;
        }
    }
    const EventBits_t stopped = xEventGroupWaitBits(
        service->lifecycle,
        kStoppedBit,
        pdFALSE,
        pdTRUE,
        kLifecycleTimeoutTicks
    );
    if ((stopped & kStoppedBit) == 0) {
        return false;
    }
    vTaskDelete(service->owner_task);
    service->owner_task = nullptr;
    vQueueDelete(service->commands);
    vSemaphoreDelete(service->snapshot_mutex);
    vEventGroupDelete(service->lifecycle);
    delete service;
    return true;
}

bool deck_serial_service_enter(deck_serial_service_t *service)
{
    return enqueue(service, {CommandKind::enter, false, 0, 0, 0, 0});
}

bool deck_serial_service_exit(deck_serial_service_t *service)
{
    if (service == nullptr ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    const uint64_t control_epoch = advance_control_epoch(service);
    const Command command{
        CommandKind::exit, false, 0, 0, 0, control_epoch
    };
    return xQueueSendToFront(service->commands, &command, kOwnerPollTicks) == pdTRUE;
}

bool deck_serial_service_request_web(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t request_id,
    bool enable
)
{
    return enqueue(
        service,
        {CommandKind::request_web, enable, session_id, request_id, 0, 0}
    );
}

bool deck_serial_service_web_activity(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id
)
{
    return enqueue(
        service,
        {CommandKind::web_activity, false, session_id, 0, lease_id, 0}
    );
}

bool deck_serial_service_web_disconnect(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id
)
{
    return enqueue(
        service,
        {CommandKind::web_disconnect, false, session_id, 0, lease_id, 0}
    );
}

bool deck_serial_service_snapshot(
    deck_serial_service_t *service,
    deck_serial_session_snapshot_t *snapshot
)
{
    if (service == nullptr || snapshot == nullptr ||
        xSemaphoreTake(service->snapshot_mutex, kOwnerPollTicks) != pdTRUE) {
        return false;
    }
    *snapshot = service->latest_snapshot;
    xSemaphoreGive(service->snapshot_mutex);
    return true;
}
