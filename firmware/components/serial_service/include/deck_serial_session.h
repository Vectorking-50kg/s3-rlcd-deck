#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define DECK_SERIAL_DEFAULT_WEB_LEASE_MS 600000ULL

typedef struct deck_serial_session deck_serial_session_t;

typedef enum {
    DECK_SERIAL_DISARMED = 0,
    DECK_SERIAL_USB_TX,
    DECK_SERIAL_WEB_TX,
} deck_serial_state_t;

typedef enum {
    DECK_SERIAL_COMMAND_APPLIED = 0,
    DECK_SERIAL_COMMAND_NO_CHANGE,
    DECK_SERIAL_COMMAND_STALE_SESSION,
    DECK_SERIAL_COMMAND_STALE_REQUEST,
    DECK_SERIAL_COMMAND_UART_INSTALL_FAILED,
    DECK_SERIAL_COMMAND_INVALID,
} deck_serial_command_code_t;

typedef struct {
    bool (*install_uart)(void *context);
    void (*uninstall_uart)(void *context);
    void (*set_tx_high_impedance)(void *context);
    void (*clear_usb_tx)(void *context);
    void (*clear_web_tx)(void *context);
    void *context;
} deck_serial_hardware_adapter_t;

typedef struct {
    deck_serial_hardware_adapter_t hardware;
    uint64_t web_lease_timeout_ms;
} deck_serial_session_config_t;

typedef struct {
    deck_serial_command_code_t code;
    deck_serial_state_t state;
    uint64_t session_id;
    uint64_t request_id;
    uint64_t owner_generation;
    uint64_t lease_id;
} deck_serial_command_result_t;

typedef struct {
    deck_serial_state_t state;
    uint64_t session_id;
    uint64_t owner_generation;
    uint64_t lease_id;
    uint64_t lease_deadline_ms;
    uint64_t usb_tx_rejected;
    uint32_t uart_install_failures;
    bool uart_install_failed;
    bool uart_installed;
} deck_serial_session_snapshot_t;

/*
 * This state machine is intentionally synchronization-free: exactly one owner
 * task must call every mutating function. Readers receive immutable snapshots.
 * No serial payload bytes are retained here.
 */
deck_serial_session_t *deck_serial_session_create(
    const deck_serial_session_config_t *config
);
void deck_serial_session_destroy(deck_serial_session_t *session);

bool deck_serial_session_enter(
    deck_serial_session_t *session,
    uint64_t control_epoch,
    uint64_t now_ms,
    deck_serial_command_result_t *result
);
bool deck_serial_session_exit(
    deck_serial_session_t *session,
    uint64_t control_epoch,
    deck_serial_command_result_t *result
);
bool deck_serial_session_request_web(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t request_id,
    bool enable,
    uint64_t now_ms,
    deck_serial_command_result_t *result
);
bool deck_serial_session_web_activity(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t lease_id,
    uint64_t now_ms
);
bool deck_serial_session_web_disconnect(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t lease_id
);
bool deck_serial_session_accept_usb_input(
    deck_serial_session_t *session,
    size_t byte_count
);
void deck_serial_session_tick(deck_serial_session_t *session, uint64_t now_ms);
bool deck_serial_session_snapshot(
    const deck_serial_session_t *session,
    deck_serial_session_snapshot_t *snapshot
);

#ifdef __cplusplus
}
#endif
