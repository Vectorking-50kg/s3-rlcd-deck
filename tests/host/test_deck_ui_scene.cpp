#include "deck_ui_scene.h"
#include "deck_ui_layout.h"
#include "deck_ui_preview.h"

#include <cassert>
#include <cstring>
#include <string>

namespace {

constexpr int32_t kFooterDividerY = 258;

int32_t bottom(deck_ui_rect_t rectangle)
{
    return static_cast<int32_t>(rectangle.y) + rectangle.height;
}

void assert_scene_fits_panel(const deck_ui_scene_t &scene)
{
    deck_ui_layout_t layout{};
    assert(deck_ui_layout_plan(&scene, &layout));
    assert(deck_ui_rect_within_display(layout.hero));
    assert(deck_ui_rect_within_display(layout.message));
    assert(deck_ui_rect_within_display(layout.detail));

    int32_t previous_bottom = -1;
    for (size_t index = 0U; index < DECK_UI_SCENE_MAX_METRICS; ++index) {
        if (!layout.metric_visible[index]) {
            continue;
        }
        assert(deck_ui_rect_within_display(layout.metric_rows[index]));
        assert(layout.metric_rows[index].y >= previous_bottom);
        assert(bottom(layout.metric_rows[index]) <= kFooterDividerY);
        previous_bottom = bottom(layout.metric_rows[index]);
    }
    if (layout.summary_visible) {
        assert(deck_ui_rect_within_display(layout.summary));
        assert(layout.summary.y >= previous_bottom);
        assert(bottom(layout.summary) <= kFooterDividerY);
    }
}

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
    assert_scene_fits_panel(scene);
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
    assert_scene_fits_panel(scene);
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
    assert_scene_fits_panel(scene);
}

void every_pairing_transition_has_distinct_chinese_feedback()
{
    struct PairingCase {
        deck_pairing_v2_state_t state;
        const char *hero;
        const char *message;
        const char *detail;
    };
    constexpr PairingCase cases[] = {
        {DECK_PAIRING_V2_AUTHENTICATING, "123 456", "正在认证 Companion", "剩余 62 秒"},
        {DECK_PAIRING_V2_PROOF_VERIFIED, "123 456", "安全证明已通过", "剩余 62 秒"},
        {DECK_PAIRING_V2_PAIRED, "配对成功", "安全连接已经建立", "Companion Profile 已安全保存"},
        {DECK_PAIRING_V2_EXPIRED, "验证码已过期", "没有修改任何信任配置", "请按 BOOT 重新开始配对"},
        {DECK_PAIRING_V2_ERROR, "配对失败", "原有 Profile 保持不变", "请检查网络后按 BOOT 重试"},
    };
    for (const PairingCase &expected : cases) {
        deck_m0_view_model_t model = base_model();
        model.pairing_v2.state = expected.state;
        std::strcpy(model.pairing_v2.code, "123456");
        model.pairing_v2.remaining_seconds = 62U;
        deck_ui_scene_t scene{};
        assert(deck_ui_scene_project(&model, &scene));
        assert(std::string(scene.hero) == expected.hero);
        assert(std::string(scene.message) == expected.message);
        assert(std::string(scene.detail) == expected.detail);
        assert_scene_fits_panel(scene);
    }
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
    assert_scene_fits_panel(scene);
}

void setup_validation_and_failure_keep_recovery_actions_obvious()
{
    deck_m0_view_model_t model = base_model();
    model.setup_state = DECK_SETUP_ACTIVE;
    std::strcpy(model.setup_ssid, "S3-DECK-A17F");
    std::strcpy(model.setup_password, "MINT-WAVE-7294");
    std::strcpy(model.setup_address, "192.168.4.1");
    model.wifi_config_state = DECK_WIFI_CONFIG_VIEW_VALIDATING;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.badge) == "正在验证");
    assert(std::string(scene.summary_title) == "正在验证家庭 Wi-Fi");
    assert(std::string(scene.summary_detail) == "原配置保持不变，请稍候");
    assert_scene_fits_panel(scene);

    model.wifi_config_state = DECK_WIFI_CONFIG_VIEW_AUTH_FAILED;
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.badge) == "认证失败");
    assert(std::string(scene.summary_title) == "Wi-Fi 认证失败");
    assert(std::string(scene.summary_detail).find("原配置保持不变") != std::string::npos);
    assert(scene.badge_style == DECK_UI_BADGE_ALERT);
    assert_scene_fits_panel(scene);
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
    assert_scene_fits_panel(scene);
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
    assert_scene_fits_panel(scene);
}

void stale_ai_snapshot_keeps_the_last_summary_but_marks_it_untrusted()
{
    deck_m0_view_model_t model = ai_model();
    model.ai_page.snapshot_state = DECK_AI_PAGE_SNAPSHOT_STALE;
    model.ai_page.companion_state = DECK_AI_PAGE_COMPANION_OFFLINE;
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.badge) == "数据已过期");
    assert(std::string(scene.status_companion) == "Companion 离线");
    assert(scene.metric_count == 2U);
    assert(std::string(scene.summary_title) == "Deck 中文界面开发");
    assert_scene_fits_panel(scene);
}

