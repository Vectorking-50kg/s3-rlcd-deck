#include "deck_ui_scene.h"
#include "deck_m0_glyphs.h"

#include <inttypes.h>
#include <stdio.h>
#include <string.h>

namespace {

bool decode_utf8_codepoint(const char **cursor, uint32_t *codepoint)
{
    if (cursor == nullptr || *cursor == nullptr || codepoint == nullptr || **cursor == '\0') {
        return false;
    }
    const auto *bytes = reinterpret_cast<const uint8_t *>(*cursor);
    size_t length = 0U;
    uint32_t value = 0U;
    uint32_t minimum = 0U;
    if (bytes[0] < 0x80U) {
        length = 1U;
        value = bytes[0];
    } else if ((bytes[0] & 0xe0U) == 0xc0U) {
        length = 2U;
        value = bytes[0] & 0x1fU;
        minimum = 0x80U;
    } else if ((bytes[0] & 0xf0U) == 0xe0U) {
        length = 3U;
        value = bytes[0] & 0x0fU;
        minimum = 0x800U;
    } else if ((bytes[0] & 0xf8U) == 0xf0U) {
        length = 4U;
        value = bytes[0] & 0x07U;
        minimum = 0x10000U;
    } else {
        return false;
    }
    for (size_t index = 1U; index < length; ++index) {
        if (bytes[index] == 0U || (bytes[index] & 0xc0U) != 0x80U) {
            return false;
        }
        value = (value << 6U) | (bytes[index] & 0x3fU);
    }
    if (value < minimum || value > 0x10ffffU || (value >= 0xd800U && value <= 0xdfffU)) {
        return false;
    }
    *cursor += length;
    *codepoint = value;
    return true;
}

bool manifest_contains(uint32_t expected)
{
    const char *cursor = DECK_M0_REQUIRED_GLYPHS;
    while (*cursor != '\0') {
        uint32_t codepoint = 0U;
        if (!decode_utf8_codepoint(&cursor, &codepoint)) {
            return false;
        }
        if (codepoint == expected) {
            return true;
        }
    }
    return false;
}

bool display_text_supported(const char *value)
{
    if (value == nullptr || value[0] == '\0') {
        return false;
    }
    const char *cursor = value;
    while (*cursor != '\0') {
        uint32_t codepoint = 0U;
        if (!decode_utf8_codepoint(&cursor, &codepoint)) {
            return false;
        }
        if (codepoint >= 0x20U && codepoint <= 0x7eU) {
            continue;
        }
        if (!manifest_contains(codepoint)) {
            return false;
        }
    }
    return true;
}

const char *display_text_or(const char *value, const char *fallback)
{
    return display_text_supported(value) ? value : fallback;
}

template <size_t Capacity>
bool set_text(char (&output)[Capacity], const char *value)
{
    if (value == nullptr) {
        output[0] = '\0';
        return true;
    }
    size_t read = 0U;
    size_t written = 0U;
    while (value[read] != '\0') {
        const uint8_t first = static_cast<uint8_t>(value[read]);
        const size_t sequence = first < 0x80U
                                    ? 1U
                                    : (first & 0xe0U) == 0xc0U
                                          ? 2U
                                          : (first & 0xf0U) == 0xe0U
                                                ? 3U
                                                : (first & 0xf8U) == 0xf0U ? 4U : 0U;
        if (sequence == 0U || written + sequence >= Capacity) {
            break;
        }
        bool valid = true;
        for (size_t offset = 1U; offset < sequence; ++offset) {
            if ((static_cast<uint8_t>(value[read + offset]) & 0xc0U) != 0x80U) {
                valid = false;
                break;
            }
        }
        if (!valid) {
            break;
        }
        memcpy(output + written, value + read, sequence);
        written += sequence;
        read += sequence;
    }
    output[written] = '\0';
    return value[read] == '\0';
}

template <size_t Capacity, typename... Arguments>
bool format_text(char (&output)[Capacity], const char *format, Arguments... arguments)
{
    const int written = snprintf(output, Capacity, format, arguments...);
    return written >= 0 && static_cast<size_t>(written) < Capacity;
}

void compact_count(uint64_t value, char *output, size_t capacity)
{
    if (value >= 1'000'000'000ULL) {
        (void)snprintf(
            output,
            capacity,
            "%llu.%lluB",
            static_cast<unsigned long long>(value / 1'000'000'000ULL),
            static_cast<unsigned long long>(value / 100'000'000ULL % 10ULL)
        );
    } else if (value >= 1'000'000ULL) {
        (void)snprintf(
            output,
            capacity,
            "%llu.%lluM",
            static_cast<unsigned long long>(value / 1'000'000ULL),
            static_cast<unsigned long long>(value / 100'000ULL % 10ULL)
        );
    } else if (value >= 100'000ULL) {
        (void)snprintf(
            output,
            capacity,
            "%lluK",
            static_cast<unsigned long long>(value / 1'000ULL)
        );
    } else if (value >= 1'000ULL) {
        (void)snprintf(
            output,
            capacity,
            "%llu.%lluK",
            static_cast<unsigned long long>(value / 1'000ULL),
            static_cast<unsigned long long>(value / 100ULL % 10ULL)
        );
    } else {
        (void)snprintf(output, capacity, "%llu", static_cast<unsigned long long>(value));
    }
}

void duration_text(uint64_t seconds, char *output, size_t capacity)
{
    if (seconds >= 86'400ULL) {
        const uint64_t days = seconds / 86'400ULL;
        if (days > 999ULL) {
            (void)snprintf(output, capacity, "999天+");
        } else {
            (void)snprintf(
                output,
                capacity,
                "%llu天%02llu时",
                static_cast<unsigned long long>(days),
                static_cast<unsigned long long>(seconds / 3'600ULL % 24ULL)
            );
        }
    } else if (seconds >= 3'600ULL) {
        (void)snprintf(
            output,
            capacity,
            "%llu时%02llu分",
            static_cast<unsigned long long>(seconds / 3'600ULL),
            static_cast<unsigned long long>(seconds / 60ULL % 60ULL)
        );
    } else if (seconds >= 60ULL) {
        (void)snprintf(
            output,
            capacity,
            "%llu分",
            static_cast<unsigned long long>(seconds / 60ULL)
        );
    } else {
        (void)snprintf(output, capacity, "%llu秒", static_cast<unsigned long long>(seconds));
    }
}

const char *confidence_text(deck_ai_snapshot_confidence_t confidence)
{
    switch (confidence) {
        case DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED:
            return "已验证";
        case DECK_AI_SNAPSHOT_CONFIDENCE_INFERRED:
            return "推断";
        case DECK_AI_SNAPSHOT_CONFIDENCE_UNAVAILABLE:
        default:
            return "不可用";
    }
}

const char *session_state_text(deck_ai_snapshot_session_state_t state)
{
    switch (state) {
        case DECK_AI_SNAPSHOT_SESSION_RUNNING:
            return "运行中";
        case DECK_AI_SNAPSHOT_SESSION_WAITING_APPROVAL:
            return "等待批准";
        case DECK_AI_SNAPSHOT_SESSION_WAITING_INPUT:
            return "等待输入";
        case DECK_AI_SNAPSHOT_SESSION_COMPLETED:
            return "已完成";
        case DECK_AI_SNAPSHOT_SESSION_FAILED:
            return "失败";
        case DECK_AI_SNAPSHOT_SESSION_RECENT:
            return "最近活动";
        case DECK_AI_SNAPSHOT_SESSION_ENDED:
            return "已结束";
        case DECK_AI_SNAPSHOT_SESSION_UNKNOWN:
            return "未知";
        case DECK_AI_SNAPSHOT_SESSION_UNAVAILABLE:
        default:
            return "不可用";
    }
}

const char *window_name(const char *name)
{
    if (name == nullptr) {
        return "额度";
    }
    if (strcmp(name, "primary") == 0) {
        return "主要额度";
    }
    if (strcmp(name, "weekly") == 0) {
        return "每周额度";
    }
    if (strcmp(name, "monthly") == 0) {
        return "每月额度";
    }
    return display_text_or(name, "自定义额度");
}

void format_status(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    const deck_ai_page_view_model_t &ai = model->ai_page;
    uint8_t hour = model->rtc_hour;
    uint8_t minute = model->rtc_minute;
    bool time_available = model->rtc_available && hour <= 23U && minute <= 59U;
    if (ai.active) {
        const char *timezone = ai.pages.has_timezone
                                   ? ai.pages.timezone
                                   : ai.codex.has_timezone ? ai.codex.timezone : nullptr;
        uint8_t local_hour = 0;
        uint8_t local_minute = 0;
        if (timezone != nullptr &&
            deck_ai_page_local_time_from_utc(
                ai.trusted_utc_ms, timezone, &local_hour, &local_minute
            )) {
            hour = local_hour;
            minute = local_minute;
            time_available = true;
        }
    }
    if (time_available) {
        (void)format_text(
            scene->status_time,
            "%02u:%02u",
            static_cast<unsigned>(hour),
            static_cast<unsigned>(minute)
        );
    } else {
        (void)set_text(scene->status_time, "--:--");
    }

    const bool temperature_available = ai.active ? ai.temperature_available : model->sensor_available;
    const int16_t temperature = ai.active
                                    ? ai.calibrated_temperature_tenths_c
                                    : model->calibrated_temperature_tenths_c;
    if (temperature_available) {
        const int32_t signed_value = temperature;
        const uint32_t magnitude = static_cast<uint32_t>(
            signed_value < 0 ? -signed_value : signed_value
        );
        (void)format_text(
            scene->status_temperature,
            "%c%u.%u°C",
            signed_value < 0 ? '-' : '+',
            static_cast<unsigned>(magnitude / 10U),
            static_cast<unsigned>(magnitude % 10U)
        );
    } else {
        (void)set_text(scene->status_temperature, "--.-°C");
    }

    if (ai.active && ai.wifi_state == DECK_AI_PAGE_WIFI_CONNECTED &&
        ai.wifi_signal_bars >= 1U && ai.wifi_signal_bars <= 4U) {
        (void)format_text(
            scene->status_wifi,
            "Wi-Fi %u/4",
            static_cast<unsigned>(ai.wifi_signal_bars)
        );
    } else if ((!ai.active && model->wifi_state == DECK_WIFI_CONNECTED) ||
               (ai.active && ai.wifi_state == DECK_AI_PAGE_WIFI_CONNECTED)) {
        (void)set_text(scene->status_wifi, "Wi-Fi 在线");
    } else {
        (void)set_text(scene->status_wifi, "Wi-Fi 离线");
    }

    switch (ai.companion_state) {
        case DECK_AI_PAGE_COMPANION_ONLINE:
            (void)set_text(scene->status_companion, "Companion 在线");
            break;
        case DECK_AI_PAGE_COMPANION_CONNECTING:
            (void)set_text(scene->status_companion, "Companion 连接中");
            break;
        case DECK_AI_PAGE_COMPANION_OFFLINE:
            (void)set_text(scene->status_companion, "Companion 离线");
            break;
        case DECK_AI_PAGE_COMPANION_UNPAIRED:
        default:
            (void)set_text(scene->status_companion, "尚未配对");
            break;
    }
}

void format_window(
    const deck_ai_snapshot_quota_projection_t *window,
    uint64_t now_utc_ms,
    deck_ui_scene_metric_t *metric
)
{
    (void)set_text(metric->label, window_name(window->name));
    metric->has_progress = window->has_remaining_basis_points || window->has_used_basis_points;
    metric->basis_points = window->has_remaining_basis_points
                               ? window->remaining_basis_points
                               : window->used_basis_points;
    if (metric->basis_points > 10'000U) {
        metric->basis_points = 10'000U;
    }
    if (window->has_remaining_basis_points) {
        (void)format_text(
            metric->value,
            "剩余 %u%%",
            static_cast<unsigned>(metric->basis_points / 100U)
        );
    } else {
        (void)format_text(
            metric->value,
            "已用 %u%%",
            static_cast<unsigned>(metric->basis_points / 100U)
        );
    }
    if (window->has_resets_at && window->resets_at_unix_ms > now_utc_ms) {
        char duration[32]{};
        duration_text(
            (window->resets_at_unix_ms - now_utc_ms) / 1'000ULL,
            duration,
            sizeof(duration)
        );
        (void)format_text(metric->detail, "%s后", duration);
    } else if (window->has_window_minutes) {
        duration_text(
            static_cast<uint64_t>(window->window_minutes) * 60ULL,
            metric->detail,
            sizeof(metric->detail)
        );
    }
}

void format_ai_summary(const deck_ai_snapshot_codex_projection_t *codex, deck_ui_scene_t *scene)
{
    if (!codex->featured_session.present) {
        (void)set_text(scene->summary_title, "暂无 Codex 会话");
        (void)set_text(scene->summary_detail, "等待 Companion 提供新快照");
        return;
    }
    if (codex->featured_session.has_display_name) {
        (void)set_text(
            scene->summary_title,
            display_text_or(codex->featured_session.display_name, "会话名称不可用")
        );
    } else {
        const size_t id_size = strlen(codex->featured_session.session_id);
        const char *suffix = codex->featured_session.session_id + (id_size > 8U ? id_size - 8U : 0U);
        (void)format_text(scene->summary_title, "会话 %s", suffix);
    }
    (void)set_text(scene->summary_value, session_state_text(codex->featured_session.state));
    char metrics[DECK_UI_SCENE_TEXT_CAPACITY]{};
    size_t offset = 0U;
    if (codex->featured_session.has_turn_tokens) {
        char tokens[24]{};
        compact_count(codex->featured_session.turn_tokens, tokens, sizeof(tokens));
        const int written = snprintf(metrics, sizeof(metrics), "%s Token", tokens);
        if (written > 0 && static_cast<size_t>(written) < sizeof(metrics)) {
            offset = static_cast<size_t>(written);
        }
    }
    if (codex->featured_session.has_context_used_basis_points && offset < sizeof(metrics)) {
        const int written = snprintf(
            metrics + offset,
            sizeof(metrics) - offset,
            "%s上下文 %u%%",
            offset == 0U ? "" : " · ",
            static_cast<unsigned>(codex->featured_session.context_used_basis_points / 100U)
        );
        if (written > 0 && static_cast<size_t>(written) < sizeof(metrics) - offset) {
            offset += static_cast<size_t>(written);
        }
    }
    if (codex->session_count > 1U && offset < sizeof(metrics)) {
        (void)snprintf(
            metrics + offset,
            sizeof(metrics) - offset,
            "%s另有 %u 个会话",
            offset == 0U ? "" : " · ",
            static_cast<unsigned>(codex->session_count - 1U)
        );
    }
    (void)set_text(scene->summary_detail, metrics);
}

bool project_pairing(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    const deck_pairing_v2_view_model_t &pairing = model->pairing_v2;
    scene->kind = DECK_UI_SCENE_PAIRING;
    scene->centered = true;
    (void)set_text(scene->title, "配对 Companion");
    (void)set_text(scene->badge, "同一局域网");
    scene->badge_style = DECK_UI_BADGE_OUTLINE;
    (void)set_text(scene->footer_left, "TX 未启用");

    if (pairing.state == DECK_PAIRING_V2_ACTIVE ||
        pairing.state == DECK_PAIRING_V2_AUTHENTICATING ||
        pairing.state == DECK_PAIRING_V2_PROOF_VERIFIED) {
        if (strlen(pairing.code) != 6U) {
            return false;
        }
        scene->hero_is_code = true;
        (void)format_text(
            scene->hero,
            "%.3s %.3s",
            pairing.code,
            pairing.code + 3
        );
        if (pairing.state == DECK_PAIRING_V2_AUTHENTICATING) {
            (void)set_text(scene->message, "正在认证 Companion");
        } else if (pairing.state == DECK_PAIRING_V2_PROOF_VERIFIED) {
            (void)set_text(scene->message, "安全证明已通过");
        } else {
            (void)set_text(scene->message, "请在 Mac 配对页输入验证码");
        }
        (void)format_text(scene->detail, "剩余 %u 秒", pairing.remaining_seconds);
        (void)set_text(scene->footer_right, "BOOT 取消");
        return true;
    }

    scene->hero_is_code = false;
    if (pairing.state == DECK_PAIRING_V2_PAIRED) {
        (void)set_text(scene->hero, "配对成功");
        (void)set_text(scene->message, "安全连接已经建立");
        (void)set_text(scene->detail, "Companion Profile 已安全保存");
        (void)set_text(scene->footer_right, "即将返回主页");
        scene->badge_style = DECK_UI_BADGE_SOLID;
    } else if (pairing.state == DECK_PAIRING_V2_EXPIRED) {
        (void)set_text(scene->hero, "验证码已过期");
        (void)set_text(scene->message, "没有修改任何信任配置");
        (void)set_text(scene->detail, "请按 BOOT 重新开始配对");
        (void)set_text(scene->footer_right, "BOOT 重试");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else {
        (void)set_text(scene->hero, "配对失败");
        (void)set_text(scene->message, "原有 Profile 保持不变");
        (void)set_text(scene->detail, "请检查网络后按 BOOT 重试");
        (void)set_text(scene->footer_right, "BOOT 重试");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    }
    return true;
}

void project_setup(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    scene->kind = DECK_UI_SCENE_SETUP;
    (void)set_text(scene->title, "设置与恢复");
    if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_VALIDATING) {
        (void)set_text(scene->badge, "正在验证");
        scene->badge_style = DECK_UI_BADGE_OUTLINE;
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_AUTH_FAILED) {
        (void)set_text(scene->badge, "认证失败");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_TIMED_OUT) {
        (void)set_text(scene->badge, "验证超时");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_CONNECTION_FAILED) {
        (void)set_text(scene->badge, "连接失败");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_STORAGE_ERROR) {
        (void)set_text(scene->badge, "保存失败");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else {
        (void)set_text(scene->badge, "临时 AP");
        scene->badge_style = DECK_UI_BADGE_SOLID;
    }
    scene->metric_count = 3U;
    (void)set_text(scene->metrics[0].label, "网络");
    (void)set_text(scene->metrics[0].value, model->setup_ssid);
    (void)set_text(scene->metrics[1].label, "密码");
    (void)set_text(scene->metrics[1].value, model->setup_password);
    (void)set_text(scene->metrics[2].label, "访问地址");
    (void)format_text(scene->metrics[2].value, "http://%s", model->setup_address);
    if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_VALIDATING) {
        (void)set_text(scene->summary_title, "正在验证家庭 Wi-Fi");
        (void)set_text(scene->summary_detail, "原配置保持不变，请稍候");
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_AUTH_FAILED) {
        (void)set_text(scene->summary_title, "Wi-Fi 认证失败");
        (void)set_text(scene->summary_detail, "请重新输入密码，原配置保持不变");
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_TIMED_OUT) {
        (void)set_text(scene->summary_title, "Wi-Fi 验证超时");
        (void)set_text(scene->summary_detail, "请检查网络后重试，原配置保持不变");
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_CONNECTION_FAILED) {
        (void)set_text(scene->summary_title, "Wi-Fi 连接失败");
        (void)set_text(scene->summary_detail, "临时 AP 保持开启，可重新提交");
    } else if (model->wifi_config_state == DECK_WIFI_CONFIG_VIEW_STORAGE_ERROR) {
        (void)set_text(scene->summary_title, "Wi-Fi 配置保存失败");
        (void)set_text(scene->summary_detail, "原配置保持不变，请重新提交");
    } else {
        (void)set_text(scene->summary_title, "仅用于 Wi-Fi、校准与恢复");
        (void)set_text(scene->summary_detail, "完成后临时网络会自动关闭");
    }
    (void)set_text(scene->footer_left, "TX 未启用");
    (void)set_text(scene->footer_right, "BOOT 重启设置");
}

void project_serial(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    const deck_serial_view_model_t &serial = model->serial;
    scene->kind = DECK_UI_SCENE_SERIAL;
    (void)set_text(scene->title, "Serial 会话");
    (void)set_text(
        scene->badge,
        serial.state == DECK_SERIAL_VIEW_WEB_TX ? "Web TX" : "USB TX"
    );
    scene->badge_style = DECK_UI_BADGE_SOLID;
    (void)set_text(scene->hero, "115200 · 8N1");
    scene->metric_count = 4U;
    (void)set_text(scene->metrics[0].label, "会话");
    (void)format_text(scene->metrics[0].value, "#%llu", static_cast<unsigned long long>(serial.session_id));
    (void)set_text(scene->metrics[1].label, "所有权代");
    (void)format_text(
        scene->metrics[1].value,
        "%llu",
        static_cast<unsigned long long>(serial.owner_generation)
    );
    (void)set_text(scene->metrics[2].label, "USB 拒绝");
    (void)format_text(
        scene->metrics[2].value,
        "%llu B",
        static_cast<unsigned long long>(serial.usb_tx_rejected)
    );
    (void)set_text(scene->metrics[3].label, "UART 错误");
    (void)format_text(
        scene->metrics[3].value,
        "%u / %llu / %llu",
        serial.uart_install_failures,
        static_cast<unsigned long long>(serial.uart_fifo_overflows),
        static_cast<unsigned long long>(serial.uart_driver_buffer_full)
    );
    (void)set_text(scene->footer_left, serial.state == DECK_SERIAL_VIEW_WEB_TX ? "Web TX" : "USB TX");
    (void)set_text(scene->footer_center, "KEY 统计");
    (void)set_text(scene->footer_right, "BOOT 退出");
}

void project_board(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    scene->kind = DECK_UI_SCENE_BOARD;
    (void)set_text(scene->title, "Deck 状态");
    (void)set_text(
        scene->badge,
        model->data_source == DECK_DATA_VERIFIED
            ? "设备正常"
            : model->data_source == DECK_DATA_SIMULATED ? "模拟数据" : "数据不可用"
    );
    scene->badge_style = model->data_source == DECK_DATA_VERIFIED
                             ? DECK_UI_BADGE_SOLID
                             : DECK_UI_BADGE_ALERT;
    if (model->sensor_available) {
        const int32_t value = model->calibrated_temperature_tenths_c;
        const uint32_t magnitude = static_cast<uint32_t>(value < 0 ? -value : value);
        (void)format_text(
            scene->hero,
            "%c%u.%u°C",
            value < 0 ? '-' : '+',
            static_cast<unsigned>(magnitude / 10U),
            static_cast<unsigned>(magnitude % 10U)
        );
    } else {
        (void)set_text(scene->hero, "--.-°C");
    }
    (void)set_text(scene->message, "校准温度");
    scene->metric_count = 3U;
    (void)set_text(scene->metrics[0].label, "湿度");
    if (model->sensor_available) {
        (void)format_text(
            scene->metrics[0].value,
            "%u.%u%%",
            static_cast<unsigned>(model->humidity_tenths_percent / 10U),
            static_cast<unsigned>(model->humidity_tenths_percent % 10U)
        );
    } else {
        (void)set_text(scene->metrics[0].value, "--.-%");
    }
    (void)set_text(scene->metrics[1].label, "RTC");
    (void)set_text(scene->metrics[1].value, model->rtc_available ? "正常" : "不可用");
    (void)set_text(scene->metrics[2].label, "Wi-Fi");
    (void)set_text(scene->metrics[2].value, model->wifi_state == DECK_WIFI_CONNECTED ? "已连接" : "未连接");
    (void)set_text(scene->summary_title, model->firmware_version);
    (void)format_text(
        scene->summary_detail,
        "运行 %llu时%02llu分 · 最低堆 %u KiB",
        static_cast<unsigned long long>(model->uptime_seconds / 3'600ULL),
        static_cast<unsigned long long>(model->uptime_seconds / 60ULL % 60ULL),
        model->minimum_free_heap_bytes / 1'024U
    );
    (void)set_text(scene->footer_left, "TX 未启用");
    (void)set_text(scene->footer_right, "长按 BOOT 设置");
}

void project_configuration_hint(deck_ui_scene_t *scene)
{
    scene->kind = DECK_UI_SCENE_CONFIGURATION_HINT;
    scene->centered = true;
    (void)set_text(scene->title, "添加 AI Provider");
    (void)set_text(scene->badge, "需要配置");
    scene->badge_style = DECK_UI_BADGE_OUTLINE;
    (void)set_text(scene->hero, "请打开 Companion");
    (void)set_text(scene->message, "在电脑管理页添加并排序 Provider");
    (void)set_text(scene->detail, "完成后 Deck 会自动出现新页面");
    (void)set_text(scene->footer_left, "TX 未启用");
    (void)set_text(scene->footer_right, "KEY 返回 Codex");
}

void project_codex(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    const deck_ai_page_view_model_t &ai = model->ai_page;
    const deck_ai_snapshot_codex_projection_t &codex = ai.codex;
    scene->kind = DECK_UI_SCENE_AI;
    (void)set_text(scene->title, "Codex");
    if (ai.snapshot_state == DECK_AI_PAGE_SNAPSHOT_STALE) {
        (void)set_text(scene->badge, "数据已过期");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (ai.snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE ||
               !codex.provider_present ||
               codex.provider_status == DECK_AI_SNAPSHOT_PROVIDER_UNAVAILABLE) {
        (void)set_text(scene->badge, "不可用");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (codex.provider_status == DECK_AI_SNAPSHOT_PROVIDER_DEGRADED) {
        (void)set_text(scene->badge, "部分异常");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else {
        (void)set_text(scene->badge, confidence_text(codex.provider_confidence));
        scene->badge_style = DECK_UI_BADGE_SOLID;
    }

    if (ai.snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE || !codex.provider_present) {
        scene->centered = true;
        (void)set_text(scene->hero, "暂无 Codex 数据");
        if (ai.companion_state == DECK_AI_PAGE_COMPANION_OFFLINE) {
            (void)set_text(scene->message, "Active Companion 当前离线");
            (void)set_text(scene->detail, "重新连接后将恢复最新快照");
        } else if (ai.companion_state == DECK_AI_PAGE_COMPANION_UNPAIRED) {
            (void)set_text(scene->message, "尚未配对 Companion");
            (void)set_text(scene->detail, "请打开同一局域网配对窗口");
        } else {
            (void)set_text(scene->message, "等待首个有效快照");
        }
    } else {
        for (uint8_t index = 0;
             index < codex.window_count && scene->metric_count < DECK_UI_SCENE_MAX_METRICS;
             ++index) {
            if (!codex.windows[index].has_remaining_basis_points &&
                !codex.windows[index].has_used_basis_points) {
                continue;
            }
            format_window(
                &codex.windows[index],
                ai.trusted_utc_ms,
                &scene->metrics[scene->metric_count]
            );
            ++scene->metric_count;
        }
        format_ai_summary(&codex, scene);
    }
    (void)set_text(scene->footer_left, "TX 未启用");
    (void)set_text(scene->footer_center, "KEY 下一页");
    (void)set_text(scene->footer_right, "长按 进串口");
}

void project_provider(
    const deck_m0_view_model_t *model,
    const deck_ai_snapshot_provider_projection_t *provider,
    deck_ui_scene_t *scene
)
{
    scene->kind = DECK_UI_SCENE_PROVIDER;
    (void)set_text(scene->title, display_text_or(provider->display_name, "自定义 Provider"));
    if (model->ai_page.snapshot_state == DECK_AI_PAGE_SNAPSHOT_STALE) {
        (void)set_text(scene->badge, "数据已过期");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (provider->status == DECK_AI_SNAPSHOT_PROVIDER_DEGRADED) {
        (void)set_text(scene->badge, "部分异常");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (provider->status == DECK_AI_SNAPSHOT_PROVIDER_UNAVAILABLE) {
        (void)set_text(scene->badge, "不可用");
        scene->badge_style = DECK_UI_BADGE_ALERT;
    } else if (provider->experimental) {
        (void)set_text(scene->badge, "实验性");
        scene->badge_style = DECK_UI_BADGE_OUTLINE;
    } else {
        (void)set_text(scene->badge, confidence_text(provider->confidence));
        scene->badge_style = DECK_UI_BADGE_SOLID;
    }

    if (model->ai_page.snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE) {
        scene->centered = true;
        (void)set_text(scene->hero, "当前数据不可用");
        (void)set_text(scene->message, "保留上次有效配置");
    } else {
        if (provider->has_balance && scene->metric_count < DECK_UI_SCENE_MAX_METRICS) {
            deck_ui_scene_metric_t &metric = scene->metrics[scene->metric_count++];
            const uint64_t cents = (provider->balance_amount_micros + 5'000ULL) / 10'000ULL;
            (void)set_text(metric.label, "余额");
            (void)format_text(
                metric.value,
                "%llu.%02llu %s",
                static_cast<unsigned long long>(cents / 100ULL),
                static_cast<unsigned long long>(cents % 100ULL),
                provider->balance_currency
            );
        }
        for (uint8_t index = 0;
             index < provider->window_count && scene->metric_count < DECK_UI_SCENE_MAX_METRICS;
             ++index) {
            if (!provider->windows[index].has_remaining_basis_points &&
                !provider->windows[index].has_used_basis_points) {
                continue;
            }
            format_window(
                &provider->windows[index],
                model->ai_page.trusted_utc_ms,
                &scene->metrics[scene->metric_count]
            );
            ++scene->metric_count;
        }
        if (provider->has_total_tokens && scene->metric_count < DECK_UI_SCENE_MAX_METRICS) {
            deck_ui_scene_metric_t &metric = scene->metrics[scene->metric_count++];
            char tokens[24]{};
            compact_count(provider->total_tokens, tokens, sizeof(tokens));
            (void)set_text(metric.label, "Token");
            (void)set_text(metric.value, tokens);
        }
        if (provider->has_error) {
            (void)set_text(scene->summary_title, "Provider 错误");
            (void)set_text(
                scene->summary_value,
                display_text_or(provider->error_code, "错误代码不可用")
            );
            (void)set_text(scene->summary_detail, "其他健康页面继续运行");
        }
    }
    (void)set_text(scene->footer_left, "TX 未启用");
    (void)set_text(scene->footer_center, "KEY 下一页");
    (void)set_text(scene->footer_right, "在电脑上配置");
}

bool pairing_visible(deck_pairing_v2_state_t state)
{
    return state != DECK_PAIRING_V2_IDLE;
}

bool serial_visible(deck_serial_view_state_t state)
{
    return state == DECK_SERIAL_VIEW_USB_TX || state == DECK_SERIAL_VIEW_WEB_TX;
}

}  // namespace

bool deck_ui_scene_project(const deck_m0_view_model_t *model, deck_ui_scene_t *scene)
{
    if (model == nullptr || scene == nullptr || model->firmware_version == nullptr) {
        return false;
    }
    *scene = deck_ui_scene_t{};
    format_status(model, scene);

    if (pairing_visible(model->pairing_v2.state)) {
        return project_pairing(model, scene);
    }
    if (serial_visible(model->serial.state)) {
        project_serial(model, scene);
        return true;
    }
    if (model->setup_state == DECK_SETUP_ACTIVE) {
        project_setup(model, scene);
        return true;
    }
    if (!model->ai_page.active) {
        project_board(model, scene);
        return true;
    }
    if (model->ai_page.configuration_hint) {
        project_configuration_hint(scene);
        return true;
    }
    if (model->ai_page.pages.provider_count > DECK_AI_SNAPSHOT_MAX_PROVIDERS) {
        return false;
    }
    if (model->ai_page.pages.provider_count != 0U) {
        if (model->ai_page.selected_provider >= model->ai_page.pages.provider_count) {
            return false;
        }
        const deck_ai_snapshot_provider_projection_t *provider =
            &model->ai_page.pages.providers[model->ai_page.selected_provider];
        if (strcmp(provider->provider_id, "codex") != 0) {
            project_provider(model, provider, scene);
            return true;
        }
    }
    project_codex(model, scene);
    return true;
}

bool deck_ui_scene_equal(const deck_ui_scene_t *left, const deck_ui_scene_t *right)
{
    if (left == nullptr || right == nullptr ||
        left->metric_count > DECK_UI_SCENE_MAX_METRICS ||
        right->metric_count > DECK_UI_SCENE_MAX_METRICS || left->kind != right->kind ||
        left->badge_style != right->badge_style || left->metric_count != right->metric_count ||
        left->centered != right->centered || left->hero_is_code != right->hero_is_code) {
        return false;
    }
    const bool same_text = strcmp(left->status_time, right->status_time) == 0 &&
                           strcmp(left->status_temperature, right->status_temperature) == 0 &&
                           strcmp(left->status_wifi, right->status_wifi) == 0 &&
                           strcmp(left->status_companion, right->status_companion) == 0 &&
                           strcmp(left->title, right->title) == 0 &&
                           strcmp(left->badge, right->badge) == 0 &&
                           strcmp(left->hero, right->hero) == 0 &&
                           strcmp(left->message, right->message) == 0 &&
                           strcmp(left->detail, right->detail) == 0 &&
                           strcmp(left->summary_title, right->summary_title) == 0 &&
                           strcmp(left->summary_value, right->summary_value) == 0 &&
                           strcmp(left->summary_detail, right->summary_detail) == 0 &&
                           strcmp(left->footer_left, right->footer_left) == 0 &&
                           strcmp(left->footer_center, right->footer_center) == 0 &&
                           strcmp(left->footer_right, right->footer_right) == 0;
    if (!same_text) {
        return false;
    }
    for (uint8_t index = 0U; index < left->metric_count; ++index) {
        const deck_ui_scene_metric_t &left_metric = left->metrics[index];
        const deck_ui_scene_metric_t &right_metric = right->metrics[index];
        if (strcmp(left_metric.label, right_metric.label) != 0 ||
            strcmp(left_metric.value, right_metric.value) != 0 ||
            strcmp(left_metric.detail, right_metric.detail) != 0 ||
            left_metric.basis_points != right_metric.basis_points ||
            left_metric.has_progress != right_metric.has_progress) {
            return false;
        }
    }
    return true;
}
