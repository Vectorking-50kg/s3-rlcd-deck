#include "deck_ai_page_view_model.h"

#include <cassert>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <iterator>
#include <string>

namespace {

static_assert(
    DECK_AI_PAGE_TOP_OFFSET +
        DECK_AI_PAGE_MAX_LINES * DECK_AI_PAGE_FONT_LINE_HEIGHT +
        (DECK_AI_PAGE_MAX_LINES - 1U) * DECK_AI_PAGE_LINE_SPACING <=
    300U
);

bool ends_with(const std::string &value, const char *suffix)
{
    const size_t suffix_size = std::strlen(suffix);
    return value.size() >= suffix_size &&
           value.compare(value.size() - suffix_size, suffix_size, suffix) == 0;
}

void assert_snapshot(const char *name, const std::string &actual)
{
    const std::string path = std::string(DECK_REPOSITORY_ROOT) +
                             "/tests/host/snapshots/" + name + ".txt";
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    std::string expected{
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    };
    if (!expected.empty() && expected.back() == '\n') {
        expected.pop_back();
    }
    if (actual != expected) {
        std::fprintf(stderr, "snapshot %s mismatch:\n%s\n", name, actual.c_str());
    }
    assert(actual == expected);
}

deck_ai_page_view_model_t full_model()
{
    deck_ai_page_view_model_t model{};
    model.active = true;
    model.rtc_available = true;
    model.rtc_hour = 20;
    model.rtc_minute = 36;
    model.temperature_available = true;
    model.calibrated_temperature_tenths_c = 248;
    model.wifi_state = DECK_AI_PAGE_WIFI_CONNECTED;
    model.wifi_signal_bars = 3;
    model.companion_state = DECK_AI_PAGE_COMPANION_ONLINE;
    model.snapshot_state = DECK_AI_PAGE_SNAPSHOT_FRESH;
    model.trusted_utc_ms = 1'775'000'000'000ULL;
    model.codex.provider_present = true;
    std::strcpy(model.codex.provider_display_name, "Codex");
    model.codex.provider_status = DECK_AI_SNAPSHOT_PROVIDER_OK;
    model.codex.provider_confidence = DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED;
    model.codex.window_count = 2;
    std::strcpy(model.codex.windows[0].name, "primary");
    model.codex.windows[0].has_remaining_basis_points = true;
    model.codex.windows[0].remaining_basis_points = 6200;
    model.codex.windows[0].has_resets_at = true;
    model.codex.windows[0].resets_at_unix_ms = model.trusted_utc_ms + 9'000'000ULL;
    std::strcpy(model.codex.windows[1].name, "weekly");
    model.codex.windows[1].has_used_basis_points = true;
    model.codex.windows[1].used_basis_points = 2200;
    model.codex.windows[1].has_window_minutes = true;
    model.codex.windows[1].window_minutes = 10'080;
    model.codex.has_total_tokens = true;
    model.codex.total_tokens = 146'000;
    model.codex.session_count = 3;
    model.codex.featured_session.present = true;
    model.codex.featured_session.has_display_name = true;
    std::strcpy(model.codex.featured_session.display_name, "Deck development");
    model.codex.featured_session.state = DECK_AI_SNAPSHOT_SESSION_RUNNING;
    model.codex.featured_session.confidence = DECK_AI_SNAPSHOT_CONFIDENCE_INFERRED;
    model.codex.featured_session.has_duration_seconds = true;
    model.codex.featured_session.duration_seconds = 720;
    model.codex.featured_session.has_turn_tokens = true;
    model.codex.featured_session.turn_tokens = 18'420;
    model.codex.featured_session.has_context_used_basis_points = true;
    model.codex.featured_session.context_used_basis_points = 4'100;
    return model;
}

void formats_online_codex_snapshot()
{
    const deck_ai_page_view_model_t model = full_model();
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string page(text);
    assert_snapshot("ai-page-online", page);
    assert(page.find("20:36  +24.8C  WIFI 3/4  AGENT ON") != std::string::npos);
    assert(page.find("CODEX  VERIFIED") != std::string::npos);
    assert(page.find("PRIMARY [######----] R62% @2h30m") != std::string::npos);
    assert(page.find("WEEKLY  [##--------] U22% 7d") != std::string::npos);
    assert(page.find("TOKEN 146K") != std::string::npos);
    assert(page.find("DECK DEVELOPMENT") != std::string::npos);
    assert(page.find("RUNNING / INFERRED / 12m") != std::string::npos);
    assert(page.find("18.4K TOK / CTX 41% / +2 SESS") != std::string::npos);
    assert(ends_with(page, "TX DISARMED"));
}

void stale_keeps_values_but_unavailable_hides_them()
{
    deck_ai_page_view_model_t model = full_model();
    model.snapshot_state = DECK_AI_PAGE_SNAPSHOT_STALE;
    model.companion_state = DECK_AI_PAGE_COMPANION_OFFLINE;
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string stale(text);
    assert_snapshot("ai-page-stale", stale);
    assert(stale.find("CODEX  STALE / VERIFIED") != std::string::npos);
    assert(stale.find("62%") != std::string::npos);

    model.snapshot_state = DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE;
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string unavailable(text);
    assert_snapshot("ai-page-unavailable", unavailable);
    assert(unavailable.find("CODEX  UNAVAILABLE") != std::string::npos);
    assert(unavailable.find("AGENT OFFLINE") != std::string::npos);
    assert(unavailable.find("62%") == std::string::npos);
    assert(unavailable.find("TOKEN 0") == std::string::npos);
    assert(ends_with(unavailable, "TX DISARMED"));
}

void four_windows_and_long_unicode_stay_bounded()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex.window_count = 4;
    for (uint8_t index = 2; index < 4; ++index) {
        std::strcpy(model.codex.windows[index].name, index == 2 ? "team_fast" : "long_context_window");
        model.codex.windows[index].has_remaining_basis_points = true;
        model.codex.windows[index].remaining_basis_points = static_cast<uint16_t>(4'000U + index * 500U);
    }
    std::strcpy(
        model.codex.featured_session.display_name,
        "中文会话名称很长但不能截断UTF8字符边界"
    );
    char text[1024];
    const bool formatted = deck_ai_page_view_model_format(&model, text, sizeof(text));
    if (!formatted) {
        std::fprintf(stderr, "partial AI page:\n%s\n", text);
    }
    assert(formatted);
    const std::string page(text);
    size_t lines = 1;
    for (const char character : page) {
        if (character == '\n') {
            ++lines;
        }
    }
    assert(lines <= DECK_AI_PAGE_MAX_LINES);
    assert(page.find("TEAM_FAS") != std::string::npos);
    assert(page.find("LONG_CON") != std::string::npos);
    assert(page.find("中文会话") != std::string::npos);
    assert(ends_with(page, "TX DISARMED"));
}

void equality_covers_page_state()
{
    const deck_ai_page_view_model_t first = full_model();
    deck_ai_page_view_model_t second = first;
    assert(deck_ai_page_view_model_equal(&first, &second));
    second.codex.windows[0].remaining_basis_points += 100;
    assert(!deck_ai_page_view_model_equal(&first, &second));
}

void missing_sessions_and_provider_errors_stay_explicit()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex.featured_session = deck_ai_snapshot_session_projection_t{};
    model.codex.session_count = 0;
    model.codex.has_total_tokens = false;
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert_snapshot("ai-page-no-session", text);
    assert(std::string(text).find("NO CODEX SESSION") != std::string::npos);
    assert(std::string(text).find("TOKEN 0") == std::string::npos);

