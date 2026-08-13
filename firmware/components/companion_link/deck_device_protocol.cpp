#include "deck_device_protocol.h"

#include <cstring>

namespace {

class JsonCursor {
public:
    JsonCursor(const char *message, size_t size)
        : current_(message), end_(message == nullptr ? nullptr : message + size)
    {
    }

    bool valid() const { return current_ != nullptr; }

    bool consume(char expected)
    {
        whitespace();
        if (current_ == end_ || *current_ != expected) {
            return false;
        }
        ++current_;
        return true;
    }

    bool string(char *output, size_t capacity)
    {
        whitespace();
        if (output == nullptr || capacity == 0 || current_ == end_ ||
            *current_++ != '"') {
            return false;
        }
        size_t size = 0;
        while (current_ != end_ && *current_ != '"') {
            unsigned char byte = static_cast<unsigned char>(*current_++);
            if (byte == '\\') {
                if (current_ == end_) {
                    return false;
                }
                const char escaped = *current_++;
                switch (escaped) {
                    case '"':
                    case '\\':
                    case '/':
                        byte = static_cast<unsigned char>(escaped);
                        break;
                    case 'b':
                        byte = '\b';
                        break;
                    case 'f':
                        byte = '\f';
                        break;
                    case 'n':
                        byte = '\n';
                        break;
                    case 'r':
                        byte = '\r';
                        break;
                    case 't':
                        byte = '\t';
                        break;
                    case 'u': {
                        unsigned codepoint = 0;
                        for (unsigned index = 0; index < 4; ++index) {
                            if (current_ == end_) {
                                return false;
                            }
                            const char digit = *current_++;
                            codepoint <<= 4U;
                            if (digit >= '0' && digit <= '9') {
                                codepoint |= static_cast<unsigned>(digit - '0');
                            } else if (digit >= 'a' && digit <= 'f') {
                                codepoint |= static_cast<unsigned>(digit - 'a' + 10);
                            } else if (digit >= 'A' && digit <= 'F') {
                                codepoint |= static_cast<unsigned>(digit - 'A' + 10);
                            } else {
                                return false;
                            }
                        }
                        if (codepoint > 0x7fU) {
                            return false;
                        }
                        byte = static_cast<unsigned char>(codepoint);
                        break;
                    }
                    default:
                        return false;
                }
            }
            if (byte < 0x20U || byte > 0x7eU || size + 1 >= capacity) {
                return false;
            }
            output[size++] = static_cast<char>(byte);
        }
        if (current_ == end_ || *current_++ != '"') {
            return false;
        }
        output[size] = '\0';
        return true;
    }

    bool unsigned_integer(uint64_t *output)
    {
        whitespace();
        if (output == nullptr || current_ == end_ || *current_ < '0' ||
            *current_ > '9') {
            return false;
        }
        uint64_t value = 0;
        do {
            const uint64_t digit = static_cast<uint64_t>(*current_ - '0');
            if (value > (UINT64_MAX - digit) / 10U) {
                return false;
            }
            value = value * 10U + digit;
            ++current_;
        } while (current_ != end_ && *current_ >= '0' && *current_ <= '9');
        if (current_ != end_ && (*current_ == '.' || *current_ == 'e' ||
                                 *current_ == 'E' || *current_ == '-')) {
            return false;
        }
        *output = value;
        return true;
    }

    bool object_separator(bool *done)
    {
        whitespace();
        if (current_ == end_) {
            return false;
        }
        if (*current_ == '}') {
            ++current_;
            *done = true;
            return true;
        }
        if (*current_ == ',') {
            ++current_;
            *done = false;
            return true;
        }
        return false;
    }

    bool array_separator(bool *done)
    {
        whitespace();
        if (current_ == end_) {
            return false;
        }
        if (*current_ == ']') {
            ++current_;
            *done = true;
            return true;
        }
        if (*current_ == ',') {
            ++current_;
            *done = false;
            return true;
        }
        return false;
    }

    bool finished()
    {
        whitespace();
        return current_ == end_;
    }

private:
    void whitespace()
    {
        while (current_ != end_ &&
               (*current_ == ' ' || *current_ == '\t' || *current_ == '\r' ||
                *current_ == '\n')) {
            ++current_;
        }
    }

    const char *current_;
    const char *end_;
};

bool safe_device_id(const char *value)
{
    const size_t size = std::strlen(value);
    if (size < 8 || size > 64 || value[0] < 'a' || value[0] > 'z') {
        return false;
    }
    for (size_t index = 1; index < size; ++index) {
        const char byte = value[index];
        if (!((byte >= 'a' && byte <= 'z') || (byte >= '0' && byte <= '9') ||
              byte == '_' || byte == '-')) {
            return false;
        }
    }
    return true;
}

bool safe_version(const char *value)
{
    const size_t size = std::strlen(value);
    if (size == 0 || size > 32) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        const char byte = value[index];
        const bool alpha_numeric =
            (byte >= 'A' && byte <= 'Z') || (byte >= 'a' && byte <= 'z') ||
            (byte >= '0' && byte <= '9');
        if (!alpha_numeric && (index == 0 || (byte != '.' && byte != '+' &&
                                             byte != '_' && byte != '-'))) {
            return false;
        }
    }
    return true;
}

