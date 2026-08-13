#include "deck_setup_http.h"
#include "deck_setup_mode.h"

#include <array>
#include <cassert>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <string>

namespace {

struct DeterministicRandom {
    uint8_t next;
    bool fail = false;
};

bool fill_random(void *context, uint8_t *output, size_t size)
{
    auto *random = static_cast<DeterministicRandom *>(context);
    if (random->fail) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        output[index] = random->next++;
    }
    return true;
}

deck_setup_mode_t *create_setup(DeterministicRandom *random, uint64_t timeout_ms = 600'000)
{
    const deck_setup_mode_config_t config = {
        timeout_ms,
        fill_random,
        random,
    };
    return deck_setup_mode_create(&config);
}

void first_boot_without_wifi_enters_setup()
{
    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);

    assert(deck_setup_mode_boot(setup, false, 100) == DECK_SETUP_MODE_STARTED);
    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));
    assert(snapshot.active);
    assert(snapshot.reason == DECK_SETUP_REASON_NO_WIFI);
    assert(std::string(snapshot.ssid).rfind("S3-RLCD-", 0) == 0);
    assert(std::string(snapshot.address) == "192.168.4.1");
    assert(snapshot.session_id == 1);
    assert(snapshot.started_at_ms == 100);
    assert(snapshot.last_activity_ms == 100);

    deck_setup_mode_destroy(setup);
}

void valid_wifi_boot_stays_idle_but_boot_long_press_enters()
{
    DeterministicRandom random{5};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);

    assert(deck_setup_mode_boot(setup, true, 100) == DECK_SETUP_MODE_UNCHANGED);
    assert(deck_setup_mode_enter(setup, DECK_SETUP_REASON_BOOT_LONG_PRESS, 500) ==
           DECK_SETUP_MODE_STARTED);

    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));
    assert(snapshot.active);
    assert(snapshot.reason == DECK_SETUP_REASON_BOOT_LONG_PRESS);

    deck_setup_mode_destroy(setup);
}

void credentials_are_readable_and_change_on_reentry()
{
    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);
    assert(deck_setup_mode_enter(setup, DECK_SETUP_REASON_BOOT_LONG_PRESS, 0) ==
           DECK_SETUP_MODE_STARTED);

    deck_setup_snapshot_t first{};
    assert(deck_setup_mode_snapshot(setup, &first));
    const std::string first_ssid(first.ssid);
    const std::string first_password(first.password);
    assert(first_ssid.size() == 12);
    assert(first_password.size() == 14);
    assert(first_password[4] == '-' && first_password[9] == '-');
    for (const char value : first_password) {
        assert(value == '-' || std::strchr("ABCDEFGHJKLMNPQRSTUVWXYZ23456789", value) != nullptr);
    }

    assert(deck_setup_mode_enter(setup, DECK_SETUP_REASON_BOOT_LONG_PRESS, 1'000) ==
           DECK_SETUP_MODE_RESTARTED);
    deck_setup_snapshot_t second{};
    assert(deck_setup_mode_snapshot(setup, &second));
    assert(second.session_id == 2);
    assert(first_ssid != second.ssid);
    assert(first_password != second.password);
    assert(second.started_at_ms == 1'000);
    assert(second.last_activity_ms == 1'000);

    deck_setup_mode_destroy(setup);
}

void failed_credential_generation_preserves_the_active_session()
{
    DeterministicRandom random{0, false};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);
    assert(deck_setup_mode_enter(setup, DECK_SETUP_REASON_BOOT_LONG_PRESS, 100) ==
           DECK_SETUP_MODE_STARTED);
    deck_setup_snapshot_t before{};
    assert(deck_setup_mode_snapshot(setup, &before));

    random.fail = true;
    assert(deck_setup_mode_enter(setup, DECK_SETUP_REASON_BOOT_LONG_PRESS, 200) ==
           DECK_SETUP_MODE_ERROR);
    deck_setup_snapshot_t after{};
    assert(deck_setup_mode_snapshot(setup, &after));
    assert(after.active == before.active);
    assert(after.reason == before.reason);
    assert(after.session_id == before.session_id);
    assert(after.started_at_ms == before.started_at_ms);
    assert(after.last_activity_ms == before.last_activity_ms);
    assert(std::string(after.ssid) == before.ssid);
    assert(std::string(after.password) == before.password);

    deck_setup_mode_destroy(setup);
}

void only_explicit_activity_refreshes_the_timeout()
{
    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random, 600'000);
    assert(setup != nullptr);
    assert(deck_setup_mode_boot(setup, false, 10) == DECK_SETUP_MODE_STARTED);

    assert(deck_setup_mode_tick(setup, 600'009) == DECK_SETUP_MODE_UNCHANGED);
    assert(deck_setup_mode_activity(setup, 500'000));
    assert(deck_setup_mode_tick(setup, 1'099'999) == DECK_SETUP_MODE_UNCHANGED);
    assert(deck_setup_mode_tick(setup, 1'100'000) == DECK_SETUP_MODE_STOPPED);
    assert(!deck_setup_mode_activity(setup, 1'100'001));

    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));
    assert(!snapshot.active);
    deck_setup_mode_destroy(setup);
}

void explicit_stop_clears_ephemeral_credentials()
{
    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);
    assert(deck_setup_mode_boot(setup, false, 0) == DECK_SETUP_MODE_STARTED);
    assert(deck_setup_mode_stop(setup) == DECK_SETUP_MODE_STOPPED);
    assert(deck_setup_mode_stop(setup) == DECK_SETUP_MODE_UNCHANGED);

    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));
    assert(!snapshot.active);
    assert(snapshot.ssid[0] == '\0');
    assert(snapshot.password[0] == '\0');
    deck_setup_mode_destroy(setup);
}