void unsupported_dynamic_chinese_never_renders_as_missing_glyph_boxes()
{
    deck_m0_view_model_t model = ai_model();
    std::strcpy(model.ai_page.codex.featured_session.display_name, "未收录龘字");
    std::strcpy(model.ai_page.codex.windows[0].name, "龘额度");
    deck_ui_scene_t scene{};
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.summary_title) == "会话名称不可用");
    assert(std::string(scene.metrics[0].label) == "自定义额度");

    model.ai_page.pages.provider_count = 1U;
    model.ai_page.selected_provider = 0U;
    auto &provider = model.ai_page.pages.providers[0];
    std::strcpy(provider.provider_id, "custom");
    std::strcpy(provider.display_name, "龘 Provider");
    provider.status = DECK_AI_SNAPSHOT_PROVIDER_DEGRADED;
    provider.has_error = true;
    std::strcpy(provider.error_code, "错误龘");
    assert(deck_ui_scene_project(&model, &scene));
    assert(std::string(scene.title) == "自定义 Provider");
    assert(std::string(scene.summary_value) == "错误代码不可用");
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
    assert_scene_fits_panel(scene);
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

void layout_rejects_dense_centered_content_instead_of_covering_the_footer()
{
    deck_ui_scene_t scene{};
    scene.centered = true;
    std::strcpy(scene.hero, "123 456");
    scene.metric_count = 2U;
    deck_ui_layout_t layout{};
    assert(!deck_ui_layout_plan(&scene, &layout));
}

void every_visual_preview_is_named_deterministic_and_panel_safe()
{
    struct PreviewCase {
        const char *name;
        deck_ui_preview_page_t page;
    };
    constexpr PreviewCase cases[] = {
        {"board", DECK_UI_PREVIEW_BOARD},
        {"pairing", DECK_UI_PREVIEW_PAIRING},
        {"pairing-authenticating", DECK_UI_PREVIEW_PAIRING_AUTHENTICATING},
        {"pairing-verified", DECK_UI_PREVIEW_PAIRING_VERIFIED},
        {"pairing-success", DECK_UI_PREVIEW_PAIRING_SUCCESS},
        {"pairing-expired", DECK_UI_PREVIEW_PAIRING_EXPIRED},
        {"pairing-error", DECK_UI_PREVIEW_PAIRING_ERROR},
        {"setup", DECK_UI_PREVIEW_SETUP},
        {"setup-validating", DECK_UI_PREVIEW_SETUP_VALIDATING},
        {"setup-error", DECK_UI_PREVIEW_SETUP_ERROR},
        {"ai", DECK_UI_PREVIEW_AI},
        {"ai-stale", DECK_UI_PREVIEW_AI_STALE},
        {"provider", DECK_UI_PREVIEW_PROVIDER},
        {"configuration", DECK_UI_PREVIEW_CONFIGURATION},
        {"serial", DECK_UI_PREVIEW_SERIAL},
        {"offline", DECK_UI_PREVIEW_OFFLINE},
        {"error", DECK_UI_PREVIEW_ERROR},
    };
    for (const PreviewCase &preview : cases) {
        deck_ui_preview_page_t parsed = DECK_UI_PREVIEW_BOARD;
        assert(deck_ui_preview_page_parse(preview.name, &parsed));
        assert(parsed == preview.page);
        deck_ui_scene_t first{};
        deck_ui_scene_t second{};
        assert(deck_ui_preview_scene(parsed, &first));
        assert(deck_ui_preview_scene(parsed, &second));
        assert(deck_ui_scene_equal(&first, &second));
        assert(first.title[0] != '\0');
        assert(first.status_time[0] != '\0');
        assert_scene_fits_panel(first);
    }
    deck_ui_preview_page_t parsed = DECK_UI_PREVIEW_BOARD;
    assert(!deck_ui_preview_page_parse("unknown", &parsed));
    assert(!deck_ui_preview_page_parse(nullptr, &parsed));
    deck_ui_scene_t invalid{};
    assert(!deck_ui_preview_scene(static_cast<deck_ui_preview_page_t>(99), &invalid));
    assert(!deck_ui_preview_scene(DECK_UI_PREVIEW_BOARD, nullptr));
}

}  // namespace

int main()
{
    board_is_a_visual_dashboard_instead_of_a_diagnostic_log();
    pairing_has_highest_priority_and_a_large_grouped_code();
    pairing_failure_explains_that_existing_trust_is_preserved();
    every_pairing_transition_has_distinct_chinese_feedback();
    setup_credentials_are_structured_as_ephemeral_rows();
    setup_validation_and_failure_keep_recovery_actions_obvious();
    codex_uses_chinese_labels_and_real_progress_metrics();
    unavailable_ai_data_is_explicit_and_does_not_show_zeroes();
    stale_ai_snapshot_keeps_the_last_summary_but_marks_it_untrusted();
    unsupported_dynamic_chinese_never_renders_as_missing_glyph_boxes();
    serial_scene_keeps_payload_out_and_makes_the_owner_obvious();
    semantic_scene_equality_detects_visible_changes();
    layout_rejects_dense_centered_content_instead_of_covering_the_footer();
    every_visual_preview_is_named_deterministic_and_panel_safe();
    return 0;
}
