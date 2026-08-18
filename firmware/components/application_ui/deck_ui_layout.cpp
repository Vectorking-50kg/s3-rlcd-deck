#include "deck_ui_layout.h"

namespace {

constexpr int16_t kDisplayWidth = 400;
constexpr int16_t kDisplayHeight = 300;
constexpr int16_t kContentX = 8;
constexpr int16_t kContentWidth = 384;
constexpr int16_t kFooterDividerY = 258;

bool has_text(const char *text)
{
    return text != nullptr && text[0] != '\0';
}

bool rect_fits_content(deck_ui_rect_t rectangle)
{
    return deck_ui_rect_within_display(rectangle) &&
           static_cast<int32_t>(rectangle.y) + rectangle.height <= kFooterDividerY;
}

}  // namespace

bool deck_ui_layout_plan(const deck_ui_scene_t *scene, deck_ui_layout_t *layout)
{
    if (scene == nullptr || layout == nullptr ||
        scene->metric_count > DECK_UI_SCENE_MAX_METRICS) {
        return false;
    }
    *layout = deck_ui_layout_t{};

    int16_t metric_y = 82;
    if (scene->centered) {
        layout->hero = {kContentX, 91, kContentWidth, 44};
        layout->message = {kContentX, 150, kContentWidth, 24};
        layout->detail = {kContentX, 181, kContentWidth, 24};
        metric_y = 214;
    } else if (has_text(scene->hero)) {
        layout->hero = {kContentX, 81, 208, 42};
        layout->message = {222, 91, 170, 22};
        layout->detail = {222, 113, 170, 22};
        metric_y = 130;
    } else {
        layout->hero = {kContentX, 82, kContentWidth, 46};
        layout->message = {kContentX, 135, kContentWidth, 24};
        layout->detail = {kContentX, 166, kContentWidth, 24};
    }

    for (uint8_t index = 0U; index < DECK_UI_SCENE_MAX_METRICS; ++index) {
        layout->metric_visible[index] = index < scene->metric_count;
        layout->metric_rows[index] = {
            kContentX,
            static_cast<int16_t>(metric_y + static_cast<int16_t>(index) * 30),
            kContentWidth,
            30,
        };
        if (layout->metric_visible[index] && !rect_fits_content(layout->metric_rows[index])) {
            return false;
        }
    }

    layout->summary_visible = has_text(scene->summary_title) ||
                              has_text(scene->summary_value) ||
                              has_text(scene->summary_detail);
    if (layout->summary_visible) {
        const int16_t summary_y = static_cast<int16_t>(
            metric_y + static_cast<int16_t>(scene->metric_count) * 30 + 4
        );
        const int16_t available_height = static_cast<int16_t>(
            kFooterDividerY - summary_y - 6
        );
        const int16_t summary_height = available_height > 50 ? 50 : available_height;
        if (summary_height < 24) {
            layout->summary_visible = false;
        } else {
            layout->summary = {kContentX, summary_y, kContentWidth, summary_height};
            layout->summary_detail_visible = summary_height >= 48 &&
                                             has_text(scene->summary_detail);
            if (!rect_fits_content(layout->summary)) {
                return false;
            }
        }
    }
    return deck_ui_rect_within_display(layout->hero) &&
           deck_ui_rect_within_display(layout->message) &&
           deck_ui_rect_within_display(layout->detail);
}

bool deck_ui_rect_within_display(deck_ui_rect_t rectangle)
{
    if (rectangle.x < 0 || rectangle.y < 0 || rectangle.width < 0 || rectangle.height < 0) {
        return false;
    }
    const int32_t right = static_cast<int32_t>(rectangle.x) + rectangle.width;
    const int32_t bottom = static_cast<int32_t>(rectangle.y) + rectangle.height;
    return right <= kDisplayWidth && bottom <= kDisplayHeight;
}
