#include "deck_ui_preview.h"

#include <cstring>

namespace {

template <size_t Capacity>
void set_text(char (&output)[Capacity], const char *value)
{
    const size_t size = std::strlen(value);
    if (size >= Capacity) {
        output[0] = '\0';
        return;
    }
    std::memcpy(output, value, size + 1U);
}

void common_status(deck_ui_scene_t *scene)
{
    set_text(scene->status_time, "20:36");
    set_text(scene->status_temperature, "+24.8°C");
    set_text(scene->status_wifi, "Wi-Fi 4/4");
    set_text(scene->status_companion, "Companion 在线");
    set_text(scene->footer_left, "TX 未启用");
}

void progress_metric(
    deck_ui_scene_metric_t *metric,
    const char *label,
    const char *value,
    const char *detail,
    uint16_t basis_points
)
{
    set_text(metric->label, label);
    set_text(metric->value, value);
    set_text(metric->detail, detail);
    metric->has_progress = true;
    metric->basis_points = basis_points;
}

}  // namespace

bool deck_ui_preview_page_parse(const char *name, deck_ui_preview_page_t *page)
{
    if (name == nullptr || page == nullptr) {
        return false;
    }
    struct PageName {
        const char *name;
        deck_ui_preview_page_t page;
    };
    constexpr PageName names[] = {
        {"board", DECK_UI_PREVIEW_BOARD},
        {"pairing", DECK_UI_PREVIEW_PAIRING},
        {"setup", DECK_UI_PREVIEW_SETUP},
        {"ai", DECK_UI_PREVIEW_AI},
        {"provider", DECK_UI_PREVIEW_PROVIDER},
        {"configuration", DECK_UI_PREVIEW_CONFIGURATION},
        {"serial", DECK_UI_PREVIEW_SERIAL},
        {"offline", DECK_UI_PREVIEW_OFFLINE},
        {"error", DECK_UI_PREVIEW_ERROR},
    };
    for (const PageName &candidate : names) {
        if (std::strcmp(name, candidate.name) == 0) {
            *page = candidate.page;
            return true;
        }
    }
    return false;
}

