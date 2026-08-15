#include "deck_serial_service.h"
#include "deck_serial_router.h"
#include "deck_serial_usb_bridge.h"
#include "sdkconfig.h"

#include <algorithm>
#include <atomic>
#include <cstring>
#include <new>

#include "driver/gpio.h"
#include "driver/uart.h"
#include "driver/usb_serial_jtag.h"
#include "esp_heap_caps.h"
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
// TX stays unbuffered so the sole Session owner can hand only immediately
// writable FIFO bytes to hardware and can clear unsent source queues during
// an owner transition.
constexpr int kTargetTxBufferBytes = 0;
constexpr size_t kUartEventQueueDepth = 20;
constexpr size_t kInputBlockCount = 16;
constexpr size_t kInputQueueDepth = kInputBlockCount;
constexpr size_t kUsbInputQueueDepth = 16;
constexpr size_t kWebInputQueueDepth = 16;
constexpr size_t kUsbDriverBufferBytes = 4U * 1024U;
constexpr size_t kUsbSinkBytes = 16U * 1024U;
constexpr size_t kWssSinkBytes = 16U * 1024U;
constexpr size_t kStatsSinkBytes = DECK_SERIAL_ROUTER_BLOCK_BYTES;
constexpr size_t kCommandQueueDepth = 16;
constexpr uint32_t kOwnerTaskStackBytes = 4'096;
constexpr uint32_t kRxTaskStackBytes = 3'584;
constexpr uint32_t kRouterTaskStackBytes = 3'584;
constexpr uint32_t kUsbTaskStackBytes = 3'584;
constexpr UBaseType_t kOwnerTaskPriority = 4;
constexpr UBaseType_t kRxTaskPriority = 7;
constexpr UBaseType_t kRouterTaskPriority = 6;
constexpr UBaseType_t kUsbTaskPriority = 3;
constexpr TickType_t kOwnerPollTicks = pdMS_TO_TICKS(100);
constexpr TickType_t kDataPollTicks = pdMS_TO_TICKS(20);
constexpr TickType_t kLifecycleTimeoutTicks = pdMS_TO_TICKS(2'000);
constexpr EventBits_t kReadyBit = BIT0;
constexpr EventBits_t kStoppedBit = BIT1;
constexpr EventBits_t kRxReadyBit = BIT0;
constexpr EventBits_t kRouterReadyBit = BIT1;
constexpr EventBits_t kRxStoppedBit = BIT2;
constexpr EventBits_t kRouterStoppedBit = BIT3;
constexpr EventBits_t kDataStartBit = BIT4;
constexpr EventBits_t kUsbOutputReadyBit = BIT5;
constexpr EventBits_t kUsbInputReadyBit = BIT6;
constexpr EventBits_t kUsbOutputStoppedBit = BIT7;
constexpr EventBits_t kUsbInputStoppedBit = BIT8;

#ifdef CONFIG_DECK_DIAGNOSTIC_CONSOLE
constexpr bool kUsbBridgeEnabled = false;
#else
constexpr bool kUsbBridgeEnabled = true;
#endif

#ifdef CONFIG_DECK_SERIAL_HISTORY_KIB
constexpr size_t kHistoryBytes =
    static_cast<size_t>(CONFIG_DECK_SERIAL_HISTORY_KIB) * 1024U;
#else
constexpr size_t kHistoryBytes = DECK_SERIAL_HISTORY_DEFAULT_BYTES;
#endif

static_assert(kHistoryBytes >= DECK_SERIAL_HISTORY_MIN_BYTES);
static_assert(kHistoryBytes <= DECK_SERIAL_HISTORY_MAX_BYTES);
static_assert(kHistoryBytes % DECK_SERIAL_ROUTER_BLOCK_BYTES == 0);

enum class CommandKind : uint8_t {
    enter,
    exit,
    request_web,
    web_activity,
    web_disconnect,
    revoke_web_transport,
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

struct UsbInputBlock {
    uint16_t length;
    uint64_t owner_generation;
    uint8_t bytes[DECK_SERIAL_ROUTER_BLOCK_BYTES];
};

struct WebInputBlock {
    uint16_t length;
    uint64_t session_id;
    uint64_t lease_id;
    uint8_t bytes[DECK_SERIAL_ROUTER_BLOCK_BYTES];
};

enum class InputDrainResult : uint8_t {
    idle,
    progress,
    stalled,
    rejected,
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

bool install_target_uart(void *context, uint64_t session_id);
bool uninstall_target_uart(void *context);
void clear_usb_tx(void *context);
void clear_web_tx(void *context);

}  // namespace

struct SerialDataPath {
    deck_serial_router_t *router;
    deck_serial_usb_bridge_t *usb_bridge;
    QueueHandle_t uart_events;
    QueueHandle_t free_blocks;
    QueueHandle_t filled_blocks;
    QueueHandle_t usb_input_blocks;
    QueueHandle_t web_input_blocks;
    EventGroupHandle_t lifecycle;
    TaskHandle_t rx_task;
    TaskHandle_t router_task;
    TaskHandle_t usb_output_task;
    TaskHandle_t usb_input_task;
    deck_serial_input_block_t *input_blocks;
    bool usb_driver_owned;
    std::atomic<bool> stop_requested;
};

struct deck_serial_service {
    QueueHandle_t commands;
    SemaphoreHandle_t snapshot_mutex;
    EventGroupHandle_t lifecycle;
    TaskHandle_t owner_task;
    deck_serial_session_t *session;
    deck_serial_service_event_fn callback;
    void *callback_context;
    deck_serial_session_snapshot_t latest_snapshot;
    deck_serial_command_result_t latest_command_result;
    bool has_latest_command_result;
    deck_serial_router_stats_t published_router_stats;
    bool has_published_router_stats;
    UsbInputBlock pending_usb_input;
    size_t pending_usb_offset;
    bool has_pending_usb_input;
    bool pending_usb_authorized;
    WebInputBlock pending_web_input;
    size_t pending_web_offset;
    bool has_pending_web_input;
    std::atomic<uint64_t> usb_input_authority_generation;
    std::atomic<uint64_t> pending_usb_rejected_bytes;
    std::atomic<bool> stop_requested;
    std::atomic<uint64_t> control_epoch;
    std::atomic<uint64_t> web_transport_epoch;
    std::atomic<uint64_t> completed_web_revoke_epoch;
    SemaphoreHandle_t router_mutex;
    SerialDataPath data_path;
};

namespace {

void *router_allocate(void *, size_t size, bool external_memory)
{
    const uint32_t capabilities =
        MALLOC_CAP_8BIT |
        (external_memory ? MALLOC_CAP_SPIRAM : MALLOC_CAP_INTERNAL);
    return heap_caps_calloc(1, size, capabilities);
}

void router_deallocate(void *, void *memory)
{
    heap_caps_free(memory);
}

void *usb_bridge_allocate(void *, size_t size)
{
    return heap_caps_calloc(1, size, MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
}

void usb_bridge_deallocate(void *, void *memory)
{
    heap_caps_free(memory);
}

bool usb_connected(void *)
{
    return usb_serial_jtag_is_connected();
}

deck_serial_router_copy_result_t take_usb_output(
    void *context,
    deck_serial_routed_block_t *block
)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service == nullptr || block == nullptr ||
        xSemaphoreTake(service->router_mutex, kDataPollTicks) != pdTRUE) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    const deck_serial_router_copy_result_t result =
        service->data_path.router == nullptr
            ? DECK_SERIAL_ROUTER_COPY_INVALID
            : deck_serial_router_take(
                  service->data_path.router,
                  DECK_SERIAL_SINK_USB,
                  block
              );
    xSemaphoreGive(service->router_mutex);
    return result;
}

int write_usb_output(void *, const uint8_t *bytes, size_t size)
{
    if (bytes == nullptr || size == 0 ||
        size > DECK_SERIAL_ROUTER_BLOCK_BYTES) {
        return -1;
    }
    return usb_serial_jtag_write_bytes(bytes, size, 0);
}

bool usb_input_ready(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    return service != nullptr &&
           !service->data_path.stop_requested.load(std::memory_order_acquire) &&
           service->data_path.usb_input_blocks != nullptr &&
           (service->usb_input_authority_generation.load(
                std::memory_order_acquire
            ) == 0 ||
            uxQueueSpacesAvailable(service->data_path.usb_input_blocks) != 0);
}

uint64_t usb_input_authority_generation(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    return service == nullptr
               ? 0
               : service->usb_input_authority_generation.load(
                     std::memory_order_acquire
                 );
}

int read_usb_input(void *, uint8_t *bytes, size_t capacity)
{
    if (bytes == nullptr || capacity == 0 ||
        capacity > DECK_SERIAL_ROUTER_BLOCK_BYTES) {
        return -1;
    }
    return usb_serial_jtag_read_bytes(
        bytes,
        static_cast<uint32_t>(capacity),
        kDataPollTicks
    );
}

bool submit_usb_input(
    void *context,
    const uint8_t *bytes,
    size_t size,
    uint64_t authority_generation
)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service == nullptr || bytes == nullptr || size == 0 ||
        size > DECK_SERIAL_ROUTER_BLOCK_BYTES ||
        service->data_path.usb_input_blocks == nullptr ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    if (authority_generation == 0) {
        uint64_t current = service->pending_usb_rejected_bytes.load(
            std::memory_order_relaxed
        );
        while (true) {
            const uint64_t amount = static_cast<uint64_t>(size);
            const uint64_t next =
                current > UINT64_MAX - amount ? UINT64_MAX : current + amount;
            if (service->pending_usb_rejected_bytes.compare_exchange_weak(
                    current,
                    next,
                    std::memory_order_release,
                    std::memory_order_relaxed
                )) {
                break;
            }
        }
        if (service->owner_task != nullptr) {
            xTaskNotifyGive(service->owner_task);
        }
        return true;
    }
    UsbInputBlock block{};
    block.length = static_cast<uint16_t>(size);
    block.owner_generation = authority_generation;
    std::memcpy(block.bytes, bytes, size);
    const bool submitted = xQueueSend(
        service->data_path.usb_input_blocks,
        &block,
        0
    ) == pdTRUE;
    std::memset(&block, 0, sizeof(block));
    if (submitted && service->owner_task != nullptr) {
        xTaskNotifyGive(service->owner_task);
    }
    return submitted;
}

void note_uart_error(
    deck_serial_service_t *service,
    deck_serial_uart_error_t error
)
{
    if (service->data_path.router != nullptr) {
        deck_serial_router_note_uart_error(service->data_path.router, error);
    }
}

void return_input_block(
    deck_serial_service_t *service,
    deck_serial_input_block_t *block
)
{
    if (block == nullptr) {
        return;
    }
    block->length = 0;
    block->monotonic_ms = 0;
    std::memset(block->bytes, 0, sizeof(block->bytes));
    if (xQueueSend(service->data_path.free_blocks, &block, 0) != pdTRUE) {
        note_uart_error(service, DECK_SERIAL_UART_ROUTER_STARVED);
    }
}

void drain_unrouted_uart_bytes(
    deck_serial_service_t *service,
    size_t remaining
)
{
    uint8_t discarded[DECK_SERIAL_ROUTER_BLOCK_BYTES]{};
    while (remaining != 0 &&
           !service->data_path.stop_requested.load(std::memory_order_acquire)) {
        const size_t requested = std::min(remaining, sizeof(discarded));
        const int received = uart_read_bytes(
            kTargetUart,
            discarded,
            static_cast<uint32_t>(requested),
            0
        );
        if (received <= 0) {
            break;
        }
        remaining -= static_cast<size_t>(received);
    }
    std::memset(discarded, 0, sizeof(discarded));
    note_uart_error(service, DECK_SERIAL_UART_ROUTER_STARVED);
}

void receive_uart_data(deck_serial_service_t *service, size_t remaining)
{
    while (remaining != 0 &&
           !service->data_path.stop_requested.load(std::memory_order_acquire)) {
        deck_serial_input_block_t *block = nullptr;
        if (xQueueReceive(service->data_path.free_blocks, &block, 0) != pdTRUE) {
            drain_unrouted_uart_bytes(service, remaining);
            return;
        }
        const size_t requested =
            std::min(remaining, sizeof(block->bytes));
        const int received = uart_read_bytes(
            kTargetUart,
            block->bytes,
            static_cast<uint32_t>(requested),
            0
        );
        if (received <= 0) {
            return_input_block(service, block);
            return;
        }
        block->length = static_cast<uint16_t>(received);
        block->monotonic_ms = monotonic_ms();
        remaining -= static_cast<size_t>(received);
        if (xQueueSend(service->data_path.filled_blocks, &block, 0) != pdTRUE) {
            note_uart_error(service, DECK_SERIAL_UART_ROUTER_STARVED);
            return_input_block(service, block);
        }
    }
}

void uart_rx_task(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    xEventGroupSetBits(service->data_path.lifecycle, kRxReadyBit);
    while (!service->data_path.stop_requested.load(std::memory_order_acquire) &&
           (xEventGroupWaitBits(
                service->data_path.lifecycle,
                kDataStartBit,
                pdFALSE,
                pdTRUE,
                kDataPollTicks
            ) & kDataStartBit) == 0) {
    }
    while (!service->data_path.stop_requested.load(std::memory_order_acquire)) {
        uart_event_t event{};
        if (xQueueReceive(
                service->data_path.uart_events,
                &event,
                kDataPollTicks
            ) != pdTRUE) {
            continue;
        }
        switch (event.type) {
            case UART_DATA:
                receive_uart_data(service, event.size);
                break;
            case UART_FIFO_OVF:
                note_uart_error(service, DECK_SERIAL_UART_FIFO_OVERFLOW);
                (void)uart_flush_input(kTargetUart);
                (void)xQueueReset(service->data_path.uart_events);
                break;
            case UART_BUFFER_FULL:
                note_uart_error(service, DECK_SERIAL_UART_DRIVER_BUFFER_FULL);
                (void)uart_flush_input(kTargetUart);
                (void)xQueueReset(service->data_path.uart_events);
                break;
            default:
                break;
        }
    }
    xEventGroupSetBits(service->data_path.lifecycle, kRxStoppedBit);
    vTaskSuspend(nullptr);
}

void router_task(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    xEventGroupSetBits(service->data_path.lifecycle, kRouterReadyBit);
    while (!service->data_path.stop_requested.load(std::memory_order_acquire) &&
           (xEventGroupWaitBits(
                service->data_path.lifecycle,
                kDataStartBit,
                pdFALSE,
                pdTRUE,
                kDataPollTicks
            ) & kDataStartBit) == 0) {
    }
    while (!service->data_path.stop_requested.load(std::memory_order_acquire)) {
        deck_serial_input_block_t *block = nullptr;
        if (xQueueReceive(
                service->data_path.filled_blocks,
                &block,
                kDataPollTicks
            ) != pdTRUE) {
            continue;
        }
        if (!deck_serial_router_submit(service->data_path.router, block, nullptr)) {
            note_uart_error(service, DECK_SERIAL_UART_ROUTER_STARVED);
        }
        return_input_block(service, block);
    }
    xEventGroupSetBits(service->data_path.lifecycle, kRouterStoppedBit);
    vTaskSuspend(nullptr);
}

void usb_output_task(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    xEventGroupSetBits(service->data_path.lifecycle, kUsbOutputReadyBit);
    while (!service->data_path.stop_requested.load(std::memory_order_acquire) &&
           (xEventGroupWaitBits(
                service->data_path.lifecycle,
                kDataStartBit,
                pdFALSE,
                pdTRUE,
                kDataPollTicks
            ) & kDataStartBit) == 0) {
    }
    while (!service->data_path.stop_requested.load(std::memory_order_acquire)) {
        const deck_serial_usb_pump_result_t result =
            deck_serial_usb_bridge_pump_output(
                service->data_path.usb_bridge
            );
        if (result == DECK_SERIAL_USB_PROGRESS) {
            taskYIELD();
        } else {
            vTaskDelay(kDataPollTicks);
        }
    }
    xEventGroupSetBits(service->data_path.lifecycle, kUsbOutputStoppedBit);
    vTaskSuspend(nullptr);
}

void usb_input_task(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    xEventGroupSetBits(service->data_path.lifecycle, kUsbInputReadyBit);
    while (!service->data_path.stop_requested.load(std::memory_order_acquire) &&
           (xEventGroupWaitBits(
                service->data_path.lifecycle,
                kDataStartBit,
                pdFALSE,
                pdTRUE,
                kDataPollTicks
            ) & kDataStartBit) == 0) {
    }
    while (!service->data_path.stop_requested.load(std::memory_order_acquire)) {
        const deck_serial_usb_pump_result_t result =
            deck_serial_usb_bridge_pump_input(
                service->data_path.usb_bridge
            );
        if (result == DECK_SERIAL_USB_PROGRESS) {
            taskYIELD();
        } else {
            vTaskDelay(kDataPollTicks);
        }
    }
    xEventGroupSetBits(service->data_path.lifecycle, kUsbInputStoppedBit);
    vTaskSuspend(nullptr);
}

void clear_pending_usb_input(deck_serial_service_t *service)
{
    std::memset(
        &service->pending_usb_input,
        0,
        sizeof(service->pending_usb_input)
    );
    service->pending_usb_offset = 0;
    service->has_pending_usb_input = false;
    service->pending_usb_authorized = false;
}

void clear_usb_input_state(deck_serial_service_t *service)
{
    if (service->data_path.usb_input_blocks != nullptr) {
        (void)xQueueReset(service->data_path.usb_input_blocks);
    }
    clear_pending_usb_input(service);
}

void clear_pending_web_input(deck_serial_service_t *service)
{
    std::memset(
        &service->pending_web_input,
        0,
        sizeof(service->pending_web_input)
    );
    service->pending_web_offset = 0;
    service->has_pending_web_input = false;
}

void clear_web_input_state(deck_serial_service_t *service)
{
    if (service->data_path.web_input_blocks != nullptr) {
        (void)xQueueReset(service->data_path.web_input_blocks);
    }
    clear_pending_web_input(service);
}

void release_data_path_allocations(deck_serial_service_t *service)
{
    SerialDataPath &path = service->data_path;
    service->usb_input_authority_generation.store(
        0,
        std::memory_order_release
    );
    service->pending_usb_rejected_bytes.store(0, std::memory_order_release);
    clear_usb_input_state(service);
    clear_web_input_state(service);
    if (path.usb_bridge != nullptr) {
        deck_serial_usb_bridge_destroy(path.usb_bridge);
        path.usb_bridge = nullptr;
    }
    if (path.router != nullptr) {
        deck_serial_router_destroy(path.router);
        path.router = nullptr;
    }
    if (path.free_blocks != nullptr) {
        vQueueDelete(path.free_blocks);
        path.free_blocks = nullptr;
    }
    if (path.filled_blocks != nullptr) {
        vQueueDelete(path.filled_blocks);
        path.filled_blocks = nullptr;
    }
    if (path.usb_input_blocks != nullptr) {
        vQueueDelete(path.usb_input_blocks);
        path.usb_input_blocks = nullptr;
    }
    if (path.web_input_blocks != nullptr) {
        vQueueDelete(path.web_input_blocks);
        path.web_input_blocks = nullptr;
    }
    if (path.lifecycle != nullptr) {
        vEventGroupDelete(path.lifecycle);
        path.lifecycle = nullptr;
    }
    if (path.input_blocks != nullptr) {
        std::memset(
            path.input_blocks,
            0,
            kInputBlockCount * sizeof(deck_serial_input_block_t)
        );
        heap_caps_free(path.input_blocks);
        path.input_blocks = nullptr;
    }
    path.uart_events = nullptr;
    path.usb_driver_owned = false;
    path.stop_requested.store(false, std::memory_order_release);
}

bool stop_data_path_tasks(deck_serial_service_t *service)
{
    SerialDataPath &path = service->data_path;
    path.stop_requested.store(true, std::memory_order_release);
    EventBits_t required = 0;
    if (path.rx_task != nullptr) {
        required |= kRxStoppedBit;
    }
    if (path.router_task != nullptr) {
        required |= kRouterStoppedBit;
    }
    if (path.usb_output_task != nullptr) {
        required |= kUsbOutputStoppedBit;
    }
    if (path.usb_input_task != nullptr) {
        required |= kUsbInputStoppedBit;
    }
    if (required != 0) {
        const EventBits_t stopped = xEventGroupWaitBits(
            path.lifecycle,
            required,
            pdFALSE,
            pdTRUE,
            kLifecycleTimeoutTicks
        );
        if ((stopped & required) != required) {
            return false;
        }
    }
    if (path.rx_task != nullptr) {
        vTaskDelete(path.rx_task);
        path.rx_task = nullptr;
    }
    if (path.router_task != nullptr) {
        vTaskDelete(path.router_task);
        path.router_task = nullptr;
    }
    if (path.usb_output_task != nullptr) {
        vTaskDelete(path.usb_output_task);
        path.usb_output_task = nullptr;
    }
    if (path.usb_input_task != nullptr) {
        vTaskDelete(path.usb_input_task);
        path.usb_input_task = nullptr;
    }
    return true;
}

bool register_data_sinks(deck_serial_router_t *router)
{
    const deck_serial_sink_config_t sinks[] = {
        {DECK_SERIAL_SINK_USB, kUsbSinkBytes, false},
        {DECK_SERIAL_SINK_WSS, kWssSinkBytes, false},
        {DECK_SERIAL_SINK_HISTORY, kHistoryBytes, true},
        {DECK_SERIAL_SINK_STATS, kStatsSinkBytes, false},
    };
    for (const deck_serial_sink_config_t &sink : sinks) {
        if (!deck_serial_router_register_sink(router, &sink)) {
            return false;
        }
    }
    return true;
}

bool prepare_data_path(deck_serial_service_t *service, uint64_t session_id)
{
    SerialDataPath &path = service->data_path;
    if (path.router != nullptr || path.uart_events != nullptr ||
        path.usb_bridge != nullptr || path.rx_task != nullptr ||
        path.router_task != nullptr || path.usb_output_task != nullptr ||
        path.usb_input_task != nullptr) {
        return false;
    }
    service->usb_input_authority_generation.store(
        0,
        std::memory_order_release
    );
    service->pending_usb_rejected_bytes.store(0, std::memory_order_release);
    path.stop_requested.store(false, std::memory_order_release);
    path.lifecycle = xEventGroupCreate();
    path.free_blocks = xQueueCreate(kInputQueueDepth, sizeof(void *));
    path.filled_blocks = xQueueCreate(kInputQueueDepth, sizeof(void *));
    path.web_input_blocks = xQueueCreate(
        kWebInputQueueDepth,
        sizeof(WebInputBlock)
    );
    if constexpr (kUsbBridgeEnabled) {
        path.usb_input_blocks = xQueueCreate(
            kUsbInputQueueDepth,
            sizeof(UsbInputBlock)
        );
    }
    path.input_blocks = static_cast<deck_serial_input_block_t *>(heap_caps_calloc(
        kInputBlockCount,
        sizeof(deck_serial_input_block_t),
        MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT
    ));
    const deck_serial_router_config_t router_config = {
        session_id,
        kHistoryBytes,
        0,
        {router_allocate, router_deallocate, nullptr},
    };
    path.router = deck_serial_router_create(&router_config);
    if constexpr (kUsbBridgeEnabled) {
        const deck_serial_usb_bridge_config_t usb_config = {
            {usb_connected, take_usb_output, write_usb_output,
             usb_input_ready, usb_input_authority_generation, read_usb_input,
             submit_usb_input, service},
            {usb_bridge_allocate, usb_bridge_deallocate, nullptr},
        };
        path.usb_bridge = deck_serial_usb_bridge_create(&usb_config);
    }
    if (path.lifecycle == nullptr || path.free_blocks == nullptr ||
        path.filled_blocks == nullptr || path.input_blocks == nullptr ||
        path.router == nullptr || path.web_input_blocks == nullptr ||
        (kUsbBridgeEnabled &&
         (path.usb_input_blocks == nullptr || path.usb_bridge == nullptr)) ||
        !register_data_sinks(path.router)) {
        release_data_path_allocations(service);
        return false;
    }
    for (size_t index = 0; index < kInputBlockCount; ++index) {
        deck_serial_input_block_t *block = &path.input_blocks[index];
        if (xQueueSend(path.free_blocks, &block, 0) != pdTRUE) {
            release_data_path_allocations(service);
            return false;
        }
    }
    return true;
}

void abort_data_path_start(deck_serial_service_t *service)
{
    SerialDataPath &path = service->data_path;
    path.stop_requested.store(true, std::memory_order_release);
    (void)stop_data_path_tasks(service);
    // The start bit was never published, so any task that missed the bounded
    // join cannot have entered UART/Router work or acquired Router state.
    if (path.rx_task != nullptr) {
        vTaskDelete(path.rx_task);
        path.rx_task = nullptr;
    }
    if (path.router_task != nullptr) {
        vTaskDelete(path.router_task);
        path.router_task = nullptr;
    }
    if (path.usb_output_task != nullptr) {
        vTaskDelete(path.usb_output_task);
        path.usb_output_task = nullptr;
    }
    if (path.usb_input_task != nullptr) {
        vTaskDelete(path.usb_input_task);
        path.usb_input_task = nullptr;
    }
    if (path.usb_driver_owned) {
        if (usb_serial_jtag_driver_uninstall() != ESP_OK) {
            // Keep every allocation and ownership flag intact. The Session
            // installation-failure path immediately calls uninstall_uart(),
            // which can retry the same bounded cleanup without losing the
            // driver owner.
            (void)set_target_tx_high_impedance(nullptr);
            return;
        }
        path.usb_driver_owned = false;
    }
    if (uart_is_driver_installed(kTargetUart) &&
        uart_driver_delete(kTargetUart) != ESP_OK) {
        (void)set_target_tx_high_impedance(nullptr);
        return;
    }
    path.uart_events = nullptr;
    release_data_path_allocations(service);
    (void)set_target_tx_high_impedance(nullptr);
}

bool install_target_uart_locked(
    deck_serial_service_t *service,
    uint64_t session_id
)
{
    if (!prepare_data_path(service, session_id)) {
        return false;
    }
    SerialDataPath &path = service->data_path;
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
            kUartEventQueueDepth,
            &path.uart_events,
            0
        ) != ESP_OK ||
        uart_param_config(kTargetUart, &config) != ESP_OK ||
        uart_set_pin(
            kTargetUart,
            kTargetTx,
            kTargetRx,
            UART_PIN_NO_CHANGE,
            UART_PIN_NO_CHANGE
        ) != ESP_OK) {
        abort_data_path_start(service);
        return false;
    }
    if constexpr (kUsbBridgeEnabled) {
        usb_serial_jtag_driver_config_t usb_config = {
            static_cast<uint32_t>(kUsbDriverBufferBytes),
            static_cast<uint32_t>(kUsbDriverBufferBytes),
        };
        if (usb_serial_jtag_driver_install(&usb_config) != ESP_OK) {
            abort_data_path_start(service);
            return false;
        }
        path.usb_driver_owned = true;
    }
    if (xTaskCreatePinnedToCore(
            router_task,
            "serial_router",
            kRouterTaskStackBytes,
            service,
            kRouterTaskPriority,
            &path.router_task,
            1
        ) != pdPASS ||
        xTaskCreatePinnedToCore(
            uart_rx_task,
            "serial_rx",
            kRxTaskStackBytes,
            service,
            kRxTaskPriority,
            &path.rx_task,
            1
        ) != pdPASS) {
        abort_data_path_start(service);
        return false;
    }
    if constexpr (kUsbBridgeEnabled) {
        if (xTaskCreatePinnedToCore(
                usb_output_task,
                "serial_usb_out",
                kUsbTaskStackBytes,
                service,
                kUsbTaskPriority,
                &path.usb_output_task,
                1
            ) != pdPASS ||
            xTaskCreatePinnedToCore(
                usb_input_task,
                "serial_usb_in",
                kUsbTaskStackBytes,
                service,
                kUsbTaskPriority,
                &path.usb_input_task,
                1
            ) != pdPASS) {
            abort_data_path_start(service);
            return false;
        }
    }
    const EventBits_t required_ready =
        kRxReadyBit | kRouterReadyBit |
        (kUsbBridgeEnabled
             ? kUsbOutputReadyBit | kUsbInputReadyBit
             : static_cast<EventBits_t>(0));
    const EventBits_t ready = xEventGroupWaitBits(
        path.lifecycle,
        required_ready,
        pdFALSE,
        pdTRUE,
        kLifecycleTimeoutTicks
    );
    if ((ready & required_ready) != required_ready) {
        abort_data_path_start(service);
        return false;
    }
    xEventGroupSetBits(path.lifecycle, kDataStartBit);
    return true;
}