void http_contract_exposes_profile_pairing_without_secrets()
{
    size_t route_count = 0;
    const deck_setup_http_route_spec_t *routes = deck_setup_http_routes(&route_count);
    assert(routes != nullptr);
    assert(route_count == 10);
    assert(routes[0].route == DECK_SETUP_HTTP_PAGE);
    assert(routes[0].method == DECK_SETUP_HTTP_GET);
    assert(std::string(routes[0].path) == "/");
    assert(routes[1].route == DECK_SETUP_HTTP_STATUS);
    assert(routes[1].method == DECK_SETUP_HTTP_GET);
    assert(std::string(routes[1].path) == "/api/status");
    assert(routes[2].route == DECK_SETUP_HTTP_SCAN);
    assert(routes[2].method == DECK_SETUP_HTTP_POST);
    assert(std::string(routes[2].path) == "/api/scan");
    assert(routes[3].route == DECK_SETUP_HTTP_WIFI);
    assert(routes[3].method == DECK_SETUP_HTTP_POST);
    assert(std::string(routes[3].path) == "/api/wifi");
    assert(routes[4].route == DECK_SETUP_HTTP_TEMPERATURE);
    assert(std::string(routes[4].path) == "/api/temperature");
    assert(routes[5].route == DECK_SETUP_HTTP_WIFI_CLEAR_REQUEST);
    assert(std::string(routes[5].path) == "/api/wifi/clear/request");
    assert(routes[6].route == DECK_SETUP_HTTP_WIFI_CLEAR_CONFIRM);
    assert(std::string(routes[6].path) == "/api/wifi/clear/confirm");
    assert(routes[7].route == DECK_SETUP_HTTP_COMPANION_PAIR);
    assert(std::string(routes[7].path) == "/api/companions/pair");
    assert(routes[8].route == DECK_SETUP_HTTP_COMPANION_SELECT);
    assert(std::string(routes[8].path) == "/api/companions/select");
    assert(routes[9].route == DECK_SETUP_HTTP_COMPANION_REVOKE);
    assert(std::string(routes[9].path) == "/api/companions/revoke");

    assert(deck_setup_http_route("GET", "/") == DECK_SETUP_HTTP_PAGE);
    assert(deck_setup_http_route("GET", "/api/status") == DECK_SETUP_HTTP_STATUS);
    assert(deck_setup_http_route("POST", "/api/scan") == DECK_SETUP_HTTP_SCAN);
    assert(deck_setup_http_route("POST", "/api/wifi") == DECK_SETUP_HTTP_WIFI);
    assert(deck_setup_http_route("POST", "/api/temperature") ==
           DECK_SETUP_HTTP_TEMPERATURE);
    assert(deck_setup_http_route("POST", "/api/wifi/clear/request") ==
           DECK_SETUP_HTTP_WIFI_CLEAR_REQUEST);
    assert(deck_setup_http_route("POST", "/api/wifi/clear/confirm") ==
           DECK_SETUP_HTTP_WIFI_CLEAR_CONFIRM);
    assert(deck_setup_http_route("GET", "/api/wifi") == DECK_SETUP_HTTP_METHOD_NOT_ALLOWED);
    assert(deck_setup_http_route("POST", "/api/companions/pair") ==
           DECK_SETUP_HTTP_COMPANION_PAIR);
    assert(deck_setup_http_route("POST", "/api/companions/select") ==
           DECK_SETUP_HTTP_COMPANION_SELECT);
    assert(deck_setup_http_route("POST", "/api/companions/revoke") ==
           DECK_SETUP_HTTP_COMPANION_REVOKE);
    assert(deck_setup_http_route("GET", "/api/scan") == DECK_SETUP_HTTP_METHOD_NOT_ALLOWED);

    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);
    assert(deck_setup_mode_boot(setup, false, 0) == DECK_SETUP_MODE_STARTED);
    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));

    char page[4'096];
    assert(deck_setup_http_render_page(page, sizeof(page)));
    const std::string html(page);
    assert(html.find("Setup / Recovery") != std::string::npos);
    assert(html.find("Scan networks") != std::string::npos);
    assert(html.find("Companion pairing") != std::string::npos);
    assert(html.find("name=hub_address") != std::string::npos);
    assert(html.find("name=code") != std::string::npos);
    assert(html.find("/api/companions/pair") != std::string::npos);
    assert(html.find("/api/companions/select") != std::string::npos);
    assert(html.find("/api/companions/revoke") != std::string::npos);
    assert(html.find("innerHTML") == std::string::npos);
    assert(html.find("name=password") != std::string::npos);
    assert(html.find("name=offset") != std::string::npos);
    assert(html.find("Clear Wi-Fi") != std::string::npos);
    assert(html.find("/api/wifi/clear/request") != std::string::npos);
    assert(html.find("/api/wifi/clear/confirm") != std::string::npos);
    assert(html.find("Wi-Fi changes are enabled by the next M0 step") == std::string::npos);
    assert(html.find("captive") == std::string::npos);

    const std::array<deck_setup_scan_result_t, 2> networks = {{
        {"Office", -42, true},
        {"Guest\\\"WiFi", -71, false},
    }};
    const deck_wifi_config_snapshot_t wifi = {
        DECK_WIFI_CONFIG_VALIDATING,
        DECK_WIFI_RECORD_VALID,
        DECK_WIFI_RECORD_VALID,
        true,
        true,
        7,
        "Office",
        "Candidate",
    };
    const deck_device_settings_snapshot_t settings = {
        DECK_DEVICE_SETTINGS_ACTIVE,
        DECK_DEVICE_SETTINGS_RECORD_VALID,
        DECK_DEVICE_SETTINGS_RECORD_EMPTY,
        true,
        false,
        3,
        -35,
    };
    deck_companion_profiles_snapshot_t companions{};
    companions.record_status = DECK_COMPANION_RECORD_VALID;
    companions.has_active = true;
    companions.generation = 4;
    companions.count = 1;
    std::strcpy(companions.active_profile_id, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    companions.profiles[0].profile_version = 1;
    std::strcpy(companions.profiles[0].profile_id, companions.active_profile_id);
    std::strcpy(companions.profiles[0].display_name, "Desk Companion");
    std::strcpy(companions.profiles[0].hub_address, "desk.local:7780");
    std::strcpy(companions.profiles[0].certificate_fingerprint, companions.active_profile_id);
    companions.profiles[0].priority = 3;
    companions.profiles[0].last_success_unix_ms = 1234;
    char status[2'048];
    assert(deck_setup_http_render_status(
        &snapshot,
        &wifi,
        &settings,
        &companions,
        networks.data(),
        networks.size(),
        status,
        sizeof(status)
    ));
    const std::string json(status);
    assert(json.find("\"active\":true") != std::string::npos);
    assert(json.find("\"address\":\"192.168.4.1\"") != std::string::npos);
    assert(json.find("\"companions\":{") != std::string::npos);
    assert(json.find("\"hub_address\":\"desk.local:7780\"") != std::string::npos);
    assert(json.find("Desk Companion") != std::string::npos);
    assert(json.find("\"priority\":3") != std::string::npos);
    assert(json.find("\"last_success_unix_ms\":1234") != std::string::npos);
    assert(json.find("Office") != std::string::npos);
    assert(json.find("Guest\\\\\\\"WiFi") != std::string::npos);
    assert(json.find("\"state\":\"validating\"") != std::string::npos);
    assert(json.find("\"record\":\"valid\"") != std::string::npos);
    assert(json.find("\"active_ssid\":\"Office\"") != std::string::npos);
    assert(json.find("\"candidate_ssid\":\"Candidate\"") != std::string::npos);
    assert(json.find("\"device_settings\":{") != std::string::npos);
    assert(json.find("\"temperature_offset_tenths_c\":-35") != std::string::npos);
    assert(json.find(snapshot.password) == std::string::npos);
    assert(json.find("correct-horse") == std::string::npos);
    assert(json.find("token") == std::string::npos);

    deck_setup_mode_destroy(setup);
}

void companion_requests_are_strict_and_do_not_accept_secret_shaped_extras()
{
    deck_companion_pair_request_t pair{};
    const char valid_pair[] = "hub_address=desk.local%3A7780&code=012345";
    assert(deck_setup_http_parse_companion_pair_request(
        valid_pair, sizeof(valid_pair) - 1, &pair
    ) == DECK_SETUP_COMPANION_REQUEST_OK);
    assert(std::string(pair.hub_address) == "desk.local:7780");
    assert(std::string(pair.code) == "012345");

    const char path[] = "hub_address=desk.local%3A7780%2Fapi&code=012345";
    assert(deck_setup_http_parse_companion_pair_request(path, sizeof(path) - 1, &pair) ==
           DECK_SETUP_COMPANION_REQUEST_INVALID_ADDRESS);
    const char duplicate[] =
        "hub_address=desk.local%3A7780&hub_address=other.local%3A7780&code=012345";
    assert(deck_setup_http_parse_companion_pair_request(
               duplicate, sizeof(duplicate) - 1, &pair
           ) == DECK_SETUP_COMPANION_REQUEST_MALFORMED);
    const char secret_extra[] =
        "hub_address=desk.local%3A7780&code=012345&token=should-not-be-accepted";
    assert(deck_setup_http_parse_companion_pair_request(
               secret_extra, sizeof(secret_extra) - 1, &pair
           ) == DECK_SETUP_COMPANION_REQUEST_MALFORMED);

    char profile_id[DECK_COMPANION_PROFILE_ID_CAPACITY];
    const char valid_id[] =
        "profile_id=sha256%3Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    assert(deck_setup_http_parse_companion_profile_request(
        valid_id, sizeof(valid_id) - 1, profile_id, sizeof(profile_id)
    ));
    assert(std::string(profile_id).rfind("sha256:", 0) == 0);
    assert(!deck_setup_http_parse_companion_profile_request(
        "profile_id=x", 12, profile_id, sizeof(profile_id)
    ));
}

