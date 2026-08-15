#!/usr/bin/env python3

import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "m1_setup_client", ROOT / "tools/m1_setup_client.py"
)
assert SPEC and SPEC.loader
helper = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(helper)


def test_probe_requires_the_live_ssh_path_to_use_the_wired_interface() -> None:
    outputs = iter(
        [
            "2: eth0    inet 192.168.31.46/24 brd 192.168.31.255 scope global eth0",
            "wifi",
            "ethernet",
        ]
    )
    with mock.patch.object(helper.shutil, "which", return_value="/usr/bin/tool"), mock.patch.dict(
        helper.os.environ,
        {"SSH_CONNECTION": "192.168.31.45 50000 192.168.31.46 22"},
        clear=True,
    ), mock.patch.object(helper, "run_checked", side_effect=lambda *_args: next(outputs)):
        helper.verify_control_path("wlan0", "eth0")

    with mock.patch.object(helper.shutil, "which", return_value="/usr/bin/tool"), mock.patch.dict(
        helper.os.environ,
        {},
        clear=True,
    ):
        try:
            helper.verify_control_path("wlan0", "eth0")
        except helper.HelperFailure:
            pass
        else:
            raise AssertionError("a non-SSH or same-interface control path must fail")

    wrong_control_type = iter(
        [
            "2: wlan1 inet 192.168.31.46/24 scope global wlan1",
            "wifi",
            "wifi",
        ]
    )
    with mock.patch.object(helper.shutil, "which", return_value="/usr/bin/tool"), mock.patch.dict(
        helper.os.environ,
        {"SSH_CONNECTION": "192.168.31.45 50000 192.168.31.46 22"},
        clear=True,
    ), mock.patch.object(
        helper, "run_checked", side_effect=lambda *_args: next(wrong_control_type)
    ):
        try:
            helper.verify_control_path("wlan0", "wlan1")
        except helper.HelperFailure:
            pass
        else:
            raise AssertionError("a second Wi-Fi interface must not be reported as wired")


def test_transaction_lock_serializes_cleanup_after_a_delayed_primary_process() -> None:
    identifier = "5" * 32
    module_path = str(ROOT / "tools/m1_setup_client.py")
    child_code = """
import importlib.util
import sys
spec = importlib.util.spec_from_file_location('m1_setup_client_child', sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with module.TransactionLock(sys.argv[2], 3):
    print('acquired', flush=True)
"""
    with tempfile.TemporaryDirectory() as directory, mock.patch.object(
        helper.tempfile, "gettempdir", return_value=directory
    ):
        primary = helper.TransactionLock(identifier, 2)
        primary.__enter__()
        environment = {**os.environ, "TMPDIR": directory}
        child = subprocess.Popen(
            [sys.executable, "-c", child_code, module_path, identifier],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=environment,
        )
        try:
            time.sleep(0.2)
            assert child.poll() is None
        finally:
            primary.__exit__()
        stdout, stderr = child.communicate(timeout=4)
    assert child.returncode == 0, stderr
    assert stdout.strip() == "acquired"


def test_cleanup_cancellation_fence_blocks_a_late_primary_process() -> None:
    identifier = "6" * 32
    module_path = str(ROOT / "tools/m1_setup_client.py")
    child_code = """
import importlib.util
import sys
spec = importlib.util.spec_from_file_location('m1_setup_client_child', sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with module.TransactionLock(sys.argv[2], 3):
    try:
        module.require_active_transaction(sys.argv[2])
    except module.HelperFailure:
        print('cancelled', flush=True)
    else:
        raise SystemExit(2)
"""
    with tempfile.TemporaryDirectory() as directory, mock.patch.object(
        helper.tempfile, "gettempdir", return_value=directory
    ):
        with helper.TransactionLock(identifier, 2):
            helper.cancel_transaction(identifier)
        child = subprocess.run(
            [sys.executable, "-c", child_code, module_path, identifier],
            capture_output=True,
            text=True,
            timeout=4,
            env={**os.environ, "TMPDIR": directory},
        )
    assert child.returncode == 0, child.stderr
    assert child.stdout.strip() == "cancelled"