bool install_target_uart(void *context, uint64_t session_id)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service == nullptr ||
        xSemaphoreTake(service->router_mutex, kLifecycleTimeoutTicks) != pdTRUE) {
        return false;
    }
    const bool installed = install_target_uart_locked(service, session_id);
    xSemaphoreGive(service->router_mutex);
    return installed;
}

bool uninstall_target_uart(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service == nullptr) {
        return false;
    }
    SerialDataPath &path = service->data_path;
    if (!stop_data_path_tasks(service)) {
        return false;
    }
    if (path.usb_driver_owned) {
        if (usb_serial_jtag_driver_uninstall() != ESP_OK) {
            return false;
        }
        path.usb_driver_owned = false;
    }
    if (uart_is_driver_installed(kTargetUart) &&
        uart_driver_delete(kTargetUart) != ESP_OK) {
        return false;
    }
    path.uart_events = nullptr;
    if (xSemaphoreTake(service->router_mutex, kLifecycleTimeoutTicks) != pdTRUE) {
        return false;
    }
    release_data_path_allocations(service);
    xSemaphoreGive(service->router_mutex);
    return set_target_tx_high_impedance(nullptr);
}

// Source queues are separate from Router target-RX sinks. Owner transitions
// clear only unsent host -> target bytes; target output survives reconnects.
void clear_usb_tx(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service != nullptr) {
        clear_usb_input_state(service);
    }
}