bool deck_ui_preview_scene(deck_ui_preview_page_t page, deck_ui_scene_t *scene)
{
    if (scene == nullptr) {
        return false;
    }
    *scene = deck_ui_scene_t{};
    common_status(scene);

    switch (page) {
        case DECK_UI_PREVIEW_BOARD:
            scene->kind = DECK_UI_SCENE_BOARD;
            set_text(scene->title, "Deck 状态");
            set_text(scene->badge, "设备正常");
            scene->badge_style = DECK_UI_BADGE_SOLID;
            set_text(scene->hero, "+24.8°C");
            set_text(scene->message, "校准温度");
            scene->metric_count = 3U;
            set_text(scene->metrics[0].label, "湿度");
            set_text(scene->metrics[0].value, "53.0%");
            set_text(scene->metrics[1].label, "RTC");
            set_text(scene->metrics[1].value, "正常");
            set_text(scene->metrics[2].label, "Wi-Fi");
            set_text(scene->metrics[2].value, "已连接");
            set_text(scene->summary_title, "0.3.0-dev");
            set_text(scene->summary_detail, "运行 12时08分 · 最低堆 8192 KiB");
            set_text(scene->footer_right, "长按 BOOT 设置");
            return true;
        case DECK_UI_PREVIEW_PAIRING:
            scene->kind = DECK_UI_SCENE_PAIRING;
            scene->centered = true;
            scene->hero_is_code = true;
            set_text(scene->title, "配对 Companion");
            set_text(scene->badge, "同一局域网");
            set_text(scene->hero, "123 456");
            set_text(scene->message, "请在 Mac 配对页输入验证码");
            set_text(scene->detail, "剩余 87 秒");
            set_text(scene->footer_right, "BOOT 取消");
            return true;
        case DECK_UI_PREVIEW_SETUP:
            scene->kind = DECK_UI_SCENE_SETUP;
            set_text(scene->title, "设置与恢复");
            set_text(scene->badge, "临时 AP");
            scene->badge_style = DECK_UI_BADGE_SOLID;
            scene->metric_count = 3U;
            set_text(scene->metrics[0].label, "网络");
            set_text(scene->metrics[0].value, "S3-DECK-A17F");
            set_text(scene->metrics[1].label, "密码");
            set_text(scene->metrics[1].value, "MINT-WAVE-7294");
            set_text(scene->metrics[2].label, "访问地址");
            set_text(scene->metrics[2].value, "http://192.168.4.1");
            set_text(scene->summary_title, "仅用于 Wi-Fi、校准与恢复");
            set_text(scene->summary_detail, "完成后临时网络会自动关闭");
            set_text(scene->footer_right, "BOOT 重启设置");
            return true;
        case DECK_UI_PREVIEW_AI:
            scene->kind = DECK_UI_SCENE_AI;
            set_text(scene->title, "Codex");
            set_text(scene->badge, "已验证");
            scene->badge_style = DECK_UI_BADGE_SOLID;
            scene->metric_count = 2U;
            progress_metric(&scene->metrics[0], "主要额度", "剩余 62%", "2时30分后", 6'200U);
            progress_metric(&scene->metrics[1], "每周额度", "已用 22%", "7天00时", 2'200U);
            set_text(scene->summary_title, "Deck 中文界面开发");
            set_text(scene->summary_value, "运行中");
            set_text(scene->summary_detail, "18.4K Token · 上下文 41% · 另有 2 个会话");
            set_text(scene->footer_center, "KEY 下一页");
            set_text(scene->footer_right, "长按 进串口");
            return true;
        case DECK_UI_PREVIEW_PROVIDER:
            scene->kind = DECK_UI_SCENE_PROVIDER;
            set_text(scene->title, "自定义 Provider");
            set_text(scene->badge, "实验性");
            scene->badge_style = DECK_UI_BADGE_OUTLINE;
            scene->metric_count = 3U;
            set_text(scene->metrics[0].label, "余额");
            set_text(scene->metrics[0].value, "28.50 CNY");
            progress_metric(&scene->metrics[1], "每月额度", "剩余 73%", "18天04时", 7'300U);
            set_text(scene->metrics[2].label, "Token");
            set_text(scene->metrics[2].value, "1.2M");
            set_text(scene->footer_center, "KEY 下一页");
            set_text(scene->footer_right, "在电脑上配置");
            return true;
        case DECK_UI_PREVIEW_CONFIGURATION:
            scene->kind = DECK_UI_SCENE_CONFIGURATION_HINT;
            scene->centered = true;
            set_text(scene->title, "添加 AI Provider");
            set_text(scene->badge, "需要配置");
            set_text(scene->hero, "请打开 Companion");
            set_text(scene->message, "在电脑管理页添加并排序 Provider");
            set_text(scene->detail, "完成后 Deck 会自动出现新页面");
            set_text(scene->footer_right, "KEY 返回 Codex");
            return true;
        case DECK_UI_PREVIEW_SERIAL:
            scene->kind = DECK_UI_SCENE_SERIAL;
            set_text(scene->title, "Serial 会话");
            set_text(scene->badge, "Web TX");
            scene->badge_style = DECK_UI_BADGE_SOLID;
            set_text(scene->hero, "115200 · 8N1");
            scene->metric_count = 4U;
            set_text(scene->metrics[0].label, "会话");
            set_text(scene->metrics[0].value, "#7");
            set_text(scene->metrics[1].label, "所有权代");
            set_text(scene->metrics[1].value, "11");
            set_text(scene->metrics[2].label, "USB 拒绝");
            set_text(scene->metrics[2].value, "23 B");
            set_text(scene->metrics[3].label, "UART 错误");
            set_text(scene->metrics[3].value, "0 / 0 / 0");
            set_text(scene->footer_left, "Web TX");
            set_text(scene->footer_center, "KEY 统计");
            set_text(scene->footer_right, "BOOT 退出");
            return true;
        case DECK_UI_PREVIEW_OFFLINE:
            scene->kind = DECK_UI_SCENE_AI;
            scene->centered = true;
            set_text(scene->status_companion, "Companion 离线");
            set_text(scene->title, "Codex");
            set_text(scene->badge, "不可用");
            scene->badge_style = DECK_UI_BADGE_ALERT;
            set_text(scene->hero, "暂无 Codex 数据");
            set_text(scene->message, "Active Companion 当前离线");
            set_text(scene->detail, "重新连接后将恢复最新快照");
            set_text(scene->footer_center, "KEY 下一页");
            set_text(scene->footer_right, "长按 进串口");
            return true;
        case DECK_UI_PREVIEW_ERROR:
            scene->kind = DECK_UI_SCENE_PROVIDER;
            set_text(scene->title, "自定义 Provider");
            set_text(scene->badge, "部分异常");
            scene->badge_style = DECK_UI_BADGE_ALERT;
            scene->metric_count = 1U;
            progress_metric(&scene->metrics[0], "每月额度", "剩余 73%", "18天04时", 7'300U);
            set_text(scene->summary_title, "Provider 错误");
            set_text(scene->summary_value, "连接超时");
            set_text(scene->summary_detail, "其他健康页面继续运行");
            set_text(scene->footer_center, "KEY 下一页");
            set_text(scene->footer_right, "在电脑上配置");
            return true;
        default:
            return false;
    }
}
