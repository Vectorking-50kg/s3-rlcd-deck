#include "deck_ui_renderer.h"

#include <cstring>
#include <new>

LV_FONT_DECLARE(lv_font_deck_m0_16);
LV_FONT_DECLARE(lv_font_deck_ui_20);
LV_FONT_DECLARE(lv_font_deck_ui_32);

namespace {

constexpr int32_t kScreenWidth = 400;
constexpr int32_t kContentWidth = 384;
constexpr int32_t kMargin = 8;
constexpr int32_t kFooterDividerY = 258;

struct MetricWidgets {
    lv_obj_t *row;
    lv_obj_t *label;
    lv_obj_t *bar;
    lv_obj_t *value;
    lv_obj_t *detail;
};

void make_plain(lv_obj_t *object)
{
    lv_obj_remove_flag(object, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_set_style_bg_opa(object, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(object, 0, LV_PART_MAIN);
    lv_obj_set_style_outline_width(object, 0, LV_PART_MAIN);
    lv_obj_set_style_radius(object, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(object, 0, LV_PART_MAIN);
}

lv_obj_t *make_label(
    lv_obj_t *parent,
    int32_t x,
    int32_t y,
    int32_t width,
    int32_t height,
    const lv_font_t *font,
    lv_text_align_t align
)
{
    lv_obj_t *label = lv_label_create(parent);
    if (label == nullptr) {
        return nullptr;
    }
    make_plain(label);
    lv_obj_set_pos(label, x, y);
    lv_obj_set_size(label, width, height);
    lv_label_set_long_mode(label, LV_LABEL_LONG_MODE_CLIP);
    lv_obj_set_style_text_font(label, font, LV_PART_MAIN);
    lv_obj_set_style_text_color(label, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_text_align(label, align, LV_PART_MAIN);
    lv_obj_set_style_text_line_space(label, 0, LV_PART_MAIN);
    return label;
}

lv_obj_t *make_divider(lv_obj_t *parent, int32_t y, int32_t height)
{
    lv_obj_t *divider = lv_obj_create(parent);
    if (divider == nullptr) {
        return nullptr;
    }
    make_plain(divider);
    lv_obj_set_pos(divider, kMargin, y);
    lv_obj_set_size(divider, kContentWidth, height);
    lv_obj_set_style_bg_color(divider, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(divider, LV_OPA_COVER, LV_PART_MAIN);
    return divider;
}

void set_visible(lv_obj_t *object, bool visible)
{
    if (visible) {
        lv_obj_remove_flag(object, LV_OBJ_FLAG_HIDDEN);
    } else {
        lv_obj_add_flag(object, LV_OBJ_FLAG_HIDDEN);
    }
}

bool has_text(const char *text)
{
    return text != nullptr && text[0] != '\0';
}

void set_label(lv_obj_t *label, const char *text)
{
    if (label == nullptr) {
        return;
    }
    char *previous = lv_label_get_text(label);
    if (previous != nullptr) {
        const size_t length = std::strlen(previous);
        volatile char *cursor = previous;
        for (size_t index = 0U; index < length; ++index) {
            cursor[index] = '\0';
        }
    }
    lv_label_set_text(label, text == nullptr ? "" : text);
    set_visible(label, has_text(text));
}

}  // namespace

struct deck_ui_renderer {
    lv_obj_t *screen;
    lv_obj_t *status_time;
    lv_obj_t *status_temperature;
    lv_obj_t *status_wifi;
    lv_obj_t *status_companion;
    lv_obj_t *title;
    lv_obj_t *badge;
    lv_obj_t *badge_text;
    lv_obj_t *hero;
    lv_obj_t *message;
    lv_obj_t *detail;
    MetricWidgets metrics[DECK_UI_SCENE_MAX_METRICS];
    lv_obj_t *summary;
    lv_obj_t *summary_title;
    lv_obj_t *summary_value;
    lv_obj_t *summary_detail;
    lv_obj_t *footer_left;
    lv_obj_t *footer_center;
    lv_obj_t *footer_right;
};

deck_ui_renderer_t *deck_ui_renderer_create(lv_obj_t *screen)
{
    if (screen == nullptr) {
        return nullptr;
    }
    auto *renderer = new (std::nothrow) deck_ui_renderer_t{};
    if (renderer == nullptr) {
        return nullptr;
    }
    renderer->screen = screen;
    make_plain(screen);
    lv_obj_set_style_bg_color(screen, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(screen, LV_OPA_COVER, LV_PART_MAIN);
    lv_obj_set_size(screen, kScreenWidth, 300);

    renderer->status_time = make_label(screen, 8, 8, 58, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
    renderer->status_temperature = make_label(screen, 72, 8, 72, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
    renderer->status_wifi = make_label(screen, 150, 8, 94, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
    renderer->status_companion = make_label(screen, 248, 8, 144, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_RIGHT);
    renderer->title = make_label(screen, 8, 49, 270, 28, &lv_font_deck_ui_20, LV_TEXT_ALIGN_LEFT);
    renderer->badge = lv_obj_create(screen);
    if (renderer->badge != nullptr) {
        make_plain(renderer->badge);
        lv_obj_set_pos(renderer->badge, 286, 47);
        lv_obj_set_size(renderer->badge, 106, 28);
        lv_obj_set_style_border_color(renderer->badge, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_border_width(renderer->badge, 1, LV_PART_MAIN);
        renderer->badge_text = make_label(
            renderer->badge, 3, 3, 100, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_CENTER
        );
    }
    renderer->hero = make_label(screen, 8, 82, 384, 46, &lv_font_deck_ui_32, LV_TEXT_ALIGN_LEFT);
    renderer->message = make_label(screen, 8, 135, 384, 24, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
    renderer->detail = make_label(screen, 8, 166, 384, 24, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);

    for (size_t index = 0; index < DECK_UI_SCENE_MAX_METRICS; ++index) {
        MetricWidgets &metric = renderer->metrics[index];
        metric.row = lv_obj_create(screen);
        if (metric.row == nullptr) {
            deck_ui_renderer_destroy(renderer);
            return nullptr;
        }
        make_plain(metric.row);
        lv_obj_set_size(metric.row, kContentWidth, 30);
        lv_obj_set_style_border_side(metric.row, LV_BORDER_SIDE_BOTTOM, LV_PART_MAIN);
        lv_obj_set_style_border_color(metric.row, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_border_width(metric.row, 1, LV_PART_MAIN);
        metric.label = make_label(metric.row, 0, 3, 78, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
        metric.bar = lv_bar_create(metric.row);
        if (metric.bar != nullptr) {
            lv_obj_remove_flag(metric.bar, LV_OBJ_FLAG_SCROLLABLE);
            lv_obj_set_pos(metric.bar, 82, 8);
            lv_obj_set_size(metric.bar, 136, 13);
            lv_bar_set_range(metric.bar, 0, 10'000);
            lv_obj_set_style_radius(metric.bar, 0, LV_PART_MAIN);
            lv_obj_set_style_bg_color(metric.bar, lv_color_white(), LV_PART_MAIN);
            lv_obj_set_style_bg_opa(metric.bar, LV_OPA_COVER, LV_PART_MAIN);
            lv_obj_set_style_border_color(metric.bar, lv_color_black(), LV_PART_MAIN);
            lv_obj_set_style_border_width(metric.bar, 1, LV_PART_MAIN);
            lv_obj_set_style_pad_all(metric.bar, 2, LV_PART_MAIN);
            lv_obj_set_style_radius(metric.bar, 0, LV_PART_INDICATOR);
            lv_obj_set_style_bg_color(metric.bar, lv_color_black(), LV_PART_INDICATOR);
            lv_obj_set_style_bg_opa(metric.bar, LV_OPA_COVER, LV_PART_INDICATOR);
        }
        metric.value = make_label(metric.row, 224, 3, 88, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
        metric.detail = make_label(metric.row, 314, 3, 70, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_RIGHT);
    }

    renderer->summary = lv_obj_create(screen);
    if (renderer->summary != nullptr) {
        make_plain(renderer->summary);
        lv_obj_set_style_border_color(renderer->summary, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_border_width(renderer->summary, 1, LV_PART_MAIN);
        renderer->summary_title = make_label(
            renderer->summary, 6, 4, 220, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT
        );
        renderer->summary_value = make_label(
            renderer->summary, 232, 4, 144, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_RIGHT
        );
        renderer->summary_detail = make_label(
            renderer->summary, 6, 27, 370, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT
        );
    }

    renderer->footer_left = make_label(screen, 8, 267, 128, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_LEFT);
    renderer->footer_center = make_label(screen, 136, 267, 128, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_CENTER);
    renderer->footer_right = make_label(screen, 264, 267, 128, 22, &lv_font_deck_m0_16, LV_TEXT_ALIGN_RIGHT);

    if (renderer->status_time == nullptr || renderer->status_temperature == nullptr ||
        renderer->status_wifi == nullptr || renderer->status_companion == nullptr ||
        renderer->title == nullptr || renderer->badge == nullptr ||
        renderer->badge_text == nullptr || renderer->hero == nullptr ||
        renderer->message == nullptr || renderer->detail == nullptr ||
        renderer->summary == nullptr || renderer->summary_title == nullptr ||
        renderer->summary_value == nullptr || renderer->summary_detail == nullptr ||
        renderer->footer_left == nullptr || renderer->footer_center == nullptr ||
        renderer->footer_right == nullptr || make_divider(screen, 38, 2) == nullptr ||
        make_divider(screen, kFooterDividerY, 2) == nullptr) {
        deck_ui_renderer_destroy(renderer);
        return nullptr;
    }
    return renderer;
}

void deck_ui_renderer_destroy(deck_ui_renderer_t *renderer)
{
    if (renderer == nullptr) {
        return;
    }
    set_label(renderer->status_time, "");
    set_label(renderer->status_temperature, "");
    set_label(renderer->status_wifi, "");
    set_label(renderer->status_companion, "");
    set_label(renderer->title, "");
    set_label(renderer->badge_text, "");
    set_label(renderer->hero, "");
    set_label(renderer->message, "");
    set_label(renderer->detail, "");
    for (MetricWidgets &metric : renderer->metrics) {
        set_label(metric.label, "");
        set_label(metric.value, "");
        set_label(metric.detail, "");
    }
    set_label(renderer->summary_title, "");
    set_label(renderer->summary_value, "");
    set_label(renderer->summary_detail, "");
    set_label(renderer->footer_left, "");
    set_label(renderer->footer_center, "");
    set_label(renderer->footer_right, "");
    delete renderer;
}

bool deck_ui_renderer_present(deck_ui_renderer_t *renderer, const deck_ui_scene_t *scene)
{
    if (renderer == nullptr || scene == nullptr) {
        return false;
    }
    set_label(renderer->status_time, scene->status_time);
    set_label(renderer->status_temperature, scene->status_temperature);
    set_label(renderer->status_wifi, scene->status_wifi);
    set_label(renderer->status_companion, scene->status_companion);
    set_label(renderer->title, scene->title);
    set_label(renderer->badge_text, scene->badge);
    set_visible(renderer->badge, has_text(scene->badge));
    if (scene->badge_style == DECK_UI_BADGE_SOLID) {
        lv_obj_set_style_bg_color(renderer->badge, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_bg_opa(renderer->badge, LV_OPA_COVER, LV_PART_MAIN);
        lv_obj_set_style_text_color(renderer->badge_text, lv_color_white(), LV_PART_MAIN);
        lv_obj_set_style_border_width(renderer->badge, 1, LV_PART_MAIN);
        lv_obj_set_style_outline_width(renderer->badge, 0, LV_PART_MAIN);
    } else {
        lv_obj_set_style_bg_opa(renderer->badge, LV_OPA_TRANSP, LV_PART_MAIN);
        lv_obj_set_style_text_color(renderer->badge_text, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_border_width(
            renderer->badge,
            scene->badge_style == DECK_UI_BADGE_ALERT ? 2 : 1,
            LV_PART_MAIN
        );
        lv_obj_set_style_outline_width(
            renderer->badge,
            scene->badge_style == DECK_UI_BADGE_ALERT ? 1 : 0,
            LV_PART_MAIN
        );
        lv_obj_set_style_outline_color(renderer->badge, lv_color_black(), LV_PART_MAIN);
        lv_obj_set_style_outline_pad(renderer->badge, 1, LV_PART_MAIN);
    }

    set_label(renderer->hero, scene->hero);
    set_label(renderer->message, scene->message);
    set_label(renderer->detail, scene->detail);
    lv_obj_set_style_text_font(
        renderer->hero,
        scene->hero_is_code ? &lv_font_deck_ui_32 : &lv_font_deck_ui_20,
        LV_PART_MAIN
    );

    int32_t metric_y = 82;
    if (scene->centered) {
        lv_obj_set_pos(renderer->hero, 8, 91);
        lv_obj_set_size(renderer->hero, 384, 44);
        lv_obj_set_style_text_align(renderer->hero, LV_TEXT_ALIGN_CENTER, LV_PART_MAIN);
        lv_obj_set_pos(renderer->message, 8, 150);
        lv_obj_set_style_text_align(renderer->message, LV_TEXT_ALIGN_CENTER, LV_PART_MAIN);
        lv_obj_set_pos(renderer->detail, 8, 181);
        lv_obj_set_style_text_align(renderer->detail, LV_TEXT_ALIGN_CENTER, LV_PART_MAIN);
        metric_y = 214;
    } else if (has_text(scene->hero)) {
        lv_obj_set_pos(renderer->hero, 8, 81);
        lv_obj_set_size(renderer->hero, 208, 42);
        lv_obj_set_style_text_align(renderer->hero, LV_TEXT_ALIGN_LEFT, LV_PART_MAIN);
        lv_obj_set_pos(renderer->message, 222, 91);
        lv_obj_set_size(renderer->message, 170, 22);
        lv_obj_set_style_text_align(renderer->message, LV_TEXT_ALIGN_RIGHT, LV_PART_MAIN);
        lv_obj_set_pos(renderer->detail, 222, 113);
        lv_obj_set_size(renderer->detail, 170, 22);
        lv_obj_set_style_text_align(renderer->detail, LV_TEXT_ALIGN_RIGHT, LV_PART_MAIN);
        metric_y = 130;
    } else {
        lv_obj_set_pos(renderer->hero, 8, 82);
        lv_obj_set_style_text_align(renderer->hero, LV_TEXT_ALIGN_LEFT, LV_PART_MAIN);
        lv_obj_set_pos(renderer->message, 8, 135);
        lv_obj_set_size(renderer->message, 384, 24);
        lv_obj_set_style_text_align(renderer->message, LV_TEXT_ALIGN_LEFT, LV_PART_MAIN);
        lv_obj_set_pos(renderer->detail, 8, 166);
        lv_obj_set_size(renderer->detail, 384, 24);
        lv_obj_set_style_text_align(renderer->detail, LV_TEXT_ALIGN_LEFT, LV_PART_MAIN);
    }

    for (size_t index = 0; index < DECK_UI_SCENE_MAX_METRICS; ++index) {
        MetricWidgets &widgets = renderer->metrics[index];
        const bool visible = index < scene->metric_count;
        set_visible(widgets.row, visible);
        if (!visible) {
            continue;
        }
        const deck_ui_scene_metric_t &metric = scene->metrics[index];
        lv_obj_set_pos(widgets.row, 8, metric_y + static_cast<int32_t>(index) * 30);
        set_label(widgets.label, metric.label);
        set_label(widgets.value, metric.value);
        set_label(widgets.detail, metric.detail);
        set_visible(widgets.bar, metric.has_progress);
        if (metric.has_progress) {
            lv_obj_set_pos(widgets.value, 224, 3);
            lv_obj_set_size(widgets.value, 88, 22);
            lv_obj_set_pos(widgets.detail, 314, 3);
            lv_obj_set_size(widgets.detail, 70, 22);
            lv_bar_set_value(widgets.bar, metric.basis_points, LV_ANIM_OFF);
        } else {
            lv_obj_set_pos(widgets.value, 82, 3);
            lv_obj_set_size(widgets.value, 220, 22);
            lv_obj_set_pos(widgets.detail, 304, 3);
            lv_obj_set_size(widgets.detail, 80, 22);
        }
    }

    const bool summary_visible = has_text(scene->summary_title) ||
                                 has_text(scene->summary_value) ||
                                 has_text(scene->summary_detail);
    set_visible(renderer->summary, summary_visible);
    if (summary_visible) {
        const int32_t summary_y = metric_y + static_cast<int32_t>(scene->metric_count) * 30 + 4;
        const int32_t available_height = kFooterDividerY - summary_y - 6;
        const int32_t summary_height = available_height > 50 ? 50 : available_height;
        if (summary_height < 24) {
            set_visible(renderer->summary, false);
        } else {
            lv_obj_set_pos(renderer->summary, 8, summary_y);
            lv_obj_set_size(renderer->summary, 384, summary_height);
            set_label(renderer->summary_title, scene->summary_title);
            set_label(renderer->summary_value, scene->summary_value);
            set_label(renderer->summary_detail, scene->summary_detail);
            set_visible(renderer->summary_detail, summary_height >= 48 && has_text(scene->summary_detail));
        }
    }

    set_label(renderer->footer_left, scene->footer_left);
    set_label(renderer->footer_center, scene->footer_center);
    set_label(renderer->footer_right, scene->footer_right);
    return true;
}
