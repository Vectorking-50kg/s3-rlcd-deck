#include "deck_tls_clock.h"

#include "deck_tls_clock_policy.h"

#include <climits>
#include <cstdint>
#include <ctime>

#include <sys/time.h>

#include "mbedtls/x509_crt.h"

#ifndef DECK_FIRMWARE_BUILD_UNIX
#define DECK_FIRMWARE_BUILD_UNIX 1704067200
#endif

namespace {

constexpr int64_t kFirmwareBuildUnix = DECK_FIRMWARE_BUILD_UNIX;

bool leap_year(int year)
{
    return year % 4 == 0 && (year % 100 != 0 || year % 400 == 0);
}

int days_in_month(int year, int month)
{
    static constexpr int days[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    if (month < 1 || month > 12) {
        return 0;
    }
    return month == 2 && leap_year(year) ? 29 : days[month - 1];
}

int64_t days_from_civil(int year, unsigned month, unsigned day)
{
    year -= month <= 2U ? 1 : 0;
    const int era = (year >= 0 ? year : year - 399) / 400;
    const unsigned year_of_era = static_cast<unsigned>(year - era * 400);
    const unsigned adjusted_month = month > 2U ? month - 3U : month + 9U;
    const unsigned day_of_year =
        (153U * adjusted_month + 2U) / 5U + day - 1U;
    const unsigned day_of_era =
        year_of_era * 365U + year_of_era / 4U - year_of_era / 100U + day_of_year;
    return static_cast<int64_t>(era) * 146097 + static_cast<int64_t>(day_of_era) - 719468;
}

bool x509_time_to_unix(const mbedtls_x509_time &value, int64_t *unix_seconds)
{
    if (unix_seconds == nullptr || value.year < 1970 || value.year > 9999 ||
        value.mon < 1 || value.mon > 12 || value.day < 1 ||
        value.day > days_in_month(value.year, value.mon) || value.hour < 0 ||
        value.hour > 23 || value.min < 0 || value.min > 59 || value.sec < 0 ||
        value.sec > 60) {
        return false;
    }
    const int64_t days = days_from_civil(
        value.year,
        static_cast<unsigned>(value.mon),
        static_cast<unsigned>(value.day)
    );
    *unix_seconds = days * 86'400 + static_cast<int64_t>(value.hour) * 3'600 +
                    static_cast<int64_t>(value.min) * 60 + value.sec;
    return *unix_seconds > 0;
}

bool set_wall_clock(uint64_t unix_ms)
{
    if (unix_ms > static_cast<uint64_t>(INT64_MAX)) {
        return false;
    }
    timeval value{};
    value.tv_sec = static_cast<time_t>(unix_ms / 1'000U);
    value.tv_usec = static_cast<suseconds_t>((unix_ms % 1'000U) * 1'000U);
    return settimeofday(&value, nullptr) == 0;
}

}  // namespace

bool deck_tls_clock_prepare_pinned_certificate(
    const uint8_t *certificate_der,
    size_t certificate_der_size
)
{
    if (certificate_der == nullptr || certificate_der_size == 0) {
        return false;
    }
    mbedtls_x509_crt certificate;
    mbedtls_x509_crt_init(&certificate);
    const int parsed = mbedtls_x509_crt_parse_der(
        &certificate,
        certificate_der,
        certificate_der_size
    );
    int64_t not_before = 0;
    int64_t not_after = 0;
    const bool valid = parsed == 0 && certificate.next == nullptr &&
                       x509_time_to_unix(certificate.valid_from, &not_before) &&
                       x509_time_to_unix(certificate.valid_to, &not_after);
    mbedtls_x509_crt_free(&certificate);
    if (!valid) {
        return false;
    }

    const time_t now = time(nullptr);
    int64_t seed = 0;
    const DeckTlsClockDecision decision = deck_tls_clock_plan(
        static_cast<int64_t>(now),
        kFirmwareBuildUnix,
        not_before,
        not_after,
        &seed
    );
    if (decision == DeckTlsClockDecision::keep) {
        return true;
    }
    return decision == DeckTlsClockDecision::seed &&
           set_wall_clock(static_cast<uint64_t>(seed) * 1'000U);
}

bool deck_tls_clock_accept_trusted_utc(uint64_t unix_ms)
{
    return deck_tls_clock_trusted_utc_allowed(unix_ms, kFirmwareBuildUnix) &&
           set_wall_clock(unix_ms);
}