void clear_web_tx(void *context)
{
    auto *service = static_cast<deck_serial_service_t *>(context);
    if (service != nullptr) {
        clear_web_input_state(service);
    }
}

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
    // A transient Router owner lock timeout must not clear a previously
    // published data-loss alert. Successful observation of a missing Router
    // (normal session exit) is what clears the current-session counters.
    event.has_router_stats = service->has_published_router_stats;
    event.router_stats = service->published_router_stats;
    if (xSemaphoreTake(service->router_mutex, kOwnerPollTicks) == pdTRUE) {
        if (service->data_path.router == nullptr) {
            event.has_router_stats = false;
            event.router_stats = {};
        } else {
            event.has_router_stats = deck_serial_router_stats(
                service->data_path.router,
                &event.router_stats
            );
        }
        xSemaphoreGive(service->router_mutex);
    }
    service->published_router_stats = event.router_stats;
    service->has_published_router_stats = event.has_router_stats;
    if (xSemaphoreTake(service->snapshot_mutex, portMAX_DELAY) == pdTRUE) {
        service->latest_snapshot = event.snapshot;
        if (result != nullptr) {
            service->latest_command_result = *result;
            service->has_latest_command_result = true;
        }
        xSemaphoreGive(service->snapshot_mutex);
    }
    if (service->callback != nullptr) {
        service->callback(service->callback_context, &event);
    }
}

