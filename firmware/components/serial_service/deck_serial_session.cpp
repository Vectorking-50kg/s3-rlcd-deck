#include "deck_serial_session.h"

#include <limits>
#include <new>

struct deck_serial_session {
    deck_serial_hardware_adapter_t hardware;
    uint64_t lease_timeout_ms;
    deck_serial_state_t state;
    uint64_t session_id;
    uint64_t owner_generation;
    uint64_t lease_id;
    uint64_t lease_deadline_ms;
    uint64_t usb_tx_rejected;
    uint64_t control_epoch;
    uint64_t web_transport_epoch;
    uint64_t last_request_id;
    deck_serial_command_result_t last_request_result;
    uint32_t uart_install_failures;
    bool uart_install_failed;
    bool uart_installed;
    bool has_last_request;
};

namespace {

bool valid_adapter(const deck_serial_hardware_adapter_t &adapter)
{
    return adapter.install_uart != nullptr && adapter.uninstall_uart != nullptr &&
           adapter.set_tx_high_impedance != nullptr &&
           adapter.clear_usb_tx != nullptr && adapter.clear_web_tx != nullptr;
}

uint64_t increment_nonzero(uint64_t value)
{
    ++value;
    return value == 0 ? 1 : value;
}

uint64_t saturating_add(uint64_t left, uint64_t right)
{
    return left > std::numeric_limits<uint64_t>::max() - right
               ? std::numeric_limits<uint64_t>::max()
               : left + right;
}

void fill_result(
    const deck_serial_session_t *session,
    deck_serial_command_code_t code,
    uint64_t request_id,
    deck_serial_command_result_t *result
)
{
    *result = {
        code,
        session->state,
        session->session_id,
        request_id,
        session->owner_generation,
        session->lease_id,
    };
}

void clear_web_lease(deck_serial_session_t *session)
{
    session->lease_id = 0;
    session->lease_deadline_ms = 0;
}

void return_to_usb(deck_serial_session_t *session)
{
    if (session->state != DECK_SERIAL_WEB_TX) {
        return;
    }
    session->hardware.clear_web_tx(session->hardware.context);
    clear_web_lease(session);
    session->state = DECK_SERIAL_USB_TX;
    session->owner_generation = increment_nonzero(session->owner_generation);
}

void expire_web_lease(deck_serial_session_t *session, uint64_t now_ms)
{
    if (session->state == DECK_SERIAL_WEB_TX &&
        now_ms >= session->lease_deadline_ms) {
        return_to_usb(session);
    }
}

bool disarm(deck_serial_session_t *session)
{
    const bool installed = session->uart_installed;
    const bool state_changed = session->state != DECK_SERIAL_DISARMED;
    session->state = DECK_SERIAL_DISARMED;
    clear_web_lease(session);
    session->hardware.clear_usb_tx(session->hardware.context);
    session->hardware.clear_web_tx(session->hardware.context);
    bool uninstalled = true;
    if (installed) {
        uninstalled = session->hardware.uninstall_uart(session->hardware.context);
    }
    session->uart_installed = installed && !uninstalled;
    session->hardware.set_tx_high_impedance(session->hardware.context);
    if (state_changed) {
        session->owner_generation = increment_nonzero(session->owner_generation);
    }
    session->has_last_request = false;
    session->last_request_id = 0;
    return uninstalled;
}

}  // namespace

deck_serial_session_t *deck_serial_session_create(
    const deck_serial_session_config_t *config
)
{
    if (config == nullptr || config->web_lease_timeout_ms == 0 ||
        !valid_adapter(config->hardware)) {
        return nullptr;
    }
    auto *session = new (std::nothrow) deck_serial_session_t{};
    if (session == nullptr) {
        return nullptr;
    }
    session->hardware = config->hardware;
    session->lease_timeout_ms = config->web_lease_timeout_ms;
    session->state = DECK_SERIAL_DISARMED;
    session->hardware.set_tx_high_impedance(session->hardware.context);
    return session;
}

bool deck_serial_session_destroy(deck_serial_session_t *session)
{
    if (session == nullptr) {
        return true;
    }
    if (session->state != DECK_SERIAL_DISARMED || session->uart_installed) {
        if (!disarm(session)) {
            return false;
        }
    } else {
        session->hardware.set_tx_high_impedance(session->hardware.context);
    }
    delete session;
    return true;
}

