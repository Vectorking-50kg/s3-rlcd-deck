#include "deck_ui_scene.h"

#include <cassert>
#include <cstring>
#include <string>

namespace {

deck_m0_view_model_t base_model()
{
    deck_m0_view_model_t model{};
    model.firmware_version = "0.3.0-dev";
    model.data_source = DECK_DATA_VERIFIED;
    model.rtc_available = true;
    model.rtc_hour = 20;
    model.rtc_minute = 36;
    model.sensor_available = true;
    model.calibrated_temperature_tenths_c = 248;
    model.humidity_tenths_percent = 530;
    model.wifi_state = DECK_WIFI_CONNECTED;
    model.setup_state = DECK_SETUP_IDLE;
    model.minimum_free_heap_bytes = 8U * 1'024U * 1'024U;
    model.ai_page.companion_state = DECK_AI_PAGE_COMPANION_UNPAIRED;
    return model;
}

deck_m0_view_model_t ai_model()
{
    deck_m0_view_model_t model = base_model();
    model.ai_page.active = true;
    model.ai_page.temperature_available = true;
    model.ai_page.calibrated_temperature_tenths_c = 248;
    model.ai_page.rtc_available = true;
    model.ai_page.rtc_hour = 20;
    model.ai_page.rtc_minute = 36;
    model.ai_page.wifi_state = DECK_AI_PAGE_WIFI_CONNECTED;
    model.ai_page.wifi_signal_bars = 3;
    model.ai_page.companion_state = DECK_AI_PAGE_COMPANION_ONLINE;
    model.ai_page.snapshot_state = DECK_AI_PAGE_SNAPSHOT_FRESH;
    model.ai_page.trusted_utc_ms = 1'775'000'000'000ULL;
    model.ai_page.codex.provider_present = true;
    std::strcpy(model.ai_page.codex.provider_display_name, "Codex");
    model.ai_page.codex.provider_status = DECK_AI_SNAPSHOT_PROVIDER_OK;
    model.ai_page.codex.provider_confidence = DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED;
    model.ai_page.codex.window_count = 2;
    std::strcpy(model.ai_page.codex.windows[0].name, "primary");
    model.ai_page.codex.windows[0].has_remaining_basis_points = true;
    model.ai_page.codex.windows[0].remaining_basis_points = 6'200;
    model.ai_page.codex.windows[0].has_resets_at = true;
    model.ai_page.codex.windows[0].resets_at_unix_ms =
        model.ai_page.trusted_utc_ms + 9'000'000ULL;
    std::strcpy(model.ai_page.codex.windows[1].name, "weekly");
    model.ai_page.codex.windows[1].has_used_basis_points = true;
    model.ai_page.codex.windows[1].used_basis_points = 2'200;
    model.ai_page.codex.windows[1].has_window_minutes = true;
    model.ai_page.codex.windows[1].window_minutes = 10'080;
    model.ai_page.codex.featured_session.present = true;
    model.ai_page.codex.featured_session.has_display_name = true;
    std::strcpy(model.ai_page.codex.featured_session.display_name, "Deck 中文界面开发");
    model.ai_page.codex.featured_session.state = DECK_AI_SNAPSHOT_SESSION_RUNNING;
    model.ai_page.codex.featured_session.has_turn_tokens = true;
    model.ai_page.codex.featured_session.turn_tokens = 18'420;
    model.ai_page.codex.featured_session.has_context_used_basis_points = true;
    model.ai_page.codex.featured_session.context_used_basis_points = 4'100;
    model.ai_page.codex.session_count = 3;
    return model;
}

void board_is_a_visual_dashboard_instead_of_a_diagnostic_log()
{
    const deck_m0_view_model_t model = base_model();
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.kind == DECK_UI_SCENE_BOARD);
    assert(std::string(scene.title) == "Deck 状态");
    assert(std::string(scene.badge) == "设备正常");
    assert(std::string(scene.hero) == "+24.8°C");
    assert(scene.metric_count == 3U);
    assert(std::string(scene.metrics[0].label) == "湿度");
    assert(std::string(scene.metrics[0].value) == "53.0%");
    assert(std::string(scene.footer_left) == "TX 未启用");
    assert(std::string(scene.summary_detail).find("最低堆") != std::string::npos);
}

void pairing_has_highest_priority_and_a_large_grouped_code()
{
    deck_m0_view_model_t model = ai_model();
    model.setup_state = DECK_SETUP_ACTIVE;
    model.serial.state = DECK_SERIAL_VIEW_WEB_TX;
    model.pairing_v2.state = DECK_PAIRING_V2_ACTIVE;
    std::strcpy(model.pairing_v2.code, "123456");
    model.pairing_v2.remaining_seconds = 87U;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.kind == DECK_UI_SCENE_PAIRING);
    assert(scene.centered);
    assert(scene.hero_is_code);
    assert(std::string(scene.hero) == "123 456");
    assert(std::string(scene.message) == "请在 Mac 配对页输入验证码");
    assert(std::string(scene.detail) == "剩余 87 秒");
    assert(std::string(scene.footer_right) == "BOOT 取消");
}

