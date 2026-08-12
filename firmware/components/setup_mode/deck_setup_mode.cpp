#include "deck_setup_mode.h"

#include <cstdio>
#include <cstring>
#include <new>

struct deck_setup_mode {
    uint64_t inactivity_timeout_ms;
    deck_setup_random_fn random;
    void *random_context;
    deck_setup_snapshot_t snapshot;
};

namespace {

constexpr char kReadableAlphabet[] = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
constexpr size_t kReadableAlphabetSize = sizeof(kReadableAlphabet) - 1;
constexpr char kHexAlphabet[] = "0123456789ABCDEF";
constexpr char kAddress[] = "192.168.4.1";

bool generate_credentials(deck_setup_mode_t *setup)
{
    uint8_t random_bytes[14]{};
    if (!setup->random(setup->random_context, random_bytes, sizeof(random_bytes))) {
        return false;
    }

    const int ssid_size = std::snprintf(
        setup->snapshot.ssid,
        sizeof(setup->snapshot.ssid),
        "S3-RLCD-%c%c%c%c",
        kHexAlphabet[random_bytes[0] >> 4U],
        kHexAlphabet[random_bytes[0] & 0x0fU],
        kHexAlphabet[random_bytes[1] >> 4U],
        kHexAlphabet[random_bytes[1] & 0x0fU]
    );
    if (ssid_size != DECK_SETUP_SSID_CAPACITY - 1) {
        return false;
    }

    size_t output_index = 0;
    for (size_t random_index = 2; random_index < sizeof(random_bytes); ++random_index) {
        if (output_index == 4 || output_index == 9) {
            setup->snapshot.password[output_index++] = '-';
        }
        const size_t alphabet_index = random_bytes[random_index] % kReadableAlphabetSize;
        setup->snapshot.password[output_index++] = kReadableAlphabet[alphabet_index];
    }
    setup->snapshot.password[output_index] = '\0';
    return output_index == DECK_SETUP_PASSWORD_CAPACITY - 1;
}

void clear_credentials(deck_setup_snapshot_t *snapshot)
{
    std::memset(snapshot->ssid, 0, sizeof(snapshot->ssid));
    std::memset(snapshot->password, 0, sizeof(snapshot->password));
}

}  // namespace

deck_setup_mode_t *deck_setup_mode_create(const deck_setup_mode_config_t *config)
{
    if (config == nullptr || config->inactivity_timeout_ms == 0 || config->random == nullptr) {
        return nullptr;
    }
    auto *setup = new (std::nothrow) deck_setup_mode_t{};
    if (setup == nullptr) {
        return nullptr;
    }
    setup->inactivity_timeout_ms = config->inactivity_timeout_ms;
    setup->random = config->random;
    setup->random_context = config->random_context;
    std::memcpy(setup->snapshot.address, kAddress, sizeof(kAddress));
    return setup;
}

void deck_setup_mode_destroy(deck_setup_mode_t *setup)
{
    if (setup != nullptr) {
        clear_credentials(&setup->snapshot);
    }
    delete setup;
}

deck_setup_mode_result_t deck_setup_mode_boot(
    deck_setup_mode_t *setup,
    bool has_valid_wifi_config,
    uint64_t now_ms
)
{
    if (setup == nullptr) {
        return DECK_SETUP_MODE_ERROR;
    }
    return has_valid_wifi_config
               ? DECK_SETUP_MODE_UNCHANGED
               : deck_setup_mode_enter(setup, DECK_SETUP_REASON_NO_WIFI, now_ms);
}

deck_setup_mode_result_t deck_setup_mode_enter(
    deck_setup_mode_t *setup,
    deck_setup_reason_t reason,
    uint64_t now_ms
)
{
    if (setup == nullptr || reason == DECK_SETUP_REASON_NONE) {
        return DECK_SETUP_MODE_ERROR;
    }
    const deck_setup_snapshot_t previous = setup->snapshot;
    const bool was_active = setup->snapshot.active;
    clear_credentials(&setup->snapshot);
    if (!generate_credentials(setup)) {
        setup->snapshot = previous;
        return DECK_SETUP_MODE_ERROR;
    }
    setup->snapshot.active = true;
    setup->snapshot.reason = reason;
    ++setup->snapshot.session_id;
    setup->snapshot.started_at_ms = now_ms;
    setup->snapshot.last_activity_ms = now_ms;
    return was_active ? DECK_SETUP_MODE_RESTARTED : DECK_SETUP_MODE_STARTED;
}

bool deck_setup_mode_activity(deck_setup_mode_t *setup, uint64_t now_ms)
{
    if (setup == nullptr || !setup->snapshot.active) {
        return false;
    }
    setup->snapshot.last_activity_ms = now_ms;
    return true;
}

deck_setup_mode_result_t deck_setup_mode_tick(deck_setup_mode_t *setup, uint64_t now_ms)
{
    if (setup == nullptr) {
        return DECK_SETUP_MODE_ERROR;
    }
    if (!setup->snapshot.active || now_ms < setup->snapshot.last_activity_ms ||
        now_ms - setup->snapshot.last_activity_ms < setup->inactivity_timeout_ms) {
        return DECK_SETUP_MODE_UNCHANGED;
    }
    return deck_setup_mode_stop(setup);
}

deck_setup_mode_result_t deck_setup_mode_stop(deck_setup_mode_t *setup)
{
    if (setup == nullptr) {
        return DECK_SETUP_MODE_ERROR;
    }
    if (!setup->snapshot.active) {
        return DECK_SETUP_MODE_UNCHANGED;
    }
    setup->snapshot.active = false;
    setup->snapshot.reason = DECK_SETUP_REASON_NONE;
    clear_credentials(&setup->snapshot);
    return DECK_SETUP_MODE_STOPPED;
}

bool deck_setup_mode_snapshot(const deck_setup_mode_t *setup, deck_setup_snapshot_t *snapshot)
{
    if (setup == nullptr || snapshot == nullptr) {
        return false;
    }
    *snapshot = setup->snapshot;
    return true;
}