def test_networkmanager_receives_setup_password_only_through_a_fifo() -> None:
    password = "SETUP-PASSWORD"
    commands: list[list[str]] = []
    temporary_uuid = "11111111-2222-3333-4444-555555555555"
    original_uuid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    active_queries = iter(
        [original_uuid, temporary_uuid, "--", original_uuid]
    )

    def checked(arguments: list[str], _timeout: float) -> str:
        commands.append(arguments)
        if "GENERAL.CON-UUID" in arguments:
            return next(active_queries)
        if "connection.uuid" in arguments:
            if arguments[-1] == "show":
                return original_uuid
            return temporary_uuid
        if "IP4.ADDRESS" in arguments:
            return "192.168.4.2/24"
        if arguments[:4] == ["ip", "-4", "route", "get"]:
            if arguments[-1] == "192.168.4.1":
                return "192.168.4.1 dev wlan0 src 192.168.4.2"
            return "192.168.31.45 dev eth0 src 192.168.31.46"
        return ""

    access = {
        "ssid": "S3Deck-1234",
        "password": password,
        "address": "192.168.4.1",
    }
    writes: list[bytes] = []

    def checked_fifo(
        arguments: list[str], _fifo: pathlib.Path, payload: bytes, _timeout: float
    ) -> str:
        commands.append(arguments)
        writes.append(payload)
        return ""

    with mock.patch.object(helper, "run_checked", side_effect=checked), mock.patch.object(
        helper, "run_checked_with_fifo", side_effect=checked_fifo
    ):
        with helper.NetworkManagerSession(
            "wlan0", "eth0", access, "192.168.31.45"
        ) as session:
            assert session.local_address == "192.168.4.2"
    assert all(password not in argument for command in commands for argument in command)
    assert writes == [
        b"802-11-wireless-security.psk:SETUP-PASSWORD\n"
    ]
    add = next(command for command in commands if "add" in command)
    assert add[add.index("save") + 1] == "no"
    up = next(command for command in commands if "passwd-file" in command)
    assert up[up.index("passwd-file") + 1].endswith("/wifi.psk")


def test_fifo_writer_closes_after_one_payload_so_the_reader_observes_eof() -> None:
    with tempfile.TemporaryDirectory() as directory:
        fifo = pathlib.Path(directory) / "secret.fifo"
        helper.os.mkfifo(fifo, 0o600)
        assert helper.run_checked_with_fifo(
            ["/bin/cat", str(fifo)], fifo, b"one-line-secret\n", 2
        ) == "one-line-secret"


def test_relay_shutdown_closes_every_active_socket_before_joining() -> None:
    relay = helper.TcpRelay("127.0.0.1", "127.0.0.1", 0)
    active = mock.Mock()
    relay._active.add(active)
    relay._thread = mock.Mock()
    relay._thread.is_alive.return_value = False
    relay.__exit__()
    active.shutdown.assert_called_once_with(helper.socket.SHUT_RDWR)
    active.close.assert_called_once()


def test_networkmanager_fails_closed_when_prior_wifi_state_is_unknown() -> None:
    session = helper.NetworkManagerSession(
        "wlan0",
        "eth0",
        {
            "ssid": "S3Deck-1234",
            "password": "SETUP-PASSWORD",
            "address": "192.168.4.1",
        },
        "192.168.31.45",
    )
    with mock.patch.object(
        helper, "run_checked", side_effect=helper.HelperFailure("query_failed")
    ):
        try:
            session._open()
        except helper.HelperFailure:
            pass
        else:
            raise AssertionError("an unknown prior Wi-Fi state must fail closed")


def test_networkmanager_requires_independent_deletion_proof() -> None:
    session = helper.NetworkManagerSession(
        "wlan0",
        "eth0",
        {
            "ssid": "S3Deck-1234",
            "password": "SETUP-PASSWORD",
            "address": "192.168.4.1",
        },
        "192.168.31.45",
    )
    session.connection_uuid = "11111111-2222-3333-4444-555555555555"
    session.original_uuid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    with mock.patch.object(
        helper,
        "active_connection_uuid",
        side_effect=[session.connection_uuid, "", session.original_uuid],
    ), mock.patch.object(helper, "run_checked", return_value=""), mock.patch.object(
        helper,
        "saved_connections",
        side_effect=helper.HelperFailure("verification_failed"),
    ):
        try:
            session._cleanup()
        except helper.HelperFailure as error:
            assert "network_cleanup_failed" in str(error)
        else:
            raise AssertionError("an unverified temporary-profile deletion must fail")


