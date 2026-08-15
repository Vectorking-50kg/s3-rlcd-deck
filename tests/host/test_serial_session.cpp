#include "deck_serial_session.h"

#include <assert.h>
#include <stdint.h>

namespace {

struct FakeHardware {
    bool install_succeeds = true;
    bool uninstall_succeeds = true;
    unsigned installs = 0;
    unsigned uninstalls = 0;
    unsigned high_impedance = 0;
    unsigned usb_clears = 0;
    unsigned web_clears = 0;
    uint64_t installed_session_id = 0;
};

bool install_uart(void *context, uint64_t session_id)
{
    auto *hardware = static_cast<FakeHardware *>(context);
    ++hardware->installs;
    hardware->installed_session_id = session_id;
    return hardware->install_succeeds;
}

bool uninstall_uart(void *context)
{
    auto *hardware = static_cast<FakeHardware *>(context);
    ++hardware->uninstalls;
    return hardware->uninstall_succeeds;
}

void set_tx_high_impedance(void *context)
{
    ++static_cast<FakeHardware *>(context)->high_impedance;
}

void clear_usb_tx(void *context)
{
    ++static_cast<FakeHardware *>(context)->usb_clears;
}

void clear_web_tx(void *context)
{
    ++static_cast<FakeHardware *>(context)->web_clears;
}

deck_serial_session_t *make_session(FakeHardware *hardware, uint64_t lease_ms = 600'000)
{
    const deck_serial_session_config_t config = {
        {install_uart, uninstall_uart, set_tx_high_impedance, clear_usb_tx, clear_web_tx,
         hardware},
        lease_ms,
    };
    return deck_serial_session_create(&config);
}

deck_serial_session_snapshot_t snapshot(deck_serial_session_t *session)
{
    deck_serial_session_snapshot_t value{};
    assert(deck_serial_session_snapshot(session, &value));
    return value;
}

void test_entry_switch_lease_and_exit()
{
    FakeHardware hardware;
    deck_serial_session_t *session = make_session(&hardware, 1'000);
    assert(session != nullptr);
    assert(hardware.high_impedance == 1);

    auto state = snapshot(session);
    assert(state.state == DECK_SERIAL_DISARMED);
    assert(!state.uart_installed);

    deck_serial_command_result_t result{};
    assert(deck_serial_session_enter(session, 0, 100, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(result.state == DECK_SERIAL_USB_TX);
    assert(result.session_id == 1);
    assert(hardware.installs == 1);
    assert(hardware.installed_session_id == 1);

    // Replayed physical input is idempotent and never creates a second session.
    assert(deck_serial_session_enter(session, 0, 101, &result));
    assert(result.code == DECK_SERIAL_COMMAND_NO_CHANGE);
    assert(result.session_id == 1);
    assert(hardware.installs == 1);

    assert(deck_serial_session_request_web(session, 1, 10, true, 200, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(result.state == DECK_SERIAL_WEB_TX);
    assert(result.lease_id != 0);
    const uint64_t lease_id = result.lease_id;
    assert(hardware.usb_clears == 1);
    assert(deck_serial_session_accept_web_input(session, 1, lease_id));
    assert(!deck_serial_session_accept_web_input(session, 2, lease_id));
    assert(!deck_serial_session_accept_web_input(session, 1, lease_id + 1));

    // The exact request replays its prior result without touching queues or the lease.
    deck_serial_command_result_t replay{};
    assert(deck_serial_session_request_web(session, 1, 10, true, 999, &replay));
    assert(replay.code == result.code);
    assert(replay.lease_id == lease_id);
    assert(hardware.usb_clears == 1);
    assert(snapshot(session).lease_deadline_ms == 1'200);

    assert(!deck_serial_session_accept_usb_input(session, 7));
    assert(!deck_serial_session_accept_usb_input(session, 5));
    assert(snapshot(session).usb_tx_rejected == 12);

    // USB bytes observed while Web owned TX may reach the owner task after
    // the lease has already returned to USB. Their receive-time decision is
    // still authoritative and must remain rejected/countable.
    assert(deck_serial_session_record_usb_rejection(session, 3));
    assert(snapshot(session).usb_tx_rejected == 15);

    assert(deck_serial_session_web_activity(session, 1, lease_id, 1'199));
    assert(snapshot(session).lease_deadline_ms == 2'199);
    deck_serial_session_tick(session, 2'198);
    assert(snapshot(session).state == DECK_SERIAL_WEB_TX);
    deck_serial_session_tick(session, 2'199);
    state = snapshot(session);
    assert(state.state == DECK_SERIAL_USB_TX);
    assert(!deck_serial_session_accept_web_input(session, 1, lease_id));
    assert(state.lease_id == 0);
    assert(hardware.web_clears == 1);
    assert(deck_serial_session_accept_usb_input(session, 1));
    assert(deck_serial_session_record_usb_rejection(session, 4));
    assert(snapshot(session).usb_tx_rejected == 19);

    // A read that started in an earlier USB owner generation cannot cross a
    // complete USB -> Web -> USB ABA and become authorized again.
    const uint64_t stale_usb_generation = state.owner_generation;
    assert(deck_serial_session_request_web(session, 1, 11, true, 2'200, &result));
    assert(deck_serial_session_request_web(session, 1, 12, false, 2'201, &result));
    const auto returned_to_usb = snapshot(session);
    assert(returned_to_usb.owner_generation != stale_usb_generation);
    assert(!deck_serial_session_accept_usb_input_generation(
        session,
        stale_usb_generation,
        6
    ));
    assert(deck_serial_session_accept_usb_input_generation(
        session,
        returned_to_usb.owner_generation,
        6
    ));
    assert(snapshot(session).usb_tx_rejected == 25);

    // A response replay is only valid while the transition it describes is
    // still current. It must not claim WEB TX after the lease expired.
    assert(deck_serial_session_request_web(session, 1, 10, true, 2'202, &replay));
    assert(replay.code == DECK_SERIAL_COMMAND_STALE_REQUEST);
    assert(replay.state == DECK_SERIAL_USB_TX);

    // Stale session/request commands cannot reacquire ownership.
    assert(deck_serial_session_request_web(session, 0, 13, true, 2'203, &result));
    assert(result.code == DECK_SERIAL_COMMAND_STALE_SESSION);
    assert(deck_serial_session_request_web(session, 1, 9, true, 2'203, &result));
    assert(result.code == DECK_SERIAL_COMMAND_STALE_REQUEST);
    assert(snapshot(session).state == DECK_SERIAL_USB_TX);

    assert(deck_serial_session_request_web(session, 1, 14, true, 2'300, &result));
    const uint64_t second_lease = result.lease_id;
    assert(deck_serial_session_request_web(session, 1, 15, false, 2'301, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(snapshot(session).state == DECK_SERIAL_USB_TX);
    assert(!deck_serial_session_web_disconnect(session, 1, second_lease));

    assert(deck_serial_session_request_web(session, 1, 16, true, 2'302, &result));
    const uint64_t third_lease = result.lease_id;
    assert(deck_serial_session_web_disconnect(session, 1, third_lease));
    assert(snapshot(session).state == DECK_SERIAL_USB_TX);

    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    state = snapshot(session);
    assert(state.state == DECK_SERIAL_DISARMED);
    assert(!state.uart_installed);
    assert(hardware.uninstalls == 1);
    assert(hardware.usb_clears == 5);
    assert(hardware.web_clears == 5);
    assert(hardware.high_impedance == 2);

    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_NO_CHANGE);
    assert(hardware.uninstalls == 1);

    assert(deck_serial_session_enter(session, 1, 3'000, &result));
    assert(result.session_id == 2);
    assert(hardware.installed_session_id == 2);
    assert(result.state == DECK_SERIAL_USB_TX);
    assert(snapshot(session).usb_tx_rejected == 0);
    deck_serial_session_destroy(session);
    assert(hardware.uninstalls == 2);
    assert(hardware.high_impedance == 3);
}

void test_install_failure_is_fail_closed()
{
    FakeHardware hardware;
    hardware.install_succeeds = false;
    deck_serial_session_t *session = make_session(&hardware);
    assert(session != nullptr);

    deck_serial_command_result_t result{};
    assert(deck_serial_session_enter(session, 0, 0, &result));
    assert(result.code == DECK_SERIAL_COMMAND_UART_INSTALL_FAILED);
    const auto state = snapshot(session);
    assert(state.state == DECK_SERIAL_DISARMED);
    assert(!state.uart_installed);
    assert(state.session_id == 0);
    assert(state.uart_install_failures == 1);
    assert(state.uart_install_failed);
    assert(hardware.uninstalls == 1);
    assert(hardware.usb_clears == 1);
    assert(hardware.web_clears == 1);
    assert(hardware.high_impedance == 2);

    // A later successful retry clears the current fault while preserving the
    // cumulative diagnostic count.
    hardware.install_succeeds = true;
    assert(deck_serial_session_enter(session, 0, 1, &result));
    const auto recovered = snapshot(session);
    assert(recovered.state == DECK_SERIAL_USB_TX);
    assert(recovered.uart_install_failures == 1);
    assert(!recovered.uart_install_failed);
    assert(deck_serial_session_exit(session, 1, &result));
    assert(!snapshot(session).uart_install_failed);

    deck_serial_session_destroy(session);
    assert(hardware.uninstalls == 2);
    assert(hardware.high_impedance == 4);
}

void test_expiry_boundary_and_exit_race_are_fail_closed()
{
    FakeHardware hardware;
    deck_serial_session_t *session = make_session(&hardware, 100);
    assert(session != nullptr);
    deck_serial_command_result_t result{};
    assert(deck_serial_session_enter(session, 0, 0, &result));
    assert(deck_serial_session_request_web(session, 1, 1, true, 50, &result));
    const uint64_t expired_lease = result.lease_id;

    // A heartbeat exactly at the deadline loses to expiry.
    assert(!deck_serial_session_web_activity(session, 1, expired_lease, 150));
    assert(snapshot(session).state == DECK_SERIAL_USB_TX);

    assert(deck_serial_session_request_web(session, 1, 2, true, 151, &result));
    const uint64_t live_lease = result.lease_id;
    assert(deck_serial_session_exit(session, 1, &result));
    assert(!deck_serial_session_web_activity(session, 1, live_lease, 152));
    assert(!deck_serial_session_web_disconnect(session, 1, live_lease));
    assert(snapshot(session).state == DECK_SERIAL_DISARMED);

    deck_serial_session_destroy(session);
}

void test_exit_barrier_supersedes_a_delayed_enter()
{
    FakeHardware hardware;
    deck_serial_session_t *session = make_session(&hardware);
    assert(session != nullptr);
    deck_serial_command_result_t result{};

    // This ENTER represents work stamped before BOOT but dequeued after the
    // urgent exit command.
    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_NO_CHANGE);
    assert(deck_serial_session_enter(session, 0, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_STALE_REQUEST);
    assert(snapshot(session).state == DECK_SERIAL_DISARMED);
    assert(hardware.installs == 0);

    // A genuinely new KEY press after BOOT uses the current epoch and may arm.
    assert(deck_serial_session_enter(session, 1, 2, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(snapshot(session).state == DECK_SERIAL_USB_TX);
    assert(hardware.installs == 1);
    deck_serial_session_destroy(session);
}

void test_uninstall_failure_is_disarmed_and_retryable()
{
    FakeHardware hardware;
    deck_serial_session_t *session = make_session(&hardware);
    assert(session != nullptr);
    deck_serial_command_result_t result{};
    assert(deck_serial_session_enter(session, 0, 0, &result));

    hardware.uninstall_succeeds = false;
    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_UART_UNINSTALL_FAILED);
    assert(result.state == DECK_SERIAL_DISARMED);
    assert(snapshot(session).uart_installed);
    assert(hardware.high_impedance == 2);

    hardware.uninstall_succeeds = true;
    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_NO_CHANGE);
    assert(!snapshot(session).uart_installed);
    assert(hardware.uninstalls == 2);
    deck_serial_session_destroy(session);
}

void test_partial_install_cleanup_is_retained_until_retry()
{
    FakeHardware hardware;
    hardware.install_succeeds = false;
    hardware.uninstall_succeeds = false;
    deck_serial_session_t *session = make_session(&hardware);
    assert(session != nullptr);
    deck_serial_command_result_t result{};

    assert(deck_serial_session_enter(session, 0, 0, &result));
    assert(result.code == DECK_SERIAL_COMMAND_UART_INSTALL_FAILED);
    assert(snapshot(session).uart_installed);
    hardware.install_succeeds = true;
    assert(deck_serial_session_enter(session, 0, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_UART_UNINSTALL_FAILED);
    assert(hardware.installs == 1);

    hardware.uninstall_succeeds = true;
    assert(deck_serial_session_enter(session, 0, 2, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(result.session_id == 1);
    assert(hardware.installs == 2);
    assert(deck_serial_session_destroy(session));
}

void test_transport_revoke_fences_a_delayed_web_enable()
{
    FakeHardware hardware;
    deck_serial_session_t *session = make_session(&hardware);
    deck_serial_command_result_t result{};
    assert(deck_serial_session_enter(session, 0, 0, &result));
    assert(deck_serial_session_request_web_at_epoch(
        session, 0, 1, 90, true, 1, &result
    ));
    assert(result.state == DECK_SERIAL_WEB_TX);

    assert(deck_serial_session_revoke_web_transport(session, 2, &result));
    assert(result.state == DECK_SERIAL_USB_TX);

    // BOOT/exit has an independent lifecycle epoch. Even if the revoke was
    // placed at the front of the owner queue after exit was already queued,
    // it cannot turn the safety-critical exit into a stale command.
    assert(deck_serial_session_exit(session, 1, &result));
    assert(result.code == DECK_SERIAL_COMMAND_APPLIED);
    assert(snapshot(session).state == DECK_SERIAL_DISARMED);

    // This enable was queued by the disconnected transport before the fence,
    // but reached the sole owner after the revoke. It cannot regain TX.
    assert(deck_serial_session_request_web_at_epoch(
        session, 0, 1, 91, true, 2, &result
    ));
    assert(result.code == DECK_SERIAL_COMMAND_STALE_REQUEST);
    assert(snapshot(session).state == DECK_SERIAL_DISARMED);
    assert(deck_serial_session_destroy(session));
}

}  // namespace

int main()
{
    test_entry_switch_lease_and_exit();
    test_install_failure_is_fail_closed();
    test_expiry_boundary_and_exit_race_are_fail_closed();
    test_exit_barrier_supersedes_a_delayed_enter();
    test_uninstall_failure_is_disarmed_and_retryable();
    test_partial_install_cleanup_is_retained_until_retry();
    test_transport_revoke_fences_a_delayed_web_enable();
    return 0;
}