bool router_alert_changed(deck_serial_service_t *service)
{
    deck_serial_router_stats_t current{};
    bool has_current = false;
    if (xSemaphoreTake(service->router_mutex, kOwnerPollTicks) == pdTRUE) {
        has_current =
            service->data_path.router != nullptr &&
            deck_serial_router_stats(service->data_path.router, &current);
        xSemaphoreGive(service->router_mutex);
    }
    return has_current != service->has_published_router_stats ||
           (has_current &&
            (current.uart_fifo_overflows !=
                 service->published_router_stats.uart_fifo_overflows ||
             current.uart_driver_buffer_full !=
                 service->published_router_stats.uart_driver_buffer_full));
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

void sync_usb_input_authority(deck_serial_service_t *service)
{
    deck_serial_session_snapshot_t current{};
    const bool authorized =
        service->session != nullptr &&
        deck_serial_session_snapshot(service->session, &current) &&
        current.state == DECK_SERIAL_USB_TX && current.uart_installed;
    service->usb_input_authority_generation.store(
        authorized ? current.owner_generation : 0,
        std::memory_order_release
    );
}

bool drain_usb_rejections(deck_serial_service_t *service)
{
    const uint64_t rejected = service->pending_usb_rejected_bytes.exchange(
        0,
        std::memory_order_acq_rel
    );
    if (rejected == 0) {
        return false;
    }
    return deck_serial_session_record_usb_rejection(
        service->session,
        rejected
    );
}

InputDrainResult drain_usb_input(deck_serial_service_t *service)
{
    if (!kUsbBridgeEnabled || service->data_path.usb_input_blocks == nullptr) {
        return InputDrainResult::idle;
    }
    if (!service->has_pending_usb_input) {
        if (xQueueReceive(
                service->data_path.usb_input_blocks,
                &service->pending_usb_input,
                0
            ) != pdTRUE) {
            return InputDrainResult::idle;
        }
        if (service->pending_usb_input.length == 0 ||
            service->pending_usb_input.length >
                DECK_SERIAL_ROUTER_BLOCK_BYTES) {
            clear_pending_usb_input(service);
            return InputDrainResult::rejected;
        }
        service->has_pending_usb_input = true;
    }
    if (!service->pending_usb_authorized) {
        if (!deck_serial_session_accept_usb_input_generation(
                service->session,
                service->pending_usb_input.owner_generation,
                service->pending_usb_input.length
            )) {
            clear_pending_usb_input(service);
            return InputDrainResult::rejected;
        }
        service->pending_usb_authorized = true;
    }
    const size_t remaining =
        static_cast<size_t>(service->pending_usb_input.length) -
        service->pending_usb_offset;
    const int written = uart_tx_chars(
        kTargetUart,
        reinterpret_cast<const char *>(
            service->pending_usb_input.bytes + service->pending_usb_offset
        ),
        static_cast<uint32_t>(remaining)
    );
    if (written < 0 || static_cast<size_t>(written) > remaining) {
        clear_pending_usb_input(service);
        return InputDrainResult::rejected;
    }
    if (written == 0) {
        return InputDrainResult::stalled;
    }
    service->pending_usb_offset += static_cast<size_t>(written);
    if (service->pending_usb_offset == service->pending_usb_input.length) {
        clear_pending_usb_input(service);
    }
    return InputDrainResult::progress;
}

InputDrainResult drain_web_input(deck_serial_service_t *service)
{
    if (service->data_path.web_input_blocks == nullptr) {
        return InputDrainResult::idle;
    }
    if (!service->has_pending_web_input) {
        if (xQueueReceive(
                service->data_path.web_input_blocks,
                &service->pending_web_input,
                0
            ) != pdTRUE) {
            return InputDrainResult::idle;
        }
        if (service->pending_web_input.length == 0 ||
            service->pending_web_input.length >
                DECK_SERIAL_ROUTER_BLOCK_BYTES) {
            clear_pending_web_input(service);
            return InputDrainResult::rejected;
        }
        service->has_pending_web_input = true;
    }
    if (!deck_serial_session_accept_web_input(
            service->session,
            service->pending_web_input.session_id,
            service->pending_web_input.lease_id
        )) {
        clear_pending_web_input(service);
        return InputDrainResult::rejected;
    }
    const size_t remaining =
        static_cast<size_t>(service->pending_web_input.length) -
        service->pending_web_offset;
    const int written = uart_tx_chars(
        kTargetUart,
        reinterpret_cast<const char *>(
            service->pending_web_input.bytes + service->pending_web_offset
        ),
        static_cast<uint32_t>(remaining)
    );
    if (written < 0 || static_cast<size_t>(written) > remaining) {
        clear_pending_web_input(service);
        return InputDrainResult::rejected;
    }
    if (written == 0) {
        return InputDrainResult::stalled;
    }
    service->pending_web_offset += static_cast<size_t>(written);
    if (service->pending_web_offset == service->pending_web_input.length) {
        clear_pending_web_input(service);
    }
    return InputDrainResult::progress;
}

void process_command(deck_serial_service_t *service, const Command &command)
{
    deck_serial_command_result_t result{};
    bool has_result = false;
    const uint64_t now_ms = monotonic_ms();
    // Expiry must advance even when control producers keep the queue busy.
    deck_serial_session_tick(service->session, now_ms);
    if (command.kind == CommandKind::enter ||
        command.kind == CommandKind::exit ||
        command.kind == CommandKind::request_web ||
        command.kind == CommandKind::web_disconnect ||
        command.kind == CommandKind::revoke_web_transport ||
        command.kind == CommandKind::stop) {
        service->usb_input_authority_generation.store(
            0,
            std::memory_order_release
        );
    }
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
            if (command.control_epoch == service->web_transport_epoch.load(
                                             std::memory_order_acquire
                                         )) {
                has_result = deck_serial_session_request_web_at_epoch(
                    service->session,
                    command.control_epoch,
                    command.session_id,
                    command.request_id,
                    command.enable,
                    now_ms,
                    &result
                );
            }
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
        case CommandKind::revoke_web_transport:
            has_result = deck_serial_session_revoke_web_transport(
                service->session,
                command.control_epoch,
                &result
            );
            if (has_result) {
                service->completed_web_revoke_epoch.store(
                    command.control_epoch,
                    std::memory_order_release
                );
            }
            break;
        case CommandKind::stop:
            has_result = deck_serial_session_exit(
                service->session,
                command.control_epoch,
                &result
            );
            break;
    }
    sync_usb_input_authority(service);
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
         clear_usb_tx, clear_web_tx, service},
        DECK_SERIAL_DEFAULT_WEB_LEASE_MS,
    };
    service->session = deck_serial_session_create(&config);
    if (service->session == nullptr) {
        xEventGroupSetBits(service->lifecycle, kStoppedBit);
        vTaskSuspend(nullptr);
    }
    sync_usb_input_authority(service);
    xEventGroupSetBits(service->lifecycle, kReadyBit);
    publish(service, nullptr);

    uint64_t next_router_alert_poll_ms = monotonic_ms();
    while (true) {
        const uint64_t now_ms = monotonic_ms();
        deck_serial_session_snapshot_t before{};
        deck_serial_session_snapshot_t after{};
        (void)deck_serial_session_snapshot(service->session, &before);
        deck_serial_session_tick(service->session, now_ms);
        (void)deck_serial_session_snapshot(service->session, &after);
        sync_usb_input_authority(service);
        const bool poll_router = now_ms >= next_router_alert_poll_ms;
        const bool alert_changed = poll_router && router_alert_changed(service);
        if (poll_router) {
            next_router_alert_poll_ms = now_ms + 100U;
        }
        if (snapshot_changed(before, after) || alert_changed) {
            publish(service, nullptr);
        }

        Command command{};
        if (xQueueReceive(service->commands, &command, 0) == pdTRUE) {
            process_command(service, command);
            if (command.kind == CommandKind::stop) {
                deck_serial_session_snapshot_t state{};
                if (deck_serial_session_snapshot(service->session, &state) &&
                    !state.uart_installed &&
                    deck_serial_session_destroy(service->session)) {
                    service->session = nullptr;
                    xEventGroupSetBits(service->lifecycle, kStoppedBit);
                    vTaskSuspend(nullptr);
                }
            }
            continue;
        }

        if (drain_usb_rejections(service)) {
            publish(service, nullptr);
            continue;
        }

        const InputDrainResult web_drained = drain_web_input(service);
        if (web_drained == InputDrainResult::rejected) {
            publish(service, nullptr);
            continue;
        }
        if (web_drained == InputDrainResult::progress) {
            continue;
        }
        const InputDrainResult usb_drained = drain_usb_input(service);
        if (usb_drained == InputDrainResult::rejected) {
            publish(service, nullptr);
            continue;
        }
        if (usb_drained == InputDrainResult::progress) {
            continue;
        }
        const bool stalled = web_drained == InputDrainResult::stalled ||
                             usb_drained == InputDrainResult::stalled;
        const TickType_t wait =
            stalled ? pdMS_TO_TICKS(1) : kOwnerPollTicks;
        (void)ulTaskNotifyTake(pdTRUE, wait);
    }
}

