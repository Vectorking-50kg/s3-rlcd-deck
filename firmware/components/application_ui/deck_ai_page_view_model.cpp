#include "deck_ai_page_view_model.h"

#include <cctype>
#include <cstdarg>
#include <cstdio>
#include <cstring>

namespace {

constexpr const char *kDivider = "----------------------------------------";

struct CivilDate {
    int year;
    unsigned month;
    unsigned day;
};

int64_t days_from_civil(int year, unsigned month, unsigned day)
{
    year -= month <= 2U;
    const int era = (year >= 0 ? year : year - 399) / 400;
    const unsigned year_of_era = static_cast<unsigned>(year - era * 400);
    const unsigned day_of_year =
        (153U * (month > 2U ? month - 3U : month + 9U) + 2U) / 5U + day - 1U;
    const unsigned day_of_era =
        year_of_era * 365U + year_of_era / 4U - year_of_era / 100U + day_of_year;
    return static_cast<int64_t>(era) * 146'097 + static_cast<int64_t>(day_of_era) - 719'468;
}

CivilDate civil_from_days(int64_t days)
{
    days += 719'468;
    const int64_t era = (days >= 0 ? days : days - 146'096) / 146'097;
    const unsigned day_of_era = static_cast<unsigned>(days - era * 146'097);
    const unsigned year_of_era =
        (day_of_era - day_of_era / 1'460U + day_of_era / 36'524U -
         day_of_era / 146'096U) /
        365U;
    int year = static_cast<int>(year_of_era) + static_cast<int>(era * 400);
    const unsigned day_of_year =
        day_of_era - (365U * year_of_era + year_of_era / 4U - year_of_era / 100U);
    const unsigned month_prime = (5U * day_of_year + 2U) / 153U;
    const unsigned day = day_of_year - (153U * month_prime + 2U) / 5U + 1U;
    const unsigned month = month_prime < 10U ? month_prime + 3U : month_prime - 9U;
    year += month <= 2U;
    return {year, month, day};
}

unsigned weekday_from_days(int64_t days)
{
    const int64_t weekday = (days + 4) % 7;
    return static_cast<unsigned>(weekday < 0 ? weekday + 7 : weekday);
}

unsigned nth_sunday(int year, unsigned month, unsigned occurrence)
{
    const unsigned first_weekday = weekday_from_days(days_from_civil(year, month, 1));
    return 1U + (7U - first_weekday) % 7U + (occurrence - 1U) * 7U;
}

unsigned last_sunday(int year, unsigned month, unsigned last_day)
{
    const unsigned weekday = weekday_from_days(days_from_civil(year, month, last_day));
    return last_day - weekday;
}

int64_t utc_seconds(int year, unsigned month, unsigned day, unsigned hour)
{
    return days_from_civil(year, month, day) * 86'400 + static_cast<int64_t>(hour) * 3'600;
}

bool timezone_offset_minutes(const char *timezone, int64_t utc, int *offset)
{
    if (timezone == nullptr || offset == nullptr) {
        return false;
    }
    struct FixedTimezone {
        const char *name;
        int minutes;
    };
    constexpr FixedTimezone kFixed[] = {
        {"UTC", 0},
        {"Etc/UTC", 0},
        {"GMT", 0},
        {"Asia/Shanghai", 480},
        {"Asia/Hong_Kong", 480},
        {"Asia/Singapore", 480},
        {"Asia/Taipei", 480},
        {"Asia/Tokyo", 540},
        {"Asia/Seoul", 540},
        {"Asia/Kolkata", 330},
        {"Asia/Dubai", 240},
    };
    for (const FixedTimezone &candidate : kFixed) {
        if (std::strcmp(timezone, candidate.name) == 0) {
            *offset = candidate.minutes;
            return true;
        }
    }
    const CivilDate date = civil_from_days(utc / 86'400);
    if (std::strcmp(timezone, "America/Los_Angeles") == 0 ||
        std::strcmp(timezone, "America/New_York") == 0) {
        const bool pacific = timezone[8] == 'L';
        const int standard_offset = pacific ? -480 : -300;
        const int daylight_offset = standard_offset + 60;
        const unsigned start_day = nth_sunday(date.year, 3, 2);
        const unsigned end_day = nth_sunday(date.year, 11, 1);
        const int64_t start = utc_seconds(
            date.year,
            3,
            start_day,
            static_cast<unsigned>(2 - standard_offset / 60)
        );
        const int64_t end = utc_seconds(
            date.year,
            11,
            end_day,
            static_cast<unsigned>(2 - daylight_offset / 60)
        );
        *offset = utc >= start && utc < end ? daylight_offset : standard_offset;
        return true;
    }
    if (std::strcmp(timezone, "Europe/London") == 0 ||
        std::strcmp(timezone, "Europe/Berlin") == 0 ||
        std::strcmp(timezone, "Europe/Paris") == 0) {
        const bool london = timezone[7] == 'L';
        const unsigned start_day = last_sunday(date.year, 3, 31);
        const unsigned end_day = last_sunday(date.year, 10, 31);
        const bool daylight = utc >= utc_seconds(date.year, 3, start_day, 1) &&
                              utc < utc_seconds(date.year, 10, end_day, 1);
        *offset = (london ? 0 : 60) + (daylight ? 60 : 0);
        return true;
    }
    if (std::strcmp(timezone, "Australia/Sydney") == 0) {
        const int64_t start = utc_seconds(date.year, 10, nth_sunday(date.year, 10, 1), 2) -
                              10 * 3'600;
        const int64_t end = utc_seconds(date.year, 4, nth_sunday(date.year, 4, 1), 3) -
                            11 * 3'600;
        *offset = utc < end || utc >= start ? 660 : 600;
        return true;
    }
    return false;
}

struct Writer {
    char *buffer;
    size_t capacity;
    size_t size;
    size_t lines;
};

bool append(Writer *writer, const char *format, ...)
{
    if (writer == nullptr || writer->buffer == nullptr || writer->size >= writer->capacity) {
        return false;
    }
    va_list arguments;
    va_start(arguments, format);
    const int written = std::vsnprintf(
        writer->buffer + writer->size,
        writer->capacity - writer->size,
        format,
        arguments
    );
    va_end(arguments);
    if (written < 0 || static_cast<size_t>(written) >= writer->capacity - writer->size) {
        return false;
    }
    for (int index = 0; index < written; ++index) {
        if (writer->buffer[writer->size + static_cast<size_t>(index)] == '\n') {
            ++writer->lines;
        }
    }
    writer->size += static_cast<size_t>(written);
    return writer->lines <= DECK_AI_PAGE_MAX_LINES;
}

size_t utf8_sequence_size(uint8_t first)
{
    if (first < 0x80U) {
        return 1;
    }
    if ((first & 0xe0U) == 0xc0U) {
        return 2;
    }
    if ((first & 0xf0U) == 0xe0U) {
        return 3;
    }
    if ((first & 0xf8U) == 0xf0U) {
        return 4;
    }
    return 0;
}

bool display_text(
    const char *input,
    char *output,
    size_t output_capacity,
    size_t maximum_cells,
    bool uppercase_ascii
)
{
    if (input == nullptr || output == nullptr || output_capacity == 0) {
        return false;
    }
    size_t read = 0;
    size_t written = 0;
    size_t cells = 0;
    while (input[read] != '\0') {
        const uint8_t first = static_cast<uint8_t>(input[read]);
        const size_t sequence = utf8_sequence_size(first);
        if (sequence == 0) {
            return false;
        }
        for (size_t offset = 1; offset < sequence; ++offset) {
            if ((static_cast<uint8_t>(input[read + offset]) & 0xc0U) != 0x80U) {
                return false;
            }
        }
        const size_t width = sequence == 1 ? 1U : 2U;
        if (cells + width > maximum_cells) {
            break;
        }
        if (written + sequence >= output_capacity) {
            return false;
        }
        if (sequence == 1) {
            char character = input[read];
            if (uppercase_ascii && character >= 'a' && character <= 'z') {
                character = static_cast<char>(character - ('a' - 'A'));
            }
            output[written++] = character;
        } else {
            std::memcpy(output + written, input + read, sequence);
            written += sequence;
        }
        read += sequence;
        cells += width;
    }
    output[written] = '\0';
    return written != 0;
}

const char *confidence_name(deck_ai_snapshot_confidence_t confidence)
{
    switch (confidence) {
        case DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED:
            return "VERIFIED";
        case DECK_AI_SNAPSHOT_CONFIDENCE_INFERRED:
            return "INFERRED";
        case DECK_AI_SNAPSHOT_CONFIDENCE_UNAVAILABLE:
        default:
            return "UNAVAILABLE";
    }
}

const char *provider_status_name(deck_ai_snapshot_provider_status_t status)
{
    switch (status) {
        case DECK_AI_SNAPSHOT_PROVIDER_OK:
            return "OK";
        case DECK_AI_SNAPSHOT_PROVIDER_DEGRADED:
            return "DEGRADED";
        case DECK_AI_SNAPSHOT_PROVIDER_UNAVAILABLE:
        default:
            return "UNAVAILABLE";
    }
}

const char *session_state_name(deck_ai_snapshot_session_state_t state)
{
    switch (state) {
        case DECK_AI_SNAPSHOT_SESSION_RUNNING: return "RUNNING";
        case DECK_AI_SNAPSHOT_SESSION_WAITING_APPROVAL: return "WAITING APPROVAL";
        case DECK_AI_SNAPSHOT_SESSION_WAITING_INPUT: return "WAITING INPUT";
        case DECK_AI_SNAPSHOT_SESSION_COMPLETED: return "COMPLETED";
        case DECK_AI_SNAPSHOT_SESSION_FAILED: return "FAILED";
        case DECK_AI_SNAPSHOT_SESSION_RECENT: return "RECENT";
        case DECK_AI_SNAPSHOT_SESSION_ENDED: return "ENDED";
        case DECK_AI_SNAPSHOT_SESSION_UNKNOWN: return "UNKNOWN";
        case DECK_AI_SNAPSHOT_SESSION_UNAVAILABLE:
        default:
            return "UNAVAILABLE";
    }
}

void compact_count(uint64_t value, char *output, size_t capacity)
{
    if (value >= 1'000'000'000ULL) {
        (void)std::snprintf(output, capacity, "%llu.%lluB",
                           static_cast<unsigned long long>(value / 1'000'000'000ULL),
                           static_cast<unsigned long long>(value / 100'000'000ULL % 10ULL));
    } else if (value >= 1'000'000ULL) {
        (void)std::snprintf(output, capacity, "%llu.%lluM",
                           static_cast<unsigned long long>(value / 1'000'000ULL),
                           static_cast<unsigned long long>(value / 100'000ULL % 10ULL));
    } else if (value >= 100'000ULL) {
        (void)std::snprintf(output, capacity, "%lluK",
                           static_cast<unsigned long long>(value / 1'000ULL));
    } else if (value >= 1'000ULL) {
        (void)std::snprintf(output, capacity, "%llu.%lluK",
                           static_cast<unsigned long long>(value / 1'000ULL),
                           static_cast<unsigned long long>(value / 100ULL % 10ULL));
    } else {
        (void)std::snprintf(output, capacity, "%llu", static_cast<unsigned long long>(value));
    }
}

void duration_text(uint64_t seconds, char *output, size_t capacity)
{
    if (seconds >= 86'400ULL) {
        const uint64_t days = seconds / 86'400ULL;
        if (days > 999ULL) {
            (void)std::snprintf(output, capacity, "999d+");
        } else {
            (void)std::snprintf(output, capacity, "%llud%02lluh",
                               static_cast<unsigned long long>(days),
                               static_cast<unsigned long long>(seconds / 3'600ULL % 24ULL));
        }
    } else if (seconds >= 3'600ULL) {
        (void)std::snprintf(output, capacity, "%lluh%02llum",
                           static_cast<unsigned long long>(seconds / 3'600ULL),
                           static_cast<unsigned long long>(seconds / 60ULL % 60ULL));
    } else if (seconds >= 60ULL) {
        (void)std::snprintf(output, capacity, "%llum",
                           static_cast<unsigned long long>(seconds / 60ULL));
    } else {
        (void)std::snprintf(output, capacity, "%llus",
                           static_cast<unsigned long long>(seconds));
    }
}

bool append_status(Writer *writer, const deck_ai_page_view_model_t *model)
{
    char time[8] = "--:--";
    uint8_t local_hour = 0;
    uint8_t local_minute = 0;
    const char *timezone = model->pages.has_timezone
                               ? model->pages.timezone
                               : model->codex.has_timezone ? model->codex.timezone : nullptr;
    const bool snapshot_time = timezone != nullptr &&
                               deck_ai_page_local_time_from_utc(
                                   model->trusted_utc_ms,
                                   timezone,
                                   &local_hour,
                                   &local_minute
                               );
    if (snapshot_time ||
        (model->rtc_available && model->rtc_hour <= 23U && model->rtc_minute <= 59U)) {
        const unsigned hour = snapshot_time ? local_hour : model->rtc_hour;
        const unsigned minute = snapshot_time ? local_minute : model->rtc_minute;
        (void)std::snprintf(
            time,
            sizeof(time),
            "%02u:%02u",
            hour,
            minute
        );
    }
    char temperature[16] = "--.-C";
    if (model->temperature_available) {
        const int32_t signed_value = model->calibrated_temperature_tenths_c;
        const uint32_t magnitude = static_cast<uint32_t>(signed_value < 0 ? -signed_value : signed_value);
        (void)std::snprintf(
            temperature,
            sizeof(temperature),
            "%c%u.%uC",
            signed_value < 0 ? '-' : '+',
            static_cast<unsigned>(magnitude / 10U),
            static_cast<unsigned>(magnitude % 10U)
        );
    }
    char wifi[8] = "--";
    if (model->wifi_state == DECK_AI_PAGE_WIFI_CONNECTED) {
        if (model->wifi_signal_bars >= 1U && model->wifi_signal_bars <= 4U) {
            (void)std::snprintf(
                wifi,
                sizeof(wifi),
                "%u/4",
                static_cast<unsigned>(model->wifi_signal_bars)
            );
        }
    } else if (model->wifi_state == DECK_AI_PAGE_WIFI_DISCONNECTED) {
        (void)std::snprintf(wifi, sizeof(wifi), "OFF");
    }
    const char *companion = model->companion_state == DECK_AI_PAGE_COMPANION_ONLINE
                                ? "ON"
                                : model->companion_state == DECK_AI_PAGE_COMPANION_CONNECTING
                                      ? "LINK"
                                      : model->companion_state == DECK_AI_PAGE_COMPANION_OFFLINE
                                            ? "OFF"
                                            : "NONE";
    return append(writer, "%s  %s  WIFI %s  AGENT %s\n%s\n", time, temperature, wifi,
                  companion, kDivider);
}

bool append_window(
    Writer *writer,
    const deck_ai_snapshot_quota_projection_t *window,
    uint64_t now_utc_ms
);

bool append_provider_page(
    Writer *writer,
    const deck_ai_page_view_model_t *model,
    const deck_ai_snapshot_provider_projection_t *provider
)
{
    char name[128]{};
    if (!display_text(provider->display_name, name, sizeof(name), 24U, true) ||
        !append(writer, "%s\n", name)) {
        return false;
    }
    const char *status = model->snapshot_state == DECK_AI_PAGE_SNAPSHOT_STALE
                             ? "STALE"
                             : model->snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE
                                   ? "UNAVAILABLE"
                                   : provider_status_name(provider->status);
    if (!append(
            writer,
            "%s / %s%s\n",
            status,
            confidence_name(provider->confidence),
            provider->experimental ? " / EXPERIMENTAL" : ""
        )) {
        return false;
    }
    if (model->snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE) {
        if (model->companion_state == DECK_AI_PAGE_COMPANION_OFFLINE &&
            !append(writer, "AGENT OFFLINE\n")) {
            return false;
        }
        return append(writer, "NO CURRENT AI DATA\n%s\nKEY: NEXT  TX DISARMED", kDivider);
    }
    if (provider->has_updated_at) {
        if (provider->updated_at_unix_ms <= model->trusted_utc_ms) {
            char age[16]{};
            duration_text(
                (model->trusted_utc_ms - provider->updated_at_unix_ms) / 1'000ULL,
                age,
                sizeof(age)
            );
            if (!append(writer, "UPDATED %s AGO\n", age)) {
                return false;
            }
        } else if (!append(writer, "UPDATED --\n")) {
            return false;
        }
    } else if (!append(writer, "UPDATED --\n")) {
        return false;
    }
    const size_t metric_line_limit = DECK_AI_PAGE_MAX_LINES - 2U -
                                     (provider->has_error ? 1U : 0U);
    if (provider->has_balance && writer->lines < metric_line_limit) {
        const uint64_t cents = (provider->balance_amount_micros + 5'000ULL) / 10'000ULL;
        if (!append(
                writer,
                "BAL %llu.%02llu %s\n",
                static_cast<unsigned long long>(cents / 100ULL),
                static_cast<unsigned long long>(cents % 100ULL),
                provider->balance_currency
            )) {
            return false;
        }
    }
    for (uint8_t index = 0;
         index < provider->window_count && writer->lines < metric_line_limit;
         ++index) {
        if (!append_window(writer, &provider->windows[index], model->trusted_utc_ms)) {
            return false;
        }
    }
    if (provider->has_total_tokens && writer->lines < metric_line_limit) {
        char tokens[24]{};
        compact_count(provider->total_tokens, tokens, sizeof(tokens));
        if (!append(writer, "TOKEN %s\n", tokens)) {
            return false;
        }
    }
    if (provider->has_error && writer->lines < DECK_AI_PAGE_MAX_LINES - 2U) {
        char problem[DECK_AI_SNAPSHOT_ERROR_CODE_CAPACITY]{};
        if (!display_text(
                provider->error_code,
                problem,
                sizeof(problem),
                DECK_AI_SNAPSHOT_ERROR_CODE_CAPACITY - 1U,
                true
            ) ||
            !append(writer, "ERROR %s\n", problem)) {
            return false;
        }
    }
    return append(writer, "%s\nKEY: NEXT  TX DISARMED", kDivider);
}

bool append_configuration_hint(Writer *writer)
{
    return append(
        writer,
        "ADD AI PROVIDER\n"
        "OPEN COMPANION WEB\n"
        "WEB ON COMPUTER\n"
        "NO EXTRA PROVIDERS\n"
        "KEY: CODEX\n"
        "TX DISARMED"
    );
}

bool append_window(
    Writer *writer,
    const deck_ai_snapshot_quota_projection_t *window,
    uint64_t now_utc_ms
)
{
    if (!window->has_remaining_basis_points && !window->has_used_basis_points) {
        return true;
    }
    char name[10]{};
    if (!display_text(window->name, name, sizeof(name), 8, true)) {
        return false;
    }
    uint16_t basis_points = 0;
    const char *qualifier = "R";
    if (window->has_remaining_basis_points) {
        basis_points = window->remaining_basis_points;
    } else if (window->has_used_basis_points) {
        basis_points = window->used_basis_points;
        qualifier = "U";
    }
    char bar[11]{};
    if (window->has_remaining_basis_points || window->has_used_basis_points) {
        size_t filled = static_cast<size_t>((basis_points + 500U) / 1'000U);
        if (filled > 10U) {
            filled = 10U;
        }
        for (size_t index = 0; index < 10U; ++index) {
            bar[index] = index < filled ? '#' : '-';
        }
    }
    char timing[24]{};
    if (window->has_resets_at && window->resets_at_unix_ms > now_utc_ms) {
        char duration[16]{};
        duration_text((window->resets_at_unix_ms - now_utc_ms) / 1'000ULL, duration, sizeof(duration));
        (void)std::snprintf(timing, sizeof(timing), "@%s", duration);
    } else if (window->has_window_minutes) {
        if (window->window_minutes % 1'440U == 0U) {
            (void)std::snprintf(
                timing,
                sizeof(timing),
                "%ud",
                static_cast<unsigned>(window->window_minutes / 1'440U)
            );
        } else {
            char duration[16]{};
            duration_text(static_cast<uint64_t>(window->window_minutes) * 60ULL, duration, sizeof(duration));
            (void)std::snprintf(timing, sizeof(timing), "%s", duration);
        }
    }
    return timing[0] == '\0'
               ? append(writer, "%-8s[%s] %s%u%%\n", name, bar, qualifier,
                        static_cast<unsigned>(basis_points / 100U))
               : append(writer, "%-8s[%s] %s%u%% %s\n", name, bar, qualifier,
                        static_cast<unsigned>(basis_points / 100U), timing);
}

bool append_session(Writer *writer, const deck_ai_snapshot_codex_projection_t *codex)
{
    if (!codex->featured_session.present) {
        return append(writer, "NO CODEX SESSION\nTX DISARMED");
    }
    char name[96]{};
    if (codex->featured_session.has_display_name) {
        // The generated font's widest ASCII glyph (W) advances 13.94 px.
        // 27 such glyphs fit the 384 px label without LVGL wrapping.
        if (!display_text(
                codex->featured_session.display_name,
                name,
                sizeof(name),
                DECK_AI_PAGE_MAX_SESSION_NAME_CELLS,
                true
            )) {
            return false;
        }
    } else {
        const size_t id_size = std::strlen(codex->featured_session.session_id);
        const char *suffix = codex->featured_session.session_id + (id_size > 8U ? id_size - 8U : 0U);
        if (std::snprintf(name, sizeof(name), "SESSION %s", suffix) < 0) {
            return false;
        }
    }
    if (!append(writer, "%s\n%s / %s", name,
                session_state_name(codex->featured_session.state),
                confidence_name(codex->featured_session.confidence))) {
        return false;
    }
    if (codex->featured_session.has_duration_seconds) {
        char duration[16]{};
        duration_text(codex->featured_session.duration_seconds, duration, sizeof(duration));
        if (!append(writer, " / %s", duration)) {
            return false;
        }
    }
    if (!append(writer, "\n")) {
        return false;
    }
    bool has_metric = false;
    if (codex->featured_session.has_turn_tokens) {
        char tokens[24]{};
        compact_count(codex->featured_session.turn_tokens, tokens, sizeof(tokens));
        if (!append(writer, "%s TOK", tokens)) {
            return false;
        }
        has_metric = true;
    }
    if (codex->featured_session.has_context_used_basis_points) {
        if (!append(writer, "%sCTX %u%%", has_metric ? " / " : "",
                    static_cast<unsigned>(codex->featured_session.context_used_basis_points / 100U))) {
            return false;
        }
        has_metric = true;
    }
    if (codex->session_count > 1U) {
        if (!append(writer, "%s+%u SESS", has_metric ? " / " : "",
                    static_cast<unsigned>(codex->session_count - 1U))) {
            return false;
        }
        has_metric = true;
    }
    return append(writer, "%sTX DISARMED", has_metric ? "\n" : "");
}

}  // namespace

uint8_t deck_ai_page_wifi_signal_bars(int8_t rssi)
{
    if (rssi >= -55) {
        return 4;
    }
    if (rssi >= -67) {
        return 3;
    }
    if (rssi >= -75) {
        return 2;
    }
    return 1;
}

bool deck_ai_page_view_model_apply_pages(
    deck_ai_page_view_model_t *model,
    const deck_ai_snapshot_pages_projection_t *pages
)
{
    if (model == nullptr || pages == nullptr ||
        pages->provider_count > DECK_AI_SNAPSHOT_MAX_PROVIDERS) {
        return false;
    }
    char selected_id[DECK_AI_SNAPSHOT_PROVIDER_ID_CAPACITY]{};
    if (!model->configuration_hint && model->selected_provider < model->pages.provider_count) {
        std::memcpy(
            selected_id,
            model->pages.providers[model->selected_provider].provider_id,
            sizeof(selected_id)
        );
    }
    model->pages = *pages;
    model->selected_provider = 0U;
    if (pages->provider_count == 0U) {
        model->configuration_hint = true;
        return true;
    }
    if (model->configuration_hint && pages->provider_count == 1U) {
        return true;
    }
    model->configuration_hint = false;
    for (uint8_t index = 0; index < pages->provider_count; ++index) {
        if (selected_id[0] != '\0' &&
            std::strcmp(selected_id, pages->providers[index].provider_id) == 0) {
            model->selected_provider = index;
            return true;
        }
    }
    for (uint8_t index = 0; index < pages->provider_count; ++index) {
        if (std::strcmp(pages->providers[index].provider_id, "codex") == 0) {
            model->selected_provider = index;
            return true;
        }
    }
    return true;
}

void deck_ai_page_view_model_next(deck_ai_page_view_model_t *model)
{
    if (model == nullptr) {
        return;
    }
    if (model->pages.provider_count <= 1U) {
        model->selected_provider = 0U;
        model->configuration_hint = !model->configuration_hint;
        return;
    }
    model->configuration_hint = false;
    model->selected_provider = static_cast<uint8_t>(
        (static_cast<unsigned>(model->selected_provider) + 1U) %
        model->pages.provider_count
    );
}

bool deck_ai_page_local_time_from_utc(
    uint64_t utc_ms,
    const char *timezone,
    uint8_t *hour,
    uint8_t *minute
)
{
    if (hour == nullptr || minute == nullptr || utc_ms / 1'000ULL > INT64_MAX) {
        return false;
    }
    const int64_t utc = static_cast<int64_t>(utc_ms / 1'000ULL);
    int offset_minutes = 0;
    if (!timezone_offset_minutes(timezone, utc, &offset_minutes)) {
        return false;
    }
    const int64_t local = utc + static_cast<int64_t>(offset_minutes) * 60;
    int64_t seconds_of_day = local % 86'400;
    if (seconds_of_day < 0) {
        seconds_of_day += 86'400;
    }
    *hour = static_cast<uint8_t>(seconds_of_day / 3'600);
    *minute = static_cast<uint8_t>(seconds_of_day / 60 % 60);
    return true;
}

bool deck_ai_page_view_model_equal(
    const deck_ai_page_view_model_t *left,
    const deck_ai_page_view_model_t *right
)
{
    if (left == nullptr || right == nullptr) {
        return left == right;
    }
    if (left->active != right->active) {
        return false;
    }
    if (!left->active) {
        return true;
    }
    char left_text[1024]{};
    char right_text[1024]{};
    return deck_ai_page_view_model_format(left, left_text, sizeof(left_text)) &&
           deck_ai_page_view_model_format(right, right_text, sizeof(right_text)) &&
           std::strcmp(left_text, right_text) == 0;
}

bool deck_ai_page_view_model_format(
    const deck_ai_page_view_model_t *model,
    char *buffer,
    size_t buffer_size
)
{
    if (model == nullptr || buffer == nullptr || buffer_size == 0 || !model->active) {
        return false;
    }
    if (model->codex.window_count > DECK_AI_SNAPSHOT_MAX_WINDOWS) {
        return false;
    }
    buffer[0] = '\0';
    Writer writer{buffer, buffer_size, 0, 1};
    if (!append_status(&writer, model)) {
        return false;
    }
    if (model->configuration_hint) {
        return append_configuration_hint(&writer);
    }
    if (model->pages.provider_count > DECK_AI_SNAPSHOT_MAX_PROVIDERS ||
        model->selected_provider >= model->pages.provider_count) {
        if (model->pages.provider_count != 0U) {
            return false;
        }
    } else {
        const deck_ai_snapshot_provider_projection_t *provider =
            &model->pages.providers[model->selected_provider];
        if (std::strcmp(provider->provider_id, "codex") != 0) {
            return append_provider_page(&writer, model, provider);
        }
    }
    const char *confidence = confidence_name(model->codex.provider_confidence);
    if (model->snapshot_state == DECK_AI_PAGE_SNAPSHOT_UNAVAILABLE ||
        !model->codex.provider_present) {
        if (!append(&writer, "CODEX  UNAVAILABLE\n")) {
            return false;
        }
        if (model->companion_state == DECK_AI_PAGE_COMPANION_OFFLINE) {
            if (!append(&writer, "AGENT OFFLINE\n")) {
                return false;
            }
        } else if (model->companion_state == DECK_AI_PAGE_COMPANION_UNPAIRED) {
            if (!append(&writer, "NO ACTIVE COMPANION\n")) {
                return false;
            }
        } else if (!append(&writer, "NO CODEX DATA\n")) {
            return false;
        }
        return append(&writer, "NO CURRENT AI DATA\nTX DISARMED");
    }
    if (model->snapshot_state == DECK_AI_PAGE_SNAPSHOT_STALE) {
        if (!append(&writer, "CODEX  STALE / %s\n", confidence)) {
            return false;
        }
    } else if (model->codex.provider_status == DECK_AI_SNAPSHOT_PROVIDER_DEGRADED) {
        if (!append(&writer, "CODEX  DEGRADED / %s\n", confidence)) {
            return false;
        }
    } else if (model->codex.provider_status == DECK_AI_SNAPSHOT_PROVIDER_UNAVAILABLE) {
        return append(&writer, "CODEX  UNAVAILABLE / %s\nNO CURRENT AI DATA\nTX DISARMED", confidence);
    } else if (!append(&writer, "CODEX  %s\n", confidence)) {
        return false;
    }
    for (uint8_t index = 0; index < model->codex.window_count; ++index) {
        if (!append_window(&writer, &model->codex.windows[index], model->trusted_utc_ms)) {
            return false;
        }
    }
    if (model->codex.has_total_tokens) {
        char tokens[24]{};
        compact_count(model->codex.total_tokens, tokens, sizeof(tokens));
        if (!append(&writer, "TOKEN %s\n", tokens)) {
            return false;
        }
    }
    return append(&writer, "%s\n", kDivider) && append_session(&writer, &model->codex);
}