void temperature_and_confirmation_parsers_are_strict()
{
    int16_t offset = 0;
    assert(deck_setup_http_parse_temperature_request("offset=-4.0", 11, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_OK);
    assert(offset == -40);
    assert(deck_setup_http_parse_temperature_request("offset=%2B15.000", 16, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_OK);
    assert(offset == 150);
    assert(deck_setup_http_parse_temperature_request("offset=-15", 10, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_OK);
    assert(offset == -150);
    assert(deck_setup_http_parse_temperature_request("offset=15.1", 11, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_OUT_OF_RANGE);
    assert(deck_setup_http_parse_temperature_request("offset=1.25", 11, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_NOT_EXACT_TENTH);
    assert(deck_setup_http_parse_temperature_request("offset=one", 10, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC);
    assert(deck_setup_http_parse_temperature_request("other=-4.0", 10, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_MALFORMED);
    assert(deck_setup_http_parse_temperature_request("offset=", 7, &offset) ==
           DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC);

    char token[DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY];
    assert(deck_setup_http_parse_confirmation_request(
        "token=0001020304050607", 22, token, sizeof(token)
    ));
    assert(std::string(token) == "0001020304050607");
    assert(!deck_setup_http_parse_confirmation_request(
        "token=00010203&other=x", 22, token, sizeof(token)
    ));
    assert(!deck_setup_http_parse_confirmation_request("token=", 6, token, sizeof(token)));
}

void wifi_submission_parser_rejects_malformed_or_invalid_credentials()
{
    deck_wifi_credentials_t parsed{};
    assert(deck_setup_http_parse_wifi_request(
               "ssid=Office+WiFi&password=correct%2Dhorse",
               std::strlen("ssid=Office+WiFi&password=correct%2Dhorse"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_OK);
    assert(std::string(parsed.ssid) == "Office WiFi");
    assert(std::string(parsed.password) == "correct-horse");

    assert(deck_setup_http_parse_wifi_request(
               "password=correct-horse&ssid=Office",
               std::strlen("password=correct-horse&ssid=Office"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_OK);
    assert(deck_setup_http_parse_wifi_request(
               "ssid=OpenNetwork&password=",
               std::strlen("ssid=OpenNetwork&password="),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_OK);
    assert(deck_setup_http_parse_wifi_request(
               "ssid=&password=correct-horse",
               std::strlen("ssid=&password=correct-horse"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_INVALID_SSID);
    assert(deck_setup_http_parse_wifi_request(
               "ssid=Office&password=short",
               std::strlen("ssid=Office&password=short"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_INVALID_PASSWORD);
    assert(deck_setup_http_parse_wifi_request(
               "ssid=Office%00Hidden&password=correct-horse",
               std::strlen("ssid=Office%00Hidden&password=correct-horse"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_MALFORMED);
    assert(deck_setup_http_parse_wifi_request(
               "ssid=Office&ssid=Other&password=correct-horse",
               std::strlen("ssid=Office&ssid=Other&password=correct-horse"),
               &parsed
           ) == DECK_SETUP_WIFI_REQUEST_MALFORMED);
    assert(deck_setup_http_parse_wifi_request(nullptr, 1, &parsed) ==
           DECK_SETUP_WIFI_REQUEST_MALFORMED);
}

void production_scan_conversion_bounds_and_terminates_ssids()
{
    static constexpr uint8_t long_ssid[] =
        "123456789012345678901234567890123456";
    static constexpr uint8_t short_ssid[] = "Office";
    const std::array<deck_setup_scan_observation_t, 2> observations = {{
        {long_ssid, sizeof(long_ssid) - 1, -80, true},
        {short_ssid, sizeof(short_ssid) - 1, -42, false},
    }};
    std::array<deck_setup_scan_result_t, 2> results{};
    size_t result_count = 99;
    assert(deck_setup_http_convert_scan_results(
        observations.data(), observations.size(), results.data(), results.size(), &result_count
    ));
    assert(result_count == 2);
    assert(std::strlen(results[0].ssid) == DECK_SETUP_SCAN_SSID_CAPACITY - 1);
    assert(results[0].ssid[DECK_SETUP_SCAN_SSID_CAPACITY - 1] == '\0');
    assert(results[0].rssi == -80);
    assert(results[0].secure);
    assert(std::string(results[1].ssid) == "Office");
    assert(results[1].rssi == -42);
    assert(!results[1].secure);

    std::array<deck_setup_scan_result_t, 1> limited_results{};
    assert(deck_setup_http_convert_scan_results(
        observations.data(),
        observations.size(),
        limited_results.data(),
        limited_results.size(),
        &result_count
    ));
    assert(result_count == 1);

    assert(!deck_setup_http_convert_scan_results(
        nullptr, 1, results.data(), results.size(), &result_count
    ));
}

}  // namespace

int main()
{
    first_boot_without_wifi_enters_setup();
    valid_wifi_boot_stays_idle_but_boot_long_press_enters();
    credentials_are_readable_and_change_on_reentry();
    failed_credential_generation_preserves_the_active_session();
    only_explicit_activity_refreshes_the_timeout();
    explicit_stop_clears_ephemeral_credentials();
    http_contract_exposes_profile_pairing_without_secrets();
    companion_requests_are_strict_and_do_not_accept_secret_shaped_extras();
    wifi_submission_parser_rejects_malformed_or_invalid_credentials();
    temperature_and_confirmation_parsers_are_strict();
    production_scan_conversion_bounds_and_terminates_ssids();
    return 0;
}