bool parse_capabilities(JsonCursor *cursor)
{
    if (!cursor->consume('[')) {
        return false;
    }
    unsigned seen = 0;
    unsigned count = 0;
    bool done = false;
    while (!done) {
        char capability[16]{};
        if (!cursor->string(capability, sizeof(capability))) {
            return false;
        }
        unsigned bit = 0;
        if (std::strcmp(capability, "display") == 0) {
            bit = 1U;
        } else if (std::strcmp(capability, "serial") == 0) {
            bit = 2U;
        } else if (std::strcmp(capability, "ota") == 0) {
            bit = 4U;
        } else {
            return false;
        }
        if ((seen & bit) != 0) {
            return false;
        }
        seen |= bit;
        ++count;
        if (count > 8 || !cursor->array_separator(&done)) {
            return false;
        }
    }
    return count != 0;
}

bool leap_year(unsigned year)
{
    return year % 4U == 0U && (year % 100U != 0U || year % 400U == 0U);
}

unsigned month_days(unsigned year, unsigned month)
{
    constexpr unsigned kDays[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    return month == 2U && leap_year(year) ? 29U : kDays[month - 1U];
}

bool parse_digits(const char *value, size_t count, unsigned *output)
{
    unsigned parsed = 0;
    for (size_t index = 0; index < count; ++index) {
        if (value[index] < '0' || value[index] > '9') {
            return false;
        }
        parsed = parsed * 10U + static_cast<unsigned>(value[index] - '0');
    }
    *output = parsed;
    return true;
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
    return static_cast<int64_t>(era) * 146'097 +
           static_cast<int64_t>(day_of_era) - 719'468;
}

bool parse_canonical_utc(const char *value, uint64_t *unix_ms)
{
    const size_t size = std::strlen(value);
    if (size < 20 || size > 30 || value[4] != '-' || value[7] != '-' ||
        value[10] != 'T' || value[13] != ':' || value[16] != ':' ||
        value[size - 1] != 'Z') {
        return false;
    }
    unsigned year = 0;
    unsigned month = 0;
    unsigned day = 0;
    unsigned hour = 0;
    unsigned minute = 0;
    unsigned second = 0;
    if (!parse_digits(value, 4, &year) || !parse_digits(value + 5, 2, &month) ||
        !parse_digits(value + 8, 2, &day) || !parse_digits(value + 11, 2, &hour) ||
        !parse_digits(value + 14, 2, &minute) ||
        !parse_digits(value + 17, 2, &second) || year < 1970U || month == 0U ||
        month > 12U || day == 0U || day > month_days(year, month) || hour > 23U ||
        minute > 59U || second > 59U) {
        return false;
    }
    uint64_t fraction_ns = 0;
    if (size > 20) {
        const size_t digits = size - 21;
        if (value[19] != '.' || digits == 0 || digits > 9 ||
            value[size - 2] == '0') {
            return false;
        }
        for (size_t index = 0; index < digits; ++index) {
            if (value[20 + index] < '0' || value[20 + index] > '9') {
                return false;
            }
            fraction_ns = fraction_ns * 10U +
                          static_cast<uint64_t>(value[20 + index] - '0');
        }
        for (size_t index = digits; index < 9; ++index) {
            fraction_ns *= 10U;
        }
    }
    const int64_t days = days_from_civil(static_cast<int>(year), month, day);
    if (days < 0) {
        return false;
    }
    *unix_ms = static_cast<uint64_t>(days) * 86'400'000U +
               static_cast<uint64_t>(hour) * 3'600'000U +
               static_cast<uint64_t>(minute) * 60'000U +
               static_cast<uint64_t>(second) * 1'000U + fraction_ns / 1'000'000U;
    return true;
}

}  // namespace

