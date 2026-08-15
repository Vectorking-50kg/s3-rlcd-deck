#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "deck_serial_router.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_serial_usb_bridge deck_serial_usb_bridge_t;

typedef enum {
    DECK_SERIAL_USB_IDLE = 0,
    DECK_SERIAL_USB_PROGRESS,
    DECK_SERIAL_USB_BACKPRESSURE,
    DECK_SERIAL_USB_ERROR,
} deck_serial_usb_pump_result_t;

typedef struct {
    bool (*connected)(void *context);
    deck_serial_router_copy_result_t (*take_output)(
        void *context,
        deck_serial_routed_block_t *block
    );
    int (*write_output)(void *context, const uint8_t *bytes, size_t size);
    bool (*input_ready)(void *context);
    uint64_t (*input_authority_generation)(void *context);
    int (*read_input)(void *context, uint8_t *bytes, size_t capacity);
    bool (*submit_input)(
        void *context,
        const uint8_t *bytes,
        size_t size,
        uint64_t authority_generation
    );
    void *context;
} deck_serial_usb_io_adapter_t;

typedef void *(*deck_serial_usb_allocate_fn)(void *context, size_t size);
typedef void (*deck_serial_usb_deallocate_fn)(void *context, void *memory);

typedef struct {
    deck_serial_usb_allocate_fn allocate;
    deck_serial_usb_deallocate_fn deallocate;
    void *context;
} deck_serial_usb_memory_t;

typedef struct {
    deck_serial_usb_io_adapter_t io;
    deck_serial_usb_memory_t memory;
} deck_serial_usb_bridge_config_t;

typedef struct {
    uint64_t output_bytes;
    uint64_t output_blocks;
    uint64_t output_backpressure;
    uint64_t output_failures;
    uint64_t last_output_sequence;
    uint64_t input_bytes;
    uint64_t input_blocks;
    uint64_t input_backpressure;
    uint64_t input_failures;
    uint64_t input_submit_failures;
    uint64_t disconnect_observations;
} deck_serial_usb_bridge_stats_t;

/*
 * A small fixed-state adapter between the Router and one USB Serial/JTAG
 * driver. Calls perform at most one bounded driver operation. A partially
 * written Router block remains owned by the bridge until reconnect/progress
 * or destroy, so transports never receive Router storage ownership.
 */
deck_serial_usb_bridge_t *deck_serial_usb_bridge_create(
    const deck_serial_usb_bridge_config_t *config
);
void deck_serial_usb_bridge_destroy(deck_serial_usb_bridge_t *bridge);

deck_serial_usb_pump_result_t deck_serial_usb_bridge_pump_output(
    deck_serial_usb_bridge_t *bridge
);
deck_serial_usb_pump_result_t deck_serial_usb_bridge_pump_input(
    deck_serial_usb_bridge_t *bridge
);
bool deck_serial_usb_bridge_stats(
    const deck_serial_usb_bridge_t *bridge,
    deck_serial_usb_bridge_stats_t *stats
);

#ifdef __cplusplus
}
#endif