    model.codex.provider_status = DECK_AI_SNAPSHOT_PROVIDER_DEGRADED;
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert_snapshot("ai-page-provider-error", text);
    assert(std::string(text).find("CODEX  DEGRADED / VERIFIED") != std::string::npos);
}

void maps_wifi_rssi_to_four_visible_bars()
{
    assert(deck_ai_page_wifi_signal_bars(-40) == 4U);
    assert(deck_ai_page_wifi_signal_bars(-55) == 4U);
    assert(deck_ai_page_wifi_signal_bars(-56) == 3U);
    assert(deck_ai_page_wifi_signal_bars(-67) == 3U);
    assert(deck_ai_page_wifi_signal_bars(-68) == 2U);
    assert(deck_ai_page_wifi_signal_bars(-75) == 2U);
    assert(deck_ai_page_wifi_signal_bars(-76) == 1U);

    deck_ai_page_view_model_t model = full_model();
    model.wifi_signal_bars = 0;
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert(std::string(text).find("WIFI --") != std::string::npos);
    assert(std::string(text).find("WIFI 0/4") == std::string::npos);
}

void hides_quota_windows_without_a_known_ratio()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex.windows[0].has_remaining_basis_points = false;
    model.codex.windows[0].has_used_basis_points = false;
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string page(text);
    assert(page.find("PRIMARY") == std::string::npos);
    assert(page.find("??????????") == std::string::npos);
    assert(page.find("WEEKLY") != std::string::npos);
}

