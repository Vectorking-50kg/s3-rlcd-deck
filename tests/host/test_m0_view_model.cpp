#include "deck_m0_view_model.h"

#include <cassert>
#include <cstdint>
#include <cstring>
#include <string>

namespace {

deck_m0_view_model_t sample_model()
{
    return {
        "0.2.0-dev",
        DECK_DATA_SIMULATED,
        true,
        12,
        34,
        4,
        true,
        234,
        194,
        456,
        2,
        true,
        DECK_BUTTON_SHORT_PRESS,
        3,
        DECK_BUTTON_LONG_PRESS,
        1,
        DECK_WIFI_UNAVAILABLE,
        DECK_WIFI_CONFIG_VIEW_VALIDATING,
        DECK_WIFI_RECORD_VIEW_VALID,
        DECK_WIFI_RECORD_VIEW_CORRUPT,
        7,
        DECK_SETUP_IDLE,
        {},
        {},
        {},
        42,
        754,
        7'192'576,
    };
}

uint32_t next_codepoint(const char *&text)
{
    const auto first = static_cast<uint8_t>(*text++);
    if (first < 0x80U) {
        return first;
    }
    if ((first & 0xe0U) == 0xc0U) {
        const auto second = static_cast<uint8_t>(*text++);
        return (static_cast<uint32_t>(first & 0x1fU) << 6U) | (second & 0x3fU);
    }
    const auto second = static_cast<uint8_t>(*text++);
    const auto third = static_cast<uint8_t>(*text++);
    return (static_cast<uint32_t>(first & 0x0fU) << 12U) |
           (static_cast<uint32_t>(second & 0x3fU) << 6U) | (third & 0x3fU);
}

bool contains_codepoint(const char *text, uint32_t expected)
{
    while (*text != '\0') {
        if (next_codepoint(text) == expected) {
            return true;
        }
    }
    return false;
}

void formats_every_required_diagnostic_field()
{
    const deck_m0_view_model_t model = sample_model();
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));

    const std::string page(text);
    assert(page.find("S3 RLCD Deck / M0 诊断 [SIM]") != std::string::npos);
    assert(page.find("FW 0.2.0-dev") != std::string::npos);
    assert(page.find("UP 00:12:34") != std::string::npos);
    assert(page.find("RTC 12:34 / 状态 SIMULATED / RTC ERR 4") != std::string::npos);
    assert(page.find("温度 RAW +23.4C / CAL +19.4C") != std::string::npos);
    assert(page.find("湿度 45.6% / SENSOR ERR 2") != std::string::npos);
    assert(page.find("KEY 短按 #3 / BOOT 长按 #1") != std::string::npos);
    assert(page.find("Wi-Fi UNAVAILABLE / CFG VALIDATING #7 / REC VALID/CORRUPT / Setup IDLE") !=
           std::string::npos);
    assert(page.find("刷新 42 / 最低堆 7024 KiB") != std::string::npos);
    assert(page.find("Companion 配对 M1") != std::string::npos);
}

void equality_tracks_visible_state_only()
{
    const deck_m0_view_model_t first = sample_model();
    deck_m0_view_model_t second = first;
    assert(deck_m0_view_model_equal(&first, &second));

    ++second.refresh_count;
    assert(!deck_m0_view_model_equal(&first, &second));

    second = first;
    second.data_source = DECK_DATA_VERIFIED;
    assert(!deck_m0_view_model_equal(&first, &second));
}

void every_chinese_page_character_is_in_the_font_manifest()
{
    const deck_m0_view_model_t model = sample_model();
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));
    const char *cursor = text;
    while (*cursor != '\0') {
        const uint32_t codepoint = next_codepoint(cursor);
        if (codepoint >= 0x4e00U && codepoint <= 0x9fffU) {
            assert(contains_codepoint(deck_m0_required_glyphs(), codepoint));
        }
    }
}

void renders_unavailable_rtc_without_fabricating_time()
{
    deck_m0_view_model_t model = sample_model();
    model.rtc_available = false;
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));
    assert(
        std::string(text).find("RTC --:-- / 状态 UNAVAILABLE / RTC ERR 4") !=
        std::string::npos
    );
}

void renders_unavailable_sensor_without_hiding_valid_rtc()
{
    deck_m0_view_model_t model = sample_model();
    model.sensor_available = false;
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));

    const std::string page(text);
    assert(page.find("RTC 12:34 / 状态 SIMULATED / RTC ERR 4") != std::string::npos);
    assert(page.find("温度 RAW --.-C / CAL --.-C") != std::string::npos);
    assert(page.find("湿度 --.-% / SENSOR ERR 2") != std::string::npos);
}

void treats_unknown_data_sources_as_unavailable()
{
    deck_m0_view_model_t model = sample_model();
    model.data_source = 99;
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));

    const std::string page(text);
    assert(page.find("M0 诊断 [UNAVAILABLE]") != std::string::npos);
    assert(page.find("RTC --:-- / 状态 UNAVAILABLE / RTC ERR 4") != std::string::npos);
    assert(page.find("温度 RAW --.-C / CAL --.-C") != std::string::npos);
    assert(page.find("湿度 --.-% / SENSOR ERR 2") != std::string::npos);
}

void renders_unavailable_button_inputs_explicitly()
{
    deck_m0_view_model_t model = sample_model();
    model.buttons_available = false;
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));

    assert(
        std::string(text).find("KEY UNAVAILABLE #3 / BOOT UNAVAILABLE #1") !=
        std::string::npos
    );
}

void renders_ephemeral_setup_credentials_on_the_deck()
{
    deck_m0_view_model_t model = sample_model();
    model.setup_state = DECK_SETUP_ACTIVE;
    std::strcpy(model.setup_ssid, "S3-RLCD-A1B2");
    std::strcpy(model.setup_password, "ABCD-EFGH-JKLM");
    std::strcpy(model.setup_address, "192.168.4.1");
    char text[768];
    assert(deck_m0_view_model_format(&model, text, sizeof(text)));

    const std::string page(text);
    assert(page.find("Wi-Fi UNAVAILABLE / CFG VALIDATING #7 / REC VALID/CORRUPT / Setup ACTIVE") !=
           std::string::npos);
    assert(page.find("AP S3-RLCD-A1B2") != std::string::npos);
    assert(page.find("PASS ABCD-EFGH-JKLM") != std::string::npos);
    assert(page.find("HTTP http://192.168.4.1") != std::string::npos);
}

}  // namespace

int main()
{
    formats_every_required_diagnostic_field();
    equality_tracks_visible_state_only();
    every_chinese_page_character_is_in_the_font_manifest();
    renders_unavailable_rtc_without_fabricating_time();
    renders_unavailable_sensor_without_hiding_valid_rtc();
    treats_unknown_data_sources_as_unavailable();
    renders_unavailable_button_inputs_explicitly();
    renders_ephemeral_setup_credentials_on_the_deck();
    return 0;
}