bool enqueue(deck_serial_service_t *service, const Command &command)
{
    if (service == nullptr ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    Command stamped = command;
    stamped.control_epoch =
        command.kind == CommandKind::request_web
            ? service->web_transport_epoch.load(std::memory_order_acquire)
            : service->control_epoch.load(std::memory_order_acquire);
    if (xQueueSend(service->commands, &stamped, 0) != pdTRUE) {
        return false;
    }
    if (service->owner_task != nullptr) {
        xTaskNotifyGive(service->owner_task);
    }
    return true;
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

uint64_t advance_web_transport_epoch(deck_serial_service_t *service)
{
    uint64_t current = service->web_transport_epoch.load(std::memory_order_acquire);
    while (true) {
        uint64_t next = current + 1U;
        if (next == 0) {
            next = 1;
        }
        if (service->web_transport_epoch.compare_exchange_weak(
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
        if (deck_serial_session_destroy(service->session)) {
            service->session = nullptr;
        }
    }
    if (service->commands != nullptr) {
        vQueueDelete(service->commands);
    }
    if (service->snapshot_mutex != nullptr) {
        vSemaphoreDelete(service->snapshot_mutex);
    }
    if (service->router_mutex != nullptr) {
        vSemaphoreDelete(service->router_mutex);
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
    service->router_mutex = xSemaphoreCreateMutex();
    service->lifecycle = xEventGroupCreate();
    service->stop_requested.store(false, std::memory_order_release);
    service->control_epoch.store(0, std::memory_order_release);
    service->web_transport_epoch.store(0, std::memory_order_release);
    service->completed_web_revoke_epoch.store(0, std::memory_order_release);
    if (service->commands == nullptr || service->snapshot_mutex == nullptr ||
        service->router_mutex == nullptr || service->lifecycle == nullptr ||
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
    const EventBits_t already_stopped = xEventGroupGetBits(service->lifecycle);
    if ((already_stopped & kStoppedBit) == 0) {
        bool expected = false;
        const bool first_request = service->stop_requested.compare_exchange_strong(
            expected,
            true,
            std::memory_order_acq_rel
        );
        const uint64_t control_epoch =
            first_request ? advance_control_epoch(service)
                          : service->control_epoch.load(std::memory_order_acquire);
        const Command command{
            CommandKind::stop, false, 0, 0, 0, control_epoch
        };
        if (xQueueSendToFront(
            service->commands,
            &command,
            kOwnerPollTicks
            ) != pdTRUE) {
            if (first_request) {
                service->stop_requested.store(false, std::memory_order_release);
            }
            return false;
        }
        if (service->owner_task != nullptr) {
            xTaskNotifyGive(service->owner_task);
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
    vSemaphoreDelete(service->router_mutex);
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
    if (xQueueSendToFront(
            service->commands,
            &command,
            kOwnerPollTicks
        ) != pdTRUE) {
        return false;
    }
    if (service->owner_task != nullptr) {
        xTaskNotifyGive(service->owner_task);
    }
    return true;
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

bool deck_serial_service_revoke_web_transport(
    deck_serial_service_t *service,
    uint64_t *revoke_epoch
)
{
    if (service == nullptr || revoke_epoch == nullptr ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    const uint64_t epoch = advance_web_transport_epoch(service);
    const Command command{
        CommandKind::revoke_web_transport, false, 0, 0, 0, epoch
    };
    if (xQueueSendToFront(
            service->commands,
            &command,
            kOwnerPollTicks
        ) != pdTRUE) {
        return false;
    }
    *revoke_epoch = epoch;
    if (service->owner_task != nullptr) {
        xTaskNotifyGive(service->owner_task);
    }
    return true;
}

bool deck_serial_service_web_transport_revoked(
    deck_serial_service_t *service,
    uint64_t revoke_epoch
)
{
    return service != nullptr && revoke_epoch != 0 &&
           service->completed_web_revoke_epoch.load(
               std::memory_order_acquire
           ) == revoke_epoch;
}

bool deck_serial_service_submit_web(
    deck_serial_service_t *service,
    uint64_t session_id,
    uint64_t lease_id,
    const uint8_t *bytes,
    size_t size
)
{
    if (service == nullptr || bytes == nullptr || size == 0 ||
        size > DECK_SERIAL_ROUTER_BLOCK_BYTES || session_id == 0 ||
        lease_id == 0 ||
        service->stop_requested.load(std::memory_order_acquire)) {
        return false;
    }
    deck_serial_session_snapshot_t snapshot{};
    if (!deck_serial_service_snapshot(service, &snapshot) ||
        snapshot.state != DECK_SERIAL_WEB_TX || !snapshot.uart_installed ||
        snapshot.session_id != session_id || snapshot.lease_id != lease_id) {
        return false;
    }
    WebInputBlock block{};
    block.length = static_cast<uint16_t>(size);
    block.session_id = session_id;
    block.lease_id = lease_id;
    std::memcpy(block.bytes, bytes, size);
    bool submitted = false;
    if (xSemaphoreTake(service->router_mutex, kOwnerPollTicks) == pdTRUE) {
        submitted = service->data_path.web_input_blocks != nullptr &&
                    xQueueSend(
                        service->data_path.web_input_blocks,
                        &block,
                        0
                    ) == pdTRUE;
        xSemaphoreGive(service->router_mutex);
    }
    std::memset(&block, 0, sizeof(block));
    if (submitted && service->owner_task != nullptr) {
        xTaskNotifyGive(service->owner_task);
    }
    return submitted;
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

bool deck_serial_service_command_result(
    deck_serial_service_t *service,
    uint64_t request_id,
    deck_serial_command_result_t *result
)
{
    if (service == nullptr || request_id == 0 || result == nullptr ||
        xSemaphoreTake(service->snapshot_mutex, kOwnerPollTicks) != pdTRUE) {
        return false;
    }
    const bool matches = service->has_latest_command_result &&
                         service->latest_command_result.request_id == request_id;
    if (matches) {
        *result = service->latest_command_result;
    }
    xSemaphoreGive(service->snapshot_mutex);
    return matches;
}

deck_serial_router_copy_result_t deck_serial_service_take(
    deck_serial_service_t *service,
    deck_serial_sink_id_t sink,
    deck_serial_routed_block_t *block
)
{
    if (service == nullptr || block == nullptr ||
        xSemaphoreTake(service->router_mutex, kOwnerPollTicks) != pdTRUE) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    const deck_serial_router_copy_result_t result =
        service->data_path.router == nullptr
            ? DECK_SERIAL_ROUTER_COPY_INVALID
            : deck_serial_router_take(service->data_path.router, sink, block);
    xSemaphoreGive(service->router_mutex);
    return result;
}

deck_serial_router_copy_result_t deck_serial_service_copy_history_after(
    deck_serial_service_t *service,
    uint64_t after_sequence,
    deck_serial_routed_block_t *block
)
{
    if (service == nullptr || block == nullptr ||
        xSemaphoreTake(service->router_mutex, kOwnerPollTicks) != pdTRUE) {
        return DECK_SERIAL_ROUTER_COPY_INVALID;
    }
    const deck_serial_router_copy_result_t result =
        service->data_path.router == nullptr
            ? DECK_SERIAL_ROUTER_COPY_INVALID
            : deck_serial_router_copy_after(
                  service->data_path.router,
                  DECK_SERIAL_SINK_HISTORY,
                  after_sequence,
                  block
              );
    xSemaphoreGive(service->router_mutex);
    return result;
}

bool deck_serial_service_sink_stats(
    deck_serial_service_t *service,
    deck_serial_sink_id_t sink,
    deck_serial_sink_stats_t *stats
)
{
    if (service == nullptr || stats == nullptr ||
        xSemaphoreTake(service->router_mutex, kOwnerPollTicks) != pdTRUE) {
        return false;
    }
    const bool result =
        service->data_path.router != nullptr &&
        deck_serial_router_sink_stats(service->data_path.router, sink, stats);
    xSemaphoreGive(service->router_mutex);
    return result;
}

bool deck_serial_service_router_stats(
    deck_serial_service_t *service,
    deck_serial_router_stats_t *stats
)
{
    if (service == nullptr || stats == nullptr ||
        xSemaphoreTake(service->router_mutex, kOwnerPollTicks) != pdTRUE) {
        return false;
    }
    const bool result =
        service->data_path.router != nullptr &&
        deck_serial_router_stats(service->data_path.router, stats);
    xSemaphoreGive(service->router_mutex);
    return result;
}

bool deck_serial_service_usb_stats(
    deck_serial_service_t *service,
    deck_serial_usb_bridge_stats_t *stats
)
{
    if (service == nullptr || stats == nullptr ||
        xSemaphoreTake(service->router_mutex, kOwnerPollTicks) != pdTRUE) {
        return false;
    }
    const bool result =
        service->data_path.usb_bridge != nullptr &&
        deck_serial_usb_bridge_stats(service->data_path.usb_bridge, stats);
    xSemaphoreGive(service->router_mutex);
    return result;
}