void provider_absence_does_not_claim_the_companion_is_offline()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex = deck_ai_snapshot_codex_projection_t{};
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string page(text);
    assert(page.find("AGENT ON") != std::string::npos);
    assert(page.find("NO CODEX DATA") != std::string::npos);
    assert(page.find("AGENT OFFLINE") == std::string::npos);
}

void trusted_utc_uses_the_snapshot_timezone_before_rtc()
{
    uint8_t hour = 0;
    uint8_t minute = 0;
    assert(deck_ai_page_local_time_from_utc(
        1'786'624'496'000ULL, "Asia/Shanghai", &hour, &minute
    ));
    assert(hour == 20U && minute == 34U);
    assert(deck_ai_page_local_time_from_utc(
        1'768'509'000'000ULL, "America/Los_Angeles", &hour, &minute
    ));
    assert(hour == 12U && minute == 30U);
    assert(deck_ai_page_local_time_from_utc(
        1'784'147'400'000ULL, "America/Los_Angeles", &hour, &minute
    ));
    assert(hour == 13U && minute == 30U);
    assert(!deck_ai_page_local_time_from_utc(
        1'786'624'496'000ULL, "Unknown/Zone", &hour, &minute
    ));

    deck_ai_page_view_model_t model = full_model();
    model.rtc_available = false;
    model.trusted_utc_ms = 1'786'624'496'000ULL;
    model.codex.has_timezone = true;
    std::strcpy(model.codex.timezone, "Asia/Shanghai");
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert(std::string(text).find("20:34") == 0U);
}

void widest_session_name_cannot_add_a_wrapped_fourteenth_line()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex.window_count = 4;
    for (uint8_t index = 2; index < 4; ++index) {
        std::strcpy(model.codex.windows[index].name, index == 2 ? "team" : "monthly");
        model.codex.windows[index].has_remaining_basis_points = true;
        model.codex.windows[index].remaining_basis_points = 5'000;
    }
    std::memset(model.codex.featured_session.display_name, 'W', 48);
    model.codex.featured_session.display_name[48] = '\0';
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string page(text);
    assert(page.find(std::string(DECK_AI_PAGE_MAX_SESSION_NAME_CELLS, 'W')) != std::string::npos);
    assert(page.find(std::string(DECK_AI_PAGE_MAX_SESSION_NAME_CELLS + 1U, 'W')) == std::string::npos);
    assert(ends_with(page, "TX DISARMED"));
}

void widest_quota_row_is_compact_and_duration_is_bounded()
{
    deck_ai_page_view_model_t model = full_model();
    model.codex.window_count = 4;
    for (uint8_t index = 0; index < model.codex.window_count; ++index) {
        std::memset(model.codex.windows[index].name, 'W', 16);
        model.codex.windows[index].name[16] = '\0';
        model.codex.windows[index].has_remaining_basis_points = false;
        model.codex.windows[index].has_used_basis_points = true;
        model.codex.windows[index].used_basis_points = 10'000;
        model.codex.windows[index].has_resets_at = true;
        model.codex.windows[index].resets_at_unix_ms = UINT64_MAX;
    }
    char text[1024];
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    const std::string page(text);
    assert(page.find("WWWWWWWW[##########] U100% @999d+") != std::string::npos);
    assert(page.find("WWWWWWWWW") == std::string::npos);
    assert(ends_with(page, "TX DISARMED"));
}