bool deck_device_protocol_validate_hello(
    const char *message,
    size_t message_size,
    const char *authenticated_device_id
)
{
    if (message == nullptr || message_size == 0 ||
        message_size > DECK_DEVICE_PROTOCOL_MAX_CONTROL_BYTES ||
        authenticated_device_id == nullptr || !safe_device_id(authenticated_device_id)) {
        return false;
    }
    JsonCursor cursor(message, message_size);
    if (!cursor.valid() || !cursor.consume('{')) {
        return false;
    }
    uint8_t seen = 0;
    bool done = false;
    while (!done) {
        char key[32]{};
        if (!cursor.string(key, sizeof(key)) || !cursor.consume(':')) {
            return false;
        }
        uint8_t bit = 0;
        if (std::strcmp(key, "type") == 0) {
            bit = 1U;
            char value[17]{};
            if (!cursor.string(value, sizeof(value)) ||
                std::strcmp(value, "device.hello") != 0) {
                return false;
            }
        } else if (std::strcmp(key, "protocol_version") == 0) {
            bit = 2U;
            uint64_t value = 0;
            if (!cursor.unsigned_integer(&value) || value != DECK_DEVICE_PROTOCOL_VERSION) {
                return false;
            }
        } else if (std::strcmp(key, "device_id") == 0) {
            bit = 4U;
            char value[65]{};
            if (!cursor.string(value, sizeof(value)) || !safe_device_id(value) ||
                std::strcmp(value, authenticated_device_id) != 0) {
                return false;
            }
        } else if (std::strcmp(key, "firmware_version") == 0) {
            bit = 8U;
            char value[33]{};
            if (!cursor.string(value, sizeof(value)) || !safe_version(value)) {
                return false;
            }
        } else if (std::strcmp(key, "board") == 0) {
            bit = 16U;
            char value[32]{};
            if (!cursor.string(value, sizeof(value)) ||
                std::strcmp(value, "esp32-s3-rlcd-4.2") != 0) {
                return false;
            }
        } else if (std::strcmp(key, "capabilities") == 0) {
            bit = 32U;
            if (!parse_capabilities(&cursor)) {
                return false;
            }
        } else if (std::strcmp(key, "serial_state") == 0) {
            bit = 64U;
            char value[16]{};
            if (!cursor.string(value, sizeof(value)) ||
                std::strcmp(value, "disarmed") != 0) {
                return false;
            }
        } else {
            return false;
        }
        if ((seen & bit) != 0) {
            return false;
        }
        seen |= bit;
        if (!cursor.object_separator(&done)) {
            return false;
        }
    }
    return seen == 0x7fU && cursor.finished();
}

bool deck_device_protocol_parse_heartbeat(
    const char *message,
    size_t message_size,
    uint64_t previous_monotonic_ms,
    bool has_previous,
    deck_device_heartbeat_t *heartbeat
)
{
    if (message == nullptr || message_size == 0 ||
        message_size > DECK_DEVICE_PROTOCOL_MAX_CONTROL_BYTES || heartbeat == nullptr) {
        return false;
    }
    *heartbeat = {};
    JsonCursor cursor(message, message_size);
    if (!cursor.valid() || !cursor.consume('{')) {
        return false;
    }
    uint16_t seen = 0;
    uint64_t tx_depth = 0;
    uint64_t tx_capacity = 0;
    uint64_t rx_depth = 0;
    uint64_t rx_capacity = 0;
    bool done = false;
    while (!done) {
        char key[32]{};
        if (!cursor.string(key, sizeof(key)) || !cursor.consume(':')) {
            return false;
        }
        uint16_t bit = 0;
        if (std::strcmp(key, "type") == 0) {
            bit = 1U;
            char value[24]{};
            if (!cursor.string(value, sizeof(value)) ||
                std::strcmp(value, "device.heartbeat") != 0) {
                return false;
            }
        } else if (std::strcmp(key, "protocol_version") == 0) {
            bit = 2U;
            uint64_t value = 0;
            if (!cursor.unsigned_integer(&value) || value != DECK_DEVICE_PROTOCOL_VERSION) {
                return false;
            }
        } else if (std::strcmp(key, "utc") == 0) {
            bit = 4U;
            char value[32]{};
            if (!cursor.string(value, sizeof(value)) ||
                !parse_canonical_utc(value, &heartbeat->utc_unix_ms)) {
                return false;
            }
        } else if (std::strcmp(key, "monotonic_ms") == 0) {
            bit = 8U;
            if (!cursor.unsigned_integer(&heartbeat->monotonic_ms)) {
                return false;
            }
        } else if (std::strcmp(key, "tx_queue_depth") == 0) {
            bit = 16U;
            if (!cursor.unsigned_integer(&tx_depth) || tx_depth > UINT32_MAX) {
                return false;
            }
        } else if (std::strcmp(key, "tx_queue_capacity") == 0) {
            bit = 32U;
            if (!cursor.unsigned_integer(&tx_capacity) || tx_capacity > UINT32_MAX) {
                return false;
            }
        } else if (std::strcmp(key, "rx_queue_depth") == 0) {
            bit = 64U;
            if (!cursor.unsigned_integer(&rx_depth) || rx_depth > UINT32_MAX) {
                return false;
            }
        } else if (std::strcmp(key, "rx_queue_capacity") == 0) {
            bit = 128U;
            if (!cursor.unsigned_integer(&rx_capacity) || rx_capacity > UINT32_MAX) {
                return false;
            }
        } else {
            return false;
        }
        if ((seen & bit) != 0) {
            return false;
        }
        seen |= bit;
        if (!cursor.object_separator(&done)) {
            return false;
        }
    }
    return seen == 0xffU && cursor.finished() && tx_capacity != 0 &&
           rx_capacity != 0 && tx_depth <= tx_capacity && rx_depth <= rx_capacity &&
           (!has_previous || heartbeat->monotonic_ms >= previous_monotonic_ms);
}