bool deck_serial_session_enter(
    deck_serial_session_t *session,
    uint64_t control_epoch,
    uint64_t now_ms,
    deck_serial_command_result_t *result
)
{
    (void)now_ms;
    if (session == nullptr || result == nullptr) {
        return false;
    }
    if (control_epoch != session->control_epoch) {
        fill_result(session, DECK_SERIAL_COMMAND_STALE_REQUEST, 0, result);
        return true;
    }
    if (session->state != DECK_SERIAL_DISARMED) {
        fill_result(session, DECK_SERIAL_COMMAND_NO_CHANGE, 0, result);
        return true;
    }
    if (session->uart_installed) {
        if (!session->hardware.uninstall_uart(session->hardware.context)) {
            session->hardware.set_tx_high_impedance(session->hardware.context);
            fill_result(
                session,
                DECK_SERIAL_COMMAND_UART_UNINSTALL_FAILED,
                0,
                result
            );
            return true;
        }
        session->uart_installed = false;
    }
    const uint64_t next_session_id = increment_nonzero(session->session_id);
    if (!session->hardware.install_uart(
            session->hardware.context,
            next_session_id
        )) {
        session->hardware.clear_usb_tx(session->hardware.context);
        session->hardware.clear_web_tx(session->hardware.context);
        session->uart_installed =
            !session->hardware.uninstall_uart(session->hardware.context);
        session->hardware.set_tx_high_impedance(session->hardware.context);
        if (session->uart_install_failures != std::numeric_limits<uint32_t>::max()) {
            ++session->uart_install_failures;
        }
        session->uart_install_failed = true;
        fill_result(session, DECK_SERIAL_COMMAND_UART_INSTALL_FAILED, 0, result);
        return true;
    }
    session->uart_install_failed = false;
    session->uart_installed = true;
    session->session_id = next_session_id;
    session->owner_generation = increment_nonzero(session->owner_generation);
    session->state = DECK_SERIAL_USB_TX;
    session->usb_tx_rejected = 0;
    session->has_last_request = false;
    session->last_request_id = 0;
    clear_web_lease(session);
    fill_result(session, DECK_SERIAL_COMMAND_APPLIED, 0, result);
    return true;
}

bool deck_serial_session_exit(
    deck_serial_session_t *session,
    uint64_t control_epoch,
    deck_serial_command_result_t *result
)
{
    if (session == nullptr || result == nullptr) {
        return false;
    }
    if (control_epoch < session->control_epoch) {
        fill_result(session, DECK_SERIAL_COMMAND_STALE_REQUEST, 0, result);
        return true;
    }
    session->control_epoch = control_epoch;
    if (session->state == DECK_SERIAL_DISARMED && !session->uart_installed) {
        fill_result(session, DECK_SERIAL_COMMAND_NO_CHANGE, 0, result);
        return true;
    }
    const bool state_changed = session->state != DECK_SERIAL_DISARMED;
    if (!disarm(session)) {
        fill_result(
            session,
            DECK_SERIAL_COMMAND_UART_UNINSTALL_FAILED,
            0,
            result
        );
        return true;
    }
    fill_result(
        session,
        state_changed ? DECK_SERIAL_COMMAND_APPLIED
                      : DECK_SERIAL_COMMAND_NO_CHANGE,
        0,
        result
    );
    return true;
}

bool deck_serial_session_request_web(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t request_id,
    bool enable,
    uint64_t now_ms,
    deck_serial_command_result_t *result
)
{
    if (session == nullptr) {
        return false;
    }
    return deck_serial_session_request_web_at_epoch(
        session,
        session->web_transport_epoch,
        session_id,
        request_id,
        enable,
        now_ms,
        result
    );
}

bool deck_serial_session_request_web_at_epoch(
    deck_serial_session_t *session,
    uint64_t web_transport_epoch,
    uint64_t session_id,
    uint64_t request_id,
    bool enable,
    uint64_t now_ms,
    deck_serial_command_result_t *result
)
{
    if (session == nullptr || result == nullptr || request_id == 0) {
        return false;
    }
    if (web_transport_epoch != session->web_transport_epoch) {
        fill_result(
            session,
            DECK_SERIAL_COMMAND_STALE_REQUEST,
            request_id,
            result
        );
        return true;
    }
    expire_web_lease(session, now_ms);
    if (session->state == DECK_SERIAL_DISARMED || session_id != session->session_id) {
        fill_result(session, DECK_SERIAL_COMMAND_STALE_SESSION, request_id, result);
        return true;
    }
    if (session->has_last_request) {
        if (request_id == session->last_request_id) {
            const bool transition_still_current =
                session->owner_generation ==
                    session->last_request_result.owner_generation &&
                session->state == session->last_request_result.state &&
                session->lease_id == session->last_request_result.lease_id;
            if (transition_still_current) {
                *result = session->last_request_result;
            } else {
                fill_result(
                    session,
                    DECK_SERIAL_COMMAND_STALE_REQUEST,
                    request_id,
                    result
                );
            }
            return true;
        }
        if (request_id < session->last_request_id) {
            fill_result(session, DECK_SERIAL_COMMAND_STALE_REQUEST, request_id, result);
            return true;
        }
    }

    const deck_serial_state_t previous = session->state;
    if (enable) {
        if (session->state == DECK_SERIAL_USB_TX) {
            session->hardware.clear_usb_tx(session->hardware.context);
        }
        session->state = DECK_SERIAL_WEB_TX;
        session->owner_generation = increment_nonzero(session->owner_generation);
        session->lease_id = request_id;
        session->lease_deadline_ms = saturating_add(now_ms, session->lease_timeout_ms);
    } else {
        return_to_usb(session);
    }
    const deck_serial_command_code_t code =
        previous == session->state && !enable ? DECK_SERIAL_COMMAND_NO_CHANGE
                                              : DECK_SERIAL_COMMAND_APPLIED;
    fill_result(session, code, request_id, result);
    session->last_request_id = request_id;
    session->last_request_result = *result;
    session->has_last_request = true;
    return true;
}