void pairing_failure_explains_that_existing_trust_is_preserved()
{
    deck_m0_view_model_t model = base_model();
    model.pairing_v2.state = DECK_PAIRING_V2_ERROR;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.hero) == "配对失败");
    assert(std::string(scene.message) == "原有 Profile 保持不变");
    assert(scene.badge_style == DECK_UI_BADGE_ALERT);
}

void setup_credentials_are_structured_as_ephemeral_rows()
{
    deck_m0_view_model_t model = base_model();
    model.setup_state = DECK_SETUP_ACTIVE;
    std::strcpy(model.setup_ssid, "S3-DECK-A17F");
    std::strcpy(model.setup_password, "MINT-WAVE-7294");
    std::strcpy(model.setup_address, "192.168.4.1");
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.kind == DECK_UI_SCENE_SETUP);
    assert(scene.metric_count == 3U);
    assert(std::string(scene.metrics[0].value) == "S3-DECK-A17F");
    assert(std::string(scene.metrics[1].value) == "MINT-WAVE-7294");
    assert(std::string(scene.metrics[2].value) == "http://192.168.4.1");
}

void codex_uses_chinese_labels_and_real_progress_metrics()
{
    const deck_m0_view_model_t model = ai_model();
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.kind == DECK_UI_SCENE_AI);
    assert(std::string(scene.badge) == "已验证");
    assert(scene.metric_count == 2U);
    assert(std::string(scene.metrics[0].label) == "主要额度");
    assert(scene.metrics[0].has_progress);
    assert(scene.metrics[0].basis_points == 6'200U);
    assert(std::string(scene.metrics[0].value) == "剩余 62%");
    assert(std::string(scene.metrics[1].label) == "每周额度");
    assert(std::string(scene.metrics[1].value) == "已用 22%");
    assert(std::string(scene.summary_title) == "Deck 中文界面开发");
    assert(std::string(scene.summary_value) == "运行中");
    assert(std::string(scene.summary_detail).find("上下文 41%") != std::string::npos);
    assert(std::string(scene.footer_left) == "TX 未启用");
}

void unavailable_ai_data_is_explicit_and_does_not_show_zeroes()
{
    deck_m0_view_model_t model = ai_model();
    model.ai_page.snapshot_state = DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE;
    model.ai_page.companion_state = DECK_AI_PAGE_COMPANION_OFFLINE;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.centered);
    assert(std::string(scene.hero) == "暂无 Codex 数据");
    assert(std::string(scene.message) == "Active Companion 当前离线");
    assert(scene.metric_count == 0U);
}

void serial_scene_keeps_payload_out_and_makes_the_owner_obvious()
{
    deck_m0_view_model_t model = ai_model();
    model.serial.state = DECK_SERIAL_VIEW_WEB_TX;
    model.serial.session_id = 7U;
    model.serial.owner_generation = 11U;
    model.serial.usb_tx_rejected = 23U;
    model.serial.uart_install_failures = 2U;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(scene.kind == DECK_UI_SCENE_SERIAL);
    assert(std::string(scene.badge) == "Web TX");
    assert(std::string(scene.hero) == "115200 · 8N1");
    assert(std::string(scene.footer_left) == "Web TX");
    assert(scene.metric_count == 4U);
    assert(std::string(scene.metrics[0].value) == "#7");
}

void semantic_scene_equality_detects_visible_changes()
{
    const deck_m0_view_model_t model = ai_model();
    deck_ui_scene_t left{};
    assert(deck_ui_scene_project(&model, &left));
    deck_ui_scene_t right = left;
    assert(deck_ui_scene_equal(&left, &right));
    right.metrics[0].basis_points += 100U;
    assert(!deck_ui_scene_equal(&left, &right));
    right = left;
    std::strcpy(right.badge, "数据已过期");
    assert(!deck_ui_scene_equal(&left, &right));
}

}  // namespace

int main()
{
    board_is_a_visual_dashboard_instead_of_a_diagnostic_log();
    pairing_has_highest_priority_and_a_large_grouped_code();
    pairing_failure_explains_that_existing_trust_is_preserved();
    setup_credentials_are_structured_as_ephemeral_rows();
    codex_uses_chinese_labels_and_real_progress_metrics();
    unavailable_ai_data_is_explicit_and_does_not_show_zeroes();
    serial_scene_keeps_payload_out_and_makes_the_owner_obvious();
    semantic_scene_equality_detects_visible_changes();
    return 0;
}