void formats_ordered_provider_and_configuration_hint_pages()
{
    deck_ai_page_view_model_t model = full_model();
    model.pages.provider_count = 2U;
    auto &codex = model.pages.providers[0];
    std::strcpy(codex.provider_id, "codex");
    std::strcpy(codex.display_name, "Codex");
    codex.status = DECK_AI_SNAPSHOT_PROVIDER_OK;
    auto &cursor = model.pages.providers[1];
    std::strcpy(cursor.provider_id, "cursor");
    std::strcpy(cursor.display_name, "Cursor Experimental");
    cursor.status = DECK_AI_SNAPSHOT_PROVIDER_DEGRADED;
    cursor.confidence = DECK_AI_SNAPSHOT_CONFIDENCE_INFERRED;
    cursor.experimental = true;
    cursor.has_updated_at = true;
    cursor.updated_at_unix_ms = model.trusted_utc_ms - 60'000ULL;
    cursor.has_balance = true;
    cursor.balance_amount_micros = 18'420'000ULL;
    std::strcpy(cursor.balance_currency, "USD");
    cursor.window_count = 1U;
    std::strcpy(cursor.windows[0].name, "billing");
    cursor.windows[0].has_used_basis_points = true;
    cursor.windows[0].used_basis_points = 7'100U;
    cursor.has_error = true;
    std::strcpy(cursor.error_code, "schema_changed");
    model.selected_provider = 1U;

    char text[1024]{};
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert_snapshot("ai-page-provider-experimental", text);
    const std::string provider_page(text);
    assert(provider_page.find("CURSOR EXPERIMENTAL") != std::string::npos);
    assert(provider_page.find("EXPERIMENTAL") != std::string::npos);
    assert(provider_page.find("BAL 18.42 USD") != std::string::npos);
    assert(provider_page.find("SCHEMA_CHANGED") != std::string::npos);

    model.pages.provider_count = 1U;
    model.selected_provider = 0U;
    model.configuration_hint = true;
    assert(deck_ai_page_view_model_format(&model, text, sizeof(text)));
    assert_snapshot("ai-page-provider-config", text);
    assert(std::string(text).find("ADD AI PROVIDER") != std::string::npos);
}

void selection_cycles_order_and_survives_dynamic_reconfiguration()
{
    deck_ai_page_view_model_t model = full_model();
    deck_ai_snapshot_pages_projection_t pages{};
    pages.provider_count = 3U;
    std::strcpy(pages.providers[0].provider_id, "codex");
    std::strcpy(pages.providers[1].provider_id, "deepseek");
    std::strcpy(pages.providers[2].provider_id, "cursor");
    assert(deck_ai_page_view_model_apply_pages(&model, &pages));
    assert(model.selected_provider == 0U && !model.configuration_hint);
    deck_ai_page_view_model_next(&model);
    assert(model.selected_provider == 1U && !model.configuration_hint);
    deck_ai_page_view_model_next(&model);
    assert(model.selected_provider == 2U && !model.configuration_hint);

    deck_ai_snapshot_pages_projection_t reordered = pages;
    std::swap(reordered.providers[1], reordered.providers[2]);
    assert(deck_ai_page_view_model_apply_pages(&model, &reordered));
    assert(model.selected_provider == 1U);
    assert(std::strcmp(model.pages.providers[model.selected_provider].provider_id, "cursor") == 0);

    deck_ai_snapshot_pages_projection_t codex_only{};
    codex_only.provider_count = 1U;
    std::strcpy(codex_only.providers[0].provider_id, "codex");
    assert(deck_ai_page_view_model_apply_pages(&model, &codex_only));
    assert(model.selected_provider == 0U && !model.configuration_hint);
    deck_ai_page_view_model_next(&model);
    assert(model.configuration_hint);
    deck_ai_page_view_model_next(&model);
    assert(!model.configuration_hint && model.selected_provider == 0U);
}

}  // namespace

int main()
{
    formats_online_codex_snapshot();
    stale_keeps_values_but_unavailable_hides_them();
    four_windows_and_long_unicode_stay_bounded();
    equality_covers_page_state();
    missing_sessions_and_provider_errors_stay_explicit();
    maps_wifi_rssi_to_four_visible_bars();
    hides_quota_windows_without_a_known_ratio();
    provider_absence_does_not_claim_the_companion_is_offline();
    trusted_utc_uses_the_snapshot_timezone_before_rtc();
    widest_session_name_cannot_add_a_wrapped_fourteenth_line();
    widest_quota_row_is_compact_and_duration_is_bounded();
    formats_ordered_provider_and_configuration_hint_pages();
    selection_cycles_order_and_survives_dynamic_reconfiguration();
    return 0;
}