bool deck_serial_session_revoke_web_transport(
    deck_serial_session_t *session,
    uint64_t web_transport_epoch,
    deck_serial_command_result_t *result
)
{
    if (session == nullptr || result == nullptr) {
        return false;
    }
    if (web_transport_epoch < session->web_transport_epoch) {
        fill_result(session, DECK_SERIAL_COMMAND_STALE_REQUEST, 0, result);
        return true;
    }
    session->web_transport_epoch = web_transport_epoch;
    const deck_serial_state_t previous = session->state;
    return_to_usb(session);
    if (previous != DECK_SERIAL_WEB_TX) {
        session->hardware.clear_web_tx(session->hardware.context);
    }
    session->has_last_request = false;
    session->last_request_id = 0;
    fill_result(
        session,
        previous == DECK_SERIAL_WEB_TX ? DECK_SERIAL_COMMAND_APPLIED
                                       : DECK_SERIAL_COMMAND_NO_CHANGE,
        0,
        result
    );
    return true;
}

bool deck_serial_session_web_activity(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t lease_id,
    uint64_t now_ms
)
{
    if (session == nullptr) {
        return false;
    }
    expire_web_lease(session, now_ms);
    if (session->state != DECK_SERIAL_WEB_TX || session->session_id != session_id ||
        session->lease_id != lease_id || lease_id == 0) {
        return false;
    }
    session->lease_deadline_ms = saturating_add(now_ms, session->lease_timeout_ms);
    return true;
}

bool deck_serial_session_web_disconnect(
    deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t lease_id
)
{
    if (session == nullptr || session->state != DECK_SERIAL_WEB_TX ||
        session->session_id != session_id || session->lease_id != lease_id ||
        lease_id == 0) {
        return false;
    }
    return_to_usb(session);
    return true;
}

bool deck_serial_session_accept_web_input(
    const deck_serial_session_t *session,
    uint64_t session_id,
    uint64_t lease_id
)
{
    return session != nullptr && session->state == DECK_SERIAL_WEB_TX &&
           session->uart_installed && session_id != 0 &&
           session_id == session->session_id && lease_id != 0 &&
           lease_id == session->lease_id;
}

bool deck_serial_session_accept_usb_input(
    deck_serial_session_t *session,
    size_t byte_count
)
{
    if (session == nullptr) {
        return false;
    }
    return deck_serial_session_accept_usb_input_generation(
        session,
        session->owner_generation,
        byte_count
    );
}

bool deck_serial_session_accept_usb_input_generation(
    deck_serial_session_t *session,
    uint64_t owner_generation,
    size_t byte_count
)
{
    if (session == nullptr) {
        return false;
    }
    if (session->state == DECK_SERIAL_USB_TX && owner_generation != 0 &&
        owner_generation == session->owner_generation) {
        return true;
    }
    if (session->state != DECK_SERIAL_DISARMED && byte_count != 0) {
        const uint64_t count = static_cast<uint64_t>(byte_count);
        session->usb_tx_rejected = saturating_add(session->usb_tx_rejected, count);
    }
    return false;
}

bool deck_serial_session_record_usb_rejection(
    deck_serial_session_t *session,
    uint64_t byte_count
)
{
    if (session == nullptr || byte_count == 0 ||
        session->state == DECK_SERIAL_DISARMED) {
        return false;
    }
    session->usb_tx_rejected = saturating_add(
        session->usb_tx_rejected,
        byte_count
    );
    return true;
}

void deck_serial_session_tick(deck_serial_session_t *session, uint64_t now_ms)
{
    if (session != nullptr) {
        expire_web_lease(session, now_ms);
    }
}

bool deck_serial_session_snapshot(
    const deck_serial_session_t *session,
    deck_serial_session_snapshot_t *snapshot
)
{
    if (session == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = {
        session->state,
        session->session_id,
        session->owner_generation,
        session->lease_id,
        session->lease_deadline_ms,
        session->usb_tx_rejected,
        session->uart_install_failures,
        session->uart_install_failed,
        session->uart_installed,
    };
    return true;
}