def test_partial_network_open_always_runs_compensating_cleanup() -> None:
    session = helper.NetworkManagerSession(
        "wlan0",
        "eth0",
        {
            "ssid": "S3Deck-1234",
            "password": "SETUP-PASSWORD",
            "address": "192.168.4.1",
        },
        "192.168.31.45",
    )
    session._open = mock.Mock(side_effect=helper.HelperFailure("dhcp_failed"))
    session._cleanup = mock.Mock()
    try:
        with session:
            raise AssertionError("unreachable")
    except helper.HelperFailure as error:
        assert "dhcp_failed" in str(error)
    else:
        raise AssertionError("partial open must fail")
    session._cleanup.assert_called_once()


def test_remote_cleanup_replays_the_nonsecret_journal_and_verifies_state() -> None:
    identifier = "4" * 32
    temporary_uuid = "11111111-2222-3333-4444-555555555555"
    original_uuid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    calls: list[list[str]] = []
    with tempfile.TemporaryDirectory() as directory, mock.patch.object(
        helper.tempfile, "gettempdir", return_value=directory
    ):
        helper.write_state(
            identifier,
            {
                "connection_uuid": temporary_uuid,
                "original_uuid": original_uuid,
            },
        )
        with mock.patch.object(
            helper,
            "active_connection_uuid",
            side_effect=[temporary_uuid, "", original_uuid, original_uuid],
        ), mock.patch.object(
            helper,
            "saved_connections",
            side_effect=[{}, {}],
        ), mock.patch.object(
            helper,
            "run_checked",
            side_effect=lambda arguments, _timeout: calls.append(arguments) or "",
        ):
            assert helper.cleanup_transaction(
                "wlan0",
                {
                    "transaction_id": identifier,
                    "transaction_timeout_seconds": 75,
                },
            ) == {"network_restored": True}
        assert helper.read_state(identifier) is None
    assert any(
        "down" in command and temporary_uuid in command for command in calls
    )
    assert any("up" in command and original_uuid in command for command in calls)


def test_primary_cleanup_retains_uuid_journal_for_fresh_verification() -> None:
    identifier = "5" * 32
    original_uuid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    with tempfile.TemporaryDirectory() as directory, mock.patch.object(
        helper.tempfile, "gettempdir", return_value=directory
    ):
        session = helper.NetworkManagerSession(
            "wlan0",
            "eth0",
            {
                "ssid": "S3Deck-1234",
                "password": "SETUP-PASSWORD",
                "address": "192.168.4.1",
            },
            "192.168.31.45",
            identifier,
            helper.OperationBudget(75),
        )
        session.original_uuid = original_uuid
        session._write_state()
        with mock.patch.object(
            helper, "saved_connections", return_value={}
        ), mock.patch.object(
            helper, "active_connection_uuid", return_value=original_uuid
        ):
            session._cleanup()
        assert helper.read_state(identifier) == {
            "connection_uuid": "",
            "original_uuid": original_uuid,
        }


def test_pair_transaction_reads_the_real_page_and_acknowledges_202() -> None:
    access = {
        "ssid": "S3Deck-1234",
        "password": "SETUP-PASSWORD",
        "address": "192.168.4.1",
    }
    request = {
        "access": access,
        "hub_address": "192.168.31.45:7780",
        "pairing_code": "012345",
        "transaction_id": "1" * 32,
        "transaction_timeout_seconds": 75,
    }
    network = mock.Mock(local_address="192.168.4.2")
    network.__enter__ = mock.Mock(return_value=network)
    network.__exit__ = mock.Mock(return_value=None)
    relay = mock.Mock(failure=False, connections=1)
    relay.__enter__ = mock.Mock(return_value=relay)
    relay.__exit__ = mock.Mock(return_value=None)
    gets = [
        (200, helper.RECOVERY_TITLE),
        OSError("Setup AP closed"),
    ]
    posts = [
        (202, json.dumps({"response_ack": "a" * 32}).encode()),
        (202, b""),
    ]
    with mock.patch.object(helper, "NetworkManagerSession", return_value=network), mock.patch.object(
        helper, "TcpRelay", return_value=relay
    ), mock.patch.object(helper, "get_document", side_effect=gets), mock.patch.object(
        helper, "post_form", side_effect=posts
    ) as post:
        result = helper.pair_transaction("wlan0", "eth0", request)
    assert result == {
        "recovery_page": True,
        "response_acknowledged": True,
    }
    assert post.call_args_list[0].args[:3] == (
        "http://192.168.4.1",
        "/api/companions/pair",
        {"hub_address": "192.168.31.45:7780", "code": "012345"},
    )
    assert post.call_args_list[1].args[1:3] == (
        "/api/companions/pair/ack",
        {"response_ack": "a" * 32},
    )


