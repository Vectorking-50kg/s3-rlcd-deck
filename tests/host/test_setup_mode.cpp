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

void http_contract_is_read_only_and_labels_pairing_as_m1()
{
    assert(deck_setup_http_route("GET", "/") == DECK_SETUP_HTTP_PAGE);
    assert(deck_setup_http_route("GET", "/api/status") == DECK_SETUP_HTTP_STATUS);
    assert(deck_setup_http_route("POST", "/api/scan") == DECK_SETUP_HTTP_SCAN);
    assert(deck_setup_http_route("POST", "/api/wifi") == DECK_SETUP_HTTP_NOT_FOUND);
    assert(deck_setup_http_route("POST", "/api/pair") == DECK_SETUP_HTTP_NOT_FOUND);
    assert(deck_setup_http_route("GET", "/api/scan") == DECK_SETUP_HTTP_METHOD_NOT_ALLOWED);

    DeterministicRandom random{0};
    deck_setup_mode_t *setup = create_setup(&random);
    assert(setup != nullptr);
    assert(deck_setup_mode_boot(setup, false, 0) == DECK_SETUP_MODE_STARTED);
    deck_setup_snapshot_t snapshot{};
    assert(deck_setup_mode_snapshot(setup, &snapshot));

    char page[2'048];
    assert(deck_setup_http_render_page(page, sizeof(page)));
    const std::string html(page);
    assert(html.find("Setup / Recovery") != std::string::npos);
    assert(html.find("Scan networks") != std::string::npos);
    assert(html.find("Companion pairing is planned for M1") != std::string::npos);
    assert(html.find("password") == std::string::npos);
    assert(html.find("captive") == std::string::npos);

    const std::array<deck_setup_scan_result_t, 2> networks = {{
        {"Office", -42, true},
        {"Guest\\\"WiFi", -71, false},
    }};
    char status[1'024];
    assert(deck_setup_http_render_status(&snapshot, networks.data(), networks.size(), status,
                                         sizeof(status)));
    const std::string json(status);
    assert(json.find("\"active\":true") != std::string::npos);
    assert(json.find("\"address\":\"192.168.4.1\"") != std::string::npos);
    assert(json.find("\"pairing\":\"m1_not_available\"") != std::string::npos);
    assert(json.find("Office") != std::string::npos);
    assert(json.find("Guest\\\\\\\"WiFi") != std::string::npos);
    assert(json.find(snapshot.password) == std::string::npos);

    deck_setup_mode_destroy(setup);
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
    http_contract_is_read_only_and_labels_pairing_as_m1();
    return 0;
}