def test_snapshot_transaction_reads_the_real_page_and_profiles() -> None:
    request = {
        "access": {
            "ssid": "S3Deck-1234",
            "password": "SETUP-PASSWORD",
            "address": "192.168.4.1",
        },
        "transaction_id": "2" * 32,
        "transaction_timeout_seconds": 75,
    }
    profiles = {"profile_ids": [], "active_profile_id": ""}
    network = mock.Mock()
    network.__enter__ = mock.Mock(return_value=network)
    network.__exit__ = mock.Mock(return_value=None)
    with mock.patch.dict(
        helper.os.environ,
        {"SSH_CONNECTION": "192.168.31.45 50000 192.168.31.46 22"},
        clear=True,
    ), mock.patch.object(
        helper, "NetworkManagerSession", return_value=network
    ), mock.patch.object(
        helper, "get_document", return_value=(200, helper.RECOVERY_TITLE)
    ), mock.patch.object(helper, "companion_profiles", return_value=profiles):
        assert helper.snapshot_transaction("wlan0", "eth0", request) == {
            "recovery_page": True,
            "profiles": profiles,
        }


def test_restore_transaction_removes_only_temporary_profiles() -> None:
    original_id = "sha256:" + "a" * 64
    temporary_id = "sha256:" + "b" * 64
    request = {
        "access": {
            "ssid": "S3Deck-1234",
            "password": "SETUP-PASSWORD",
            "address": "192.168.4.1",
        },
        "original_profiles": {
            "profile_ids": [original_id],
            "active_profile_id": original_id,
        },
        "transaction_id": "3" * 32,
        "transaction_timeout_seconds": 75,
    }
    network = mock.Mock()
    network.__enter__ = mock.Mock(return_value=network)
    network.__exit__ = mock.Mock(return_value=None)
    current = {
        "profile_ids": [original_id, temporary_id],
        "active_profile_id": temporary_id,
    }
    operations: list[tuple[str, dict[str, str]]] = []

    def post(
        _base: str, path: str, fields: dict[str, str], _timeout: float
    ) -> tuple[int, bytes]:
        operations.append((path, fields))
        return 202, b""

    with mock.patch.dict(
        helper.os.environ,
        {"SSH_CONNECTION": "192.168.31.45 50000 192.168.31.46 22"},
        clear=True,
    ), mock.patch.object(
        helper, "NetworkManagerSession", return_value=network
    ), mock.patch.object(
        helper, "companion_profiles", side_effect=[current, request["original_profiles"]]
    ), mock.patch.object(helper, "post_form", side_effect=post):
        assert helper.restore_transaction("wlan0", "eth0", request) == {
            "profiles_restored": True
        }
    assert operations == [
        ("/api/companions/revoke", {"profile_id": temporary_id}),
        ("/api/companions/select", {"profile_id": original_id}),
    ]


if __name__ == "__main__":
    test_probe_requires_the_live_ssh_path_to_use_the_wired_interface()
    test_transaction_lock_serializes_cleanup_after_a_delayed_primary_process()
    test_cleanup_cancellation_fence_blocks_a_late_primary_process()
    test_networkmanager_receives_setup_password_only_through_a_fifo()
    test_fifo_writer_closes_after_one_payload_so_the_reader_observes_eof()
    test_relay_shutdown_closes_every_active_socket_before_joining()
    test_networkmanager_fails_closed_when_prior_wifi_state_is_unknown()
    test_networkmanager_requires_independent_deletion_proof()
    test_partial_network_open_always_runs_compensating_cleanup()
    test_remote_cleanup_replays_the_nonsecret_journal_and_verifies_state()
    test_primary_cleanup_retains_uuid_journal_for_fresh_verification()
    test_pair_transaction_reads_the_real_page_and_acknowledges_202()
    test_snapshot_transaction_reads_the_real_page_and_profiles()
    test_restore_transaction_removes_only_temporary_profiles()
    print("M1 Setup client contract passed")
