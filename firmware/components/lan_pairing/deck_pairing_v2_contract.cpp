#include "deck_pairing_v2_contract.h"

#include <cinttypes>
#include <cstdio>
#include <cstring>
#include <new>

namespace {

constexpr size_t kMaximumFields = 16;

enum class ValueType : uint8_t {
    string,
    unsigned_integer,
};

struct Span {
    const char *data = nullptr;
    size_t size = 0;
};

struct Field {
    Span name;
    ValueType type = ValueType::string;
    Span string_value;
    uint64_t unsigned_value = 0;
};

void secure_clear(void *buffer, size_t size)
{
    volatile uint8_t *bytes = static_cast<volatile uint8_t *>(buffer);
    while (size > 0) {
        *bytes++ = 0;
        --size;
    }
}

bool span_equal(Span span, const char *value)
{
    const size_t value_size = std::strlen(value);
    return span.size == value_size && std::memcmp(span.data, value, value_size) == 0;
}

class FlatJsonCursor {
public:
    FlatJsonCursor(const char *document, size_t size)
        : current_(document), end_(document == nullptr ? nullptr : document + size)
    {
    }

    bool parse(Field fields[kMaximumFields], size_t *field_count)
    {
        if (field_count == nullptr || !consume('{')) {
            return false;
        }
        *field_count = 0;
        whitespace();
        if (current_ == end_ || *current_ == '}') {
            return false;
        }
        while (true) {
            if (*field_count >= kMaximumFields) {
                return false;
            }
            Field &field = fields[*field_count];
            if (!string(&field.name) || field.name.size == 0 || field.name.size > 31 ||
                !consume(':') || !value(&field)) {
                return false;
            }
            for (size_t index = 0; index < *field_count; ++index) {
                if (fields[index].name.size == field.name.size &&
                    std::memcmp(fields[index].name.data, field.name.data, field.name.size) == 0) {
                    return false;
                }
            }
            ++*field_count;
            whitespace();
            if (current_ == end_) {
                return false;
            }
            if (*current_ == '}') {
                ++current_;
                whitespace();
                return current_ == end_;
            }
            if (*current_++ != ',') {
                return false;
            }
        }
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

    bool consume(char expected)
    {
        whitespace();
        if (current_ == end_ || *current_ != expected) {
            return false;
        }
        ++current_;
        return true;
    }

    bool string(Span *output)
    {
        whitespace();
        if (output == nullptr || current_ == end_ || *current_++ != '"') {
            return false;
        }
        const char *start = current_;
        while (current_ != end_ && *current_ != '"') {
            const unsigned char byte = static_cast<unsigned char>(*current_++);
            if (byte < 0x20U || byte > 0x7eU || byte == '\\') {
                return false;
            }
        }
        if (current_ == end_) {
            return false;
        }
        output->data = start;
        output->size = static_cast<size_t>(current_ - start);
        ++current_;
        return true;
    }

    bool value(Field *field)
    {
        whitespace();
        if (current_ == end_) {
            return false;
        }
        if (*current_ == '"') {
            field->type = ValueType::string;
            return string(&field->string_value);
        }
        if (*current_ < '0' || *current_ > '9') {
            return false;
        }
        if (*current_ == '0' && current_ + 1 != end_ &&
            current_[1] >= '0' && current_[1] <= '9') {
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
        field->type = ValueType::unsigned_integer;
        field->unsigned_value = value;
        return true;
    }

    const char *current_;
    const char *end_;
};

const Field *find_field(const Field *fields, size_t count, const char *name)
{
    for (size_t index = 0; index < count; ++index) {
        if (span_equal(fields[index].name, name)) {
            return &fields[index];
        }
    }
    return nullptr;
}

bool exact_fields(
    const Field *fields,
    size_t count,
    const char *const *names,
    size_t expected_count
)
{
    if (count != expected_count) {
        return false;
    }
    for (size_t index = 0; index < expected_count; ++index) {
        if (find_field(fields, count, names[index]) == nullptr) {
            return false;
        }
    }
    return true;
}

bool string_field(const Field *fields, size_t count, const char *name, Span *output)
{
    const Field *field = find_field(fields, count, name);
    if (field == nullptr || field->type != ValueType::string || output == nullptr) {
        return false;
    }
    *output = field->string_value;
    return true;
}

bool integer_field(const Field *fields, size_t count, const char *name, uint32_t *output)
{
    const Field *field = find_field(fields, count, name);
    if (field == nullptr || field->type != ValueType::unsigned_integer ||
        field->unsigned_value > UINT32_MAX || output == nullptr) {
        return false;
    }
    *output = static_cast<uint32_t>(field->unsigned_value);
    return true;
}

bool copy_span(Span source, char *output, size_t capacity)
{
    if (output == nullptr || source.size == 0 || source.size >= capacity) {
        return false;
    }
    std::memcpy(output, source.data, source.size);
    output[source.size] = '\0';
    return true;
}

bool bounded_span(const char *value, size_t capacity, Span *output)
{
    if (value == nullptr || capacity == 0 || output == nullptr) {
        return false;
    }
    size_t size = 0;
    while (size < capacity && value[size] != '\0') {
        ++size;
    }
    if (size == 0 || size == capacity) {
        return false;
    }
    *output = {value, size};
    return true;
}

bool lowercase_hex(Span value, size_t expected)
{
    if (value.size != expected) {
        return false;
    }
    for (size_t index = 0; index < value.size; ++index) {
        const char byte = value.data[index];
        if (!((byte >= '0' && byte <= '9') || (byte >= 'a' && byte <= 'f'))) {
            return false;
        }
    }
    return true;
}

bool digest(Span value)
{
    return value.size == 71 && std::memcmp(value.data, "sha256:", 7) == 0 &&
           lowercase_hex({value.data + 7, 64}, 64);
}

bool token(Span value)
{
    if (value.size != 43) {
        return false;
    }
    for (size_t index = 0; index < value.size; ++index) {
        const char byte = value.data[index];
        if (!((byte >= 'A' && byte <= 'Z') || (byte >= 'a' && byte <= 'z') ||
              (byte >= '0' && byte <= '9') || byte == '_' || byte == '-')) {
            return false;
        }
    }
    return true;
}

int base64_value(char value, bool url_safe)
{
    if (value >= 'A' && value <= 'Z') {
        return value - 'A';
    }
    if (value >= 'a' && value <= 'z') {
        return value - 'a' + 26;
    }
    if (value >= '0' && value <= '9') {
        return value - '0' + 52;
    }
    if (value == (url_safe ? '-' : '+')) {
        return 62;
    }
    if (value == (url_safe ? '_' : '/')) {
        return 63;
    }
    return -1;
}

bool decode_base64(
    Span encoded,
    bool url_safe,
    uint8_t *output,
    size_t capacity,
    size_t *output_size
)
{
    if (encoded.size == 0 || output == nullptr || output_size == nullptr ||
        (!url_safe && encoded.size % 4U != 0) ||
        (url_safe && encoded.size % 4U == 1U)) {
        return false;
    }
    size_t padding = 0;
    if (!url_safe) {
        if (encoded.size >= 1 && encoded.data[encoded.size - 1] == '=') {
            ++padding;
        }
        if (encoded.size >= 2 && encoded.data[encoded.size - 2] == '=') {
            ++padding;
        }
    }
    const size_t symbols = encoded.size - padding;
    const size_t decoded_size = (symbols * 6U) / 8U;
    if (decoded_size == 0 || decoded_size > capacity || padding > 2) {
        return false;
    }
    uint32_t accumulator = 0;
    unsigned bits = 0;
    size_t written = 0;
    for (size_t index = 0; index < symbols; ++index) {
        const int value = base64_value(encoded.data[index], url_safe);
        if (value < 0) {
            return false;
        }
        accumulator = (accumulator << 6U) | static_cast<uint32_t>(value);
        bits += 6U;
        if (bits >= 8U) {
            bits -= 8U;
            if (written >= capacity) {
                return false;
            }
            output[written++] = static_cast<uint8_t>((accumulator >> bits) & 0xffU);
        }
    }
    for (size_t index = symbols; index < encoded.size; ++index) {
        if (encoded.data[index] != '=') {
            return false;
        }
    }
    const uint32_t unused_mask = bits == 0 ? 0U : (1U << bits) - 1U;
    if (written != decoded_size || (accumulator & unused_mask) != 0U) {
        secure_clear(output, written);
        return false;
    }
    *output_size = written;
    return true;
}

bool device_identity(Span value)
{
    if (value.size < 22 || value.size > 683) {
        return false;
    }
    uint8_t decoded[512]{};
    size_t decoded_size = 0;
    const bool valid = decode_base64(value, true, decoded, sizeof(decoded), &decoded_size) &&
                       decoded_size >= 16 && decoded_size <= sizeof(decoded);
    secure_clear(decoded, sizeof(decoded));
    return valid;
}

bool device_id(Span value)
{
    if (value.size < 8 || value.size > 64 ||
        !((value.data[0] >= 'a' && value.data[0] <= 'z') ||
          (value.data[0] >= '0' && value.data[0] <= '9'))) {
        return false;
    }
    for (size_t index = 1; index < value.size; ++index) {
        const char byte = value.data[index];
        if (!((byte >= 'a' && byte <= 'z') || (byte >= '0' && byte <= '9') ||
              byte == '_' || byte == '-')) {
            return false;
        }
    }
    return true;
}

bool hub_service(Span value)
{
    constexpr char suffix[] = "._s3rlcd-hub._tcp.local.";
    constexpr size_t suffix_size = sizeof(suffix) - 1;
    if (value.size <= suffix_size || value.size - suffix_size > 63 ||
        std::memcmp(value.data + value.size - suffix_size, suffix, suffix_size) != 0) {
        return false;
    }
    const size_t prefix_size = value.size - suffix_size;
    for (size_t index = 0; index < prefix_size; ++index) {
        const char byte = value.data[index];
        if (!((byte >= 'a' && byte <= 'z') || (byte >= '0' && byte <= '9') ||
              (index != 0 && byte == '-'))) {
            return false;
        }
    }
    return true;
}

bool parse_decimal_octet(Span value, uint8_t *output)
{
    if (value.size == 0 || value.size > 3 || output == nullptr ||
        (value.size > 1 && value.data[0] == '0')) {
        return false;
    }
    unsigned result = 0;
    for (size_t index = 0; index < value.size; ++index) {
        if (value.data[index] < '0' || value.data[index] > '9') {
            return false;
        }
        result = result * 10U + static_cast<unsigned>(value.data[index] - '0');
    }
    if (result > 255U) {
        return false;
    }
    *output = static_cast<uint8_t>(result);
    return true;
}

bool hub_address(Span value)
{
    const char *colon = nullptr;
    for (size_t index = 0; index < value.size; ++index) {
        if (value.data[index] == ':') {
            if (colon != nullptr) {
                return false;
            }
            colon = value.data + index;
        }
    }
    if (colon == nullptr || colon == value.data || colon + 1 == value.data + value.size) {
        return false;
    }
    const Span port_span{colon + 1, static_cast<size_t>(value.data + value.size - colon - 1)};
    uint32_t port = 0;
    for (size_t index = 0; index < port_span.size; ++index) {
        if (port_span.data[index] < '0' || port_span.data[index] > '9' ||
            (port_span.size > 1 && port_span.data[0] == '0')) {
            return false;
        }
        port = port * 10U + static_cast<uint32_t>(port_span.data[index] - '0');
        if (port > UINT16_MAX) {
            return false;
        }
    }
    if (port == 0) {
        return false;
    }
    uint8_t octets[4]{};
    const char *part = value.data;
    for (size_t index = 0; index < 4; ++index) {
        const char *end = index == 3 ? colon : nullptr;
        if (index != 3) {
            for (const char *cursor = part; cursor < colon; ++cursor) {
                if (*cursor == '.') {
                    end = cursor;
                    break;
                }
            }
        }
        if (end == nullptr || !parse_decimal_octet(
                                  {part, static_cast<size_t>(end - part)},
                                  &octets[index]
                              )) {
            return false;
        }
        part = end + 1;
    }
    if (part != colon + 1) {
        return false;
    }
    return octets[0] == 10 ||
           (octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31) ||
           (octets[0] == 192 && octets[1] == 168) ||
           (octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127);
}

uint8_t hex_value(char value)
{
    if (value >= '0' && value <= '9') {
        return static_cast<uint8_t>(value - '0');
    }
    return static_cast<uint8_t>(value - 'a' + 10);
}

bool certificate(
    Span encoded,
    Span fingerprint,
    const deck_pairing_v2_crypto_t *crypto,
    deck_pairing_v2_credentials_t *credentials
)
{
    if (crypto == nullptr || crypto->sha256 == nullptr || credentials == nullptr ||
        encoded.size > 1368 || !digest(fingerprint) ||
        !decode_base64(
            encoded,
            false,
            credentials->certificate_der,
            sizeof(credentials->certificate_der),
            &credentials->certificate_der_size
        )) {
        return false;
    }
    uint8_t actual[32]{};
    uint8_t expected[32]{};
    for (size_t index = 0; index < sizeof(expected); ++index) {
        expected[index] = static_cast<uint8_t>(
            (hex_value(fingerprint.data[7 + index * 2]) << 4U) |
            hex_value(fingerprint.data[8 + index * 2])
        );
    }
    const bool hashed = crypto->sha256(
        crypto->context,
        credentials->certificate_der,
        credentials->certificate_der_size,
        actual
    );
    uint8_t difference = 0;
    for (size_t index = 0; index < sizeof(actual); ++index) {
        difference |= static_cast<uint8_t>(actual[index] ^ expected[index]);
    }
    secure_clear(actual, sizeof(actual));
    secure_clear(expected, sizeof(expected));
    return hashed && difference == 0;
}

bool state(Span value)
{
    return span_equal(value, "staged") || span_equal(value, "committed") ||
           span_equal(value, "connecting") || span_equal(value, "online") ||
           span_equal(value, "failed") || span_equal(value, "cancelled") ||
           span_equal(value, "expired");
}

bool error_code(Span value)
{
    return span_equal(value, "none") || span_equal(value, "busy") ||
           span_equal(value, "expired") || span_equal(value, "rate_limited") ||
           span_equal(value, "incompatible_protocol") || span_equal(value, "malformed") ||
           span_equal(value, "authentication_failed") || span_equal(value, "storage_failure") ||
           span_equal(value, "capacity_reached") || span_equal(value, "link_failed") ||
           span_equal(value, "cancelled");
}

constexpr size_t kTranscriptCapacity = DECK_PAIRING_V2_MAX_DOCUMENT_BYTES;

class TranscriptBuilder {
public:
    TranscriptBuilder(uint8_t *buffer, size_t capacity)
        : buffer_(buffer), capacity_(capacity)
    {
    }

    bool domain(const char *value)
    {
        return append(
            reinterpret_cast<const uint8_t *>(value),
            std::strlen(value) + 1U
        );
    }

    bool field(const char *label, const uint8_t *value, size_t value_size)
    {
        if (label == nullptr || (value == nullptr && value_size != 0) || value_size > UINT32_MAX) {
            return false;
        }
        const size_t label_size = std::strlen(label);
        const uint32_t length = static_cast<uint32_t>(value_size);
        const uint8_t encoded_length[4] = {
            static_cast<uint8_t>((length >> 24U) & 0xffU),
            static_cast<uint8_t>((length >> 16U) & 0xffU),
            static_cast<uint8_t>((length >> 8U) & 0xffU),
            static_cast<uint8_t>(length & 0xffU),
        };
        return append(reinterpret_cast<const uint8_t *>(label), label_size) &&
               append(reinterpret_cast<const uint8_t *>("\0"), 1U) &&
               append(encoded_length, sizeof(encoded_length)) && append(value, value_size);
    }

    bool text(const char *label, const char *value, size_t capacity)
    {
        if (value == nullptr || capacity == 0) {
            return false;
        }
        size_t value_size = 0;
        while (value_size < capacity && value[value_size] != '\0') {
            ++value_size;
        }
        return value_size > 0 && value_size < capacity &&
               field(label, reinterpret_cast<const uint8_t *>(value), value_size);
    }

    bool unsigned_integer(const char *label, uint32_t value)
    {
        const uint8_t encoded[4] = {
            static_cast<uint8_t>((value >> 24U) & 0xffU),
            static_cast<uint8_t>((value >> 16U) & 0xffU),
            static_cast<uint8_t>((value >> 8U) & 0xffU),
            static_cast<uint8_t>(value & 0xffU),
        };
        return field(label, encoded, sizeof(encoded));
    }

    const uint8_t *data() const { return buffer_; }
    size_t size() const { return size_; }

private:
    bool append(const uint8_t *value, size_t value_size)
    {
        if ((value == nullptr && value_size != 0) || value_size > capacity_ - size_) {
            return false;
        }
        if (value_size != 0) {
            std::memcpy(buffer_ + size_, value, value_size);
        }
        size_ += value_size;
        return true;
    }

    uint8_t *buffer_ = nullptr;
    size_t capacity_ = 0;
    size_t size_ = 0;
};

bool equal_bounded(const char *left, size_t left_capacity, const char *right, size_t right_capacity)
{
    size_t left_size = 0;
    size_t right_size = 0;
    while (left_size < left_capacity && left[left_size] != '\0') {
        ++left_size;
    }
    while (right_size < right_capacity && right[right_size] != '\0') {
        ++right_size;
    }
    return left_size < left_capacity && right_size < right_capacity &&
           left_size == right_size && std::memcmp(left, right, left_size) == 0;
}

void encode_digest(const uint8_t digest_value[32], char output[DECK_PAIRING_V2_DIGEST_CAPACITY])
{
    constexpr char hexadecimal[] = "0123456789abcdef";
    std::memcpy(output, "sha256:", 7U);
    for (size_t index = 0; index < 32U; ++index) {
        output[7U + index * 2U] = hexadecimal[digest_value[index] >> 4U];
        output[8U + index * 2U] = hexadecimal[digest_value[index] & 0x0fU];
    }
    output[71] = '\0';
}

bool common(
    const Field *fields,
    size_t count,
    deck_pairing_v2_message_t *message,
    Span *message_type
)
{
    Span session{};
    Span transaction{};
    uint32_t version = 0;
    return string_field(fields, count, "type", message_type) &&
           integer_field(fields, count, "protocol_version", &version) &&
           version == DECK_PAIRING_V2_PROTOCOL_VERSION &&
           string_field(fields, count, "session_id", &session) && lowercase_hex(session, 32) &&
           string_field(fields, count, "transaction_id", &transaction) &&
           lowercase_hex(transaction, 32) &&
           integer_field(fields, count, "sequence", &message->common.sequence) &&
           message->common.sequence != 0 &&
           copy_span(session, message->common.session_id, sizeof(message->common.session_id)) &&
           copy_span(
               transaction,
               message->common.transaction_id,
               sizeof(message->common.transaction_id)
           );
}

bool credentials_message(
    const Field *fields,
    size_t count,
    const deck_pairing_v2_crypto_t *crypto,
    deck_pairing_v2_message_t *message
)
{
    static constexpr const char *names[] = {
        "type", "protocol_version", "session_id", "transaction_id", "sequence",
        "window_nonce", "companion_nonce", "hub_service", "hub_address", "token",
        "certificate_fingerprint", "certificate_der", "device_link_protocol",
    };
    Span window{};
    Span companion{};
    Span service{};
    Span address{};
    Span token_value{};
    Span fingerprint{};
    Span certificate_der{};
    return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
           message->common.sequence == 1 &&
           string_field(fields, count, "window_nonce", &window) && lowercase_hex(window, 32) &&
           string_field(fields, count, "companion_nonce", &companion) &&
           lowercase_hex(companion, 32) &&
           string_field(fields, count, "hub_service", &service) && hub_service(service) &&
           string_field(fields, count, "hub_address", &address) && hub_address(address) &&
           string_field(fields, count, "token", &token_value) && token(token_value) &&
           string_field(fields, count, "certificate_fingerprint", &fingerprint) &&
           string_field(fields, count, "certificate_der", &certificate_der) &&
           integer_field(
               fields,
               count,
               "device_link_protocol",
               &message->credentials.device_link_protocol
           ) &&
           message->credentials.device_link_protocol == 1 &&
           certificate(certificate_der, fingerprint, crypto, &message->credentials) &&
           copy_span(window, message->credentials.window_nonce, sizeof(message->credentials.window_nonce)) &&
           copy_span(
               companion,
               message->credentials.companion_nonce,
               sizeof(message->credentials.companion_nonce)
           ) &&
           copy_span(service, message->credentials.hub_service, sizeof(message->credentials.hub_service)) &&
           copy_span(address, message->credentials.hub_address, sizeof(message->credentials.hub_address)) &&
           copy_span(token_value, message->credentials.token, sizeof(message->credentials.token)) &&
           copy_span(
               fingerprint,
               message->credentials.certificate_fingerprint,
               sizeof(message->credentials.certificate_fingerprint)
           );
}

bool validate_message(
    const Field *fields,
    size_t count,
    Span message_type,
    const deck_pairing_v2_crypto_t *crypto,
    deck_pairing_v2_message_t *message
)
{
    if (span_equal(message_type, "pairing.credentials")) {
        message->type = DECK_PAIRING_V2_MESSAGE_CREDENTIALS;
        return credentials_message(fields, count, crypto, message);
    }
    if (span_equal(message_type, "pairing.commit_ready")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
            "window_nonce", "companion_nonce", "deck_nonce", "device_id", "device_identity",
            "profile_id", "transcript_sha256",
        };
        Span window{};
        Span companion{};
        Span deck{};
        Span id{};
        Span identity{};
        Span profile{};
        Span transcript{};
        message->type = DECK_PAIRING_V2_MESSAGE_COMMIT_READY;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               message->common.sequence == 2 &&
               string_field(fields, count, "window_nonce", &window) && lowercase_hex(window, 32) &&
               string_field(fields, count, "companion_nonce", &companion) &&
               lowercase_hex(companion, 32) &&
               string_field(fields, count, "deck_nonce", &deck) && lowercase_hex(deck, 32) &&
               string_field(fields, count, "device_id", &id) && device_id(id) &&
               string_field(fields, count, "device_identity", &identity) && device_identity(identity) &&
               string_field(fields, count, "profile_id", &profile) && digest(profile) &&
               string_field(fields, count, "transcript_sha256", &transcript) && digest(transcript) &&
               copy_span(
                   window,
                   message->commit_ready.window_nonce,
                   sizeof(message->commit_ready.window_nonce)
               ) &&
               copy_span(
                   companion,
                   message->commit_ready.companion_nonce,
                   sizeof(message->commit_ready.companion_nonce)
               ) &&
               copy_span(
                   deck,
                   message->commit_ready.deck_nonce,
                   sizeof(message->commit_ready.deck_nonce)
               ) &&
               copy_span(
                   id,
                   message->commit_ready.device_id,
                   sizeof(message->commit_ready.device_id)
               ) &&
               copy_span(
                   identity,
                   message->commit_ready.device_identity,
                   sizeof(message->commit_ready.device_identity)
               ) &&
               copy_span(
                   profile,
                   message->commit_ready.profile_id,
                   sizeof(message->commit_ready.profile_id)
               ) &&
               copy_span(
                   transcript,
                   message->commit_ready.transcript_sha256,
                   sizeof(message->commit_ready.transcript_sha256)
               );
    }
    if (span_equal(message_type, "pairing.commit")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
            "deck_nonce", "transcript_sha256",
        };
        Span deck{};
        Span transcript{};
        message->type = DECK_PAIRING_V2_MESSAGE_COMMIT;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               message->common.sequence == 3 &&
               string_field(fields, count, "deck_nonce", &deck) && lowercase_hex(deck, 32) &&
               string_field(fields, count, "transcript_sha256", &transcript) && digest(transcript) &&
               copy_span(deck, message->commit.deck_nonce, sizeof(message->commit.deck_nonce)) &&
               copy_span(
                   transcript,
                   message->commit.transcript_sha256,
                   sizeof(message->commit.transcript_sha256)
               );
    }
    if (span_equal(message_type, "pairing.commit_receipt")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
            "profile_id", "profile_generation", "transcript_sha256",
        };
        Span profile{};
        Span transcript{};
        message->type = DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               message->common.sequence == 4 &&
               string_field(fields, count, "profile_id", &profile) && digest(profile) &&
               integer_field(
                   fields,
                   count,
                   "profile_generation",
                   &message->profile_generation
               ) &&
               message->profile_generation != 0 &&
               string_field(fields, count, "transcript_sha256", &transcript) && digest(transcript) &&
               copy_span(profile, message->profile_id, sizeof(message->profile_id)) &&
               copy_span(
                   transcript,
                   message->transcript_sha256,
                   sizeof(message->transcript_sha256)
               );
    }
    if (span_equal(message_type, "pairing.status_request")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
        };
        message->type = DECK_PAIRING_V2_MESSAGE_STATUS_REQUEST;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               message->common.sequence >= 5;
    }
    if (span_equal(message_type, "pairing.status")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
            "state", "error_code", "transcript_sha256",
        };
        Span state_value{};
        Span error{};
        Span transcript{};
        message->type = DECK_PAIRING_V2_MESSAGE_STATUS;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               message->common.sequence >= 5 &&
               string_field(fields, count, "state", &state_value) && state(state_value) &&
               string_field(fields, count, "error_code", &error) && error_code(error) &&
               string_field(fields, count, "transcript_sha256", &transcript) && digest(transcript) &&
               copy_span(state_value, message->state, sizeof(message->state)) &&
               copy_span(error, message->error_code, sizeof(message->error_code)) &&
               copy_span(
                   transcript,
                   message->transcript_sha256,
                   sizeof(message->transcript_sha256)
               );
    }
    if (span_equal(message_type, "pairing.cancel")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence",
        };
        message->type = DECK_PAIRING_V2_MESSAGE_CANCEL;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0]));
    }
    if (span_equal(message_type, "pairing.error")) {
        static constexpr const char *names[] = {
            "type", "protocol_version", "session_id", "transaction_id", "sequence", "code",
        };
        Span code{};
        message->type = DECK_PAIRING_V2_MESSAGE_ERROR;
        return exact_fields(fields, count, names, sizeof(names) / sizeof(names[0])) &&
               string_field(fields, count, "code", &code) && error_code(code) &&
               copy_span(code, message->error_code, sizeof(message->error_code));
    }
    return false;
}

}  // namespace

bool deck_pairing_v2_contract_decode(
    const char *document,
    size_t document_size,
    const deck_pairing_v2_crypto_t *crypto,
    deck_pairing_v2_message_t *message
)
{
    if (document == nullptr || document_size == 0 ||
        document_size > DECK_PAIRING_V2_MAX_DOCUMENT_BYTES || message == nullptr) {
        return false;
    }
    deck_pairing_v2_contract_clear(message);
    Field fields[kMaximumFields]{};
    size_t count = 0;
    FlatJsonCursor cursor(document, document_size);
    Span message_type{};
    const bool valid = cursor.parse(fields, &count) &&
                       common(fields, count, message, &message_type) &&
                       validate_message(fields, count, message_type, crypto, message);
    secure_clear(fields, sizeof(fields));
    if (!valid) {
        deck_pairing_v2_contract_clear(message);
    }
    return valid;
}

void deck_pairing_v2_contract_clear(deck_pairing_v2_message_t *message)
{
    if (message != nullptr) {
        secure_clear(message, sizeof(*message));
    }
}

bool deck_pairing_v2_contract_encode(
    const deck_pairing_v2_message_t *message,
    char *document,
    size_t document_capacity,
    size_t *document_size
)
{
    if (document != nullptr && document_capacity != 0) {
        secure_clear(document, document_capacity);
    }
    if (message == nullptr || document == nullptr || document_capacity == 0 ||
        document_size == nullptr) {
        return false;
    }
    *document_size = 0;
    Span session{};
    Span transaction{};
    if (!bounded_span(message->common.session_id, sizeof(message->common.session_id), &session) ||
        !lowercase_hex(session, 32) ||
        !bounded_span(
            message->common.transaction_id,
            sizeof(message->common.transaction_id),
            &transaction
        ) ||
        !lowercase_hex(transaction, 32)) {
        return false;
    }
    int written = -1;
    if (message->type == DECK_PAIRING_V2_MESSAGE_COMMIT_READY &&
        message->common.sequence == 2U) {
        Span window{};
        Span companion{};
        Span deck{};
        Span id{};
        Span identity{};
        Span profile{};
        Span transcript{};
        if (!bounded_span(
                message->commit_ready.window_nonce,
                sizeof(message->commit_ready.window_nonce),
                &window
            ) ||
            !lowercase_hex(window, 32) ||
            !bounded_span(
                message->commit_ready.companion_nonce,
                sizeof(message->commit_ready.companion_nonce),
                &companion
            ) ||
            !lowercase_hex(companion, 32) ||
            !bounded_span(
                message->commit_ready.deck_nonce,
                sizeof(message->commit_ready.deck_nonce),
                &deck
            ) ||
            !lowercase_hex(deck, 32) ||
            !bounded_span(message->commit_ready.device_id, sizeof(message->commit_ready.device_id), &id) ||
            !device_id(id) ||
            !bounded_span(
                message->commit_ready.device_identity,
                sizeof(message->commit_ready.device_identity),
                &identity
            ) ||
            !device_identity(identity) ||
            !bounded_span(
                message->commit_ready.profile_id,
                sizeof(message->commit_ready.profile_id),
                &profile
            ) ||
            !digest(profile) ||
            !bounded_span(
                message->commit_ready.transcript_sha256,
                sizeof(message->commit_ready.transcript_sha256),
                &transcript
            ) ||
            !digest(transcript)) {
            return false;
        }
        written = std::snprintf(
            document,
            document_capacity,
            "{\"type\":\"pairing.commit_ready\",\"protocol_version\":2,"
            "\"session_id\":\"%s\",\"transaction_id\":\"%s\",\"sequence\":2,"
            "\"window_nonce\":\"%s\",\"companion_nonce\":\"%s\","
            "\"deck_nonce\":\"%s\",\"device_id\":\"%s\",\"device_identity\":\"%s\","
            "\"profile_id\":\"%s\",\"transcript_sha256\":\"%s\"}",
            message->common.session_id,
            message->common.transaction_id,
            message->commit_ready.window_nonce,
            message->commit_ready.companion_nonce,
            message->commit_ready.deck_nonce,
            message->commit_ready.device_id,
            message->commit_ready.device_identity,
            message->commit_ready.profile_id,
            message->commit_ready.transcript_sha256
        );
    } else if (message->type == DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT &&
               message->common.sequence == 4U && message->profile_generation != 0U) {
        Span profile{};
        Span transcript{};
        if (!bounded_span(message->profile_id, sizeof(message->profile_id), &profile) ||
            !digest(profile) ||
            !bounded_span(
                message->transcript_sha256,
                sizeof(message->transcript_sha256),
                &transcript
            ) ||
            !digest(transcript)) {
            return false;
        }
        written = std::snprintf(
            document,
            document_capacity,
            "{\"type\":\"pairing.commit_receipt\",\"protocol_version\":2,"
            "\"session_id\":\"%s\",\"transaction_id\":\"%s\",\"sequence\":4,"
            "\"profile_id\":\"%s\",\"profile_generation\":%" PRIu32 ","
            "\"transcript_sha256\":\"%s\"}",
            message->common.session_id,
            message->common.transaction_id,
            message->profile_id,
            message->profile_generation,
            message->transcript_sha256
        );
    }
    if (written <= 0 || static_cast<size_t>(written) >= document_capacity) {
        secure_clear(document, document_capacity);
        return false;
    }
    *document_size = static_cast<size_t>(written);
    return true;
}

bool deck_pairing_v2_contract_transcript_sha256(
    const deck_pairing_v2_message_t *credentials,
    const deck_pairing_v2_message_t *commit_ready,
    const deck_pairing_v2_crypto_t *crypto,
    char output[DECK_PAIRING_V2_DIGEST_CAPACITY]
)
{
    if (output != nullptr) {
        secure_clear(output, DECK_PAIRING_V2_DIGEST_CAPACITY);
    }
    if (credentials == nullptr || commit_ready == nullptr || crypto == nullptr ||
        crypto->sha256 == nullptr || output == nullptr ||
        credentials->type != DECK_PAIRING_V2_MESSAGE_CREDENTIALS ||
        commit_ready->type != DECK_PAIRING_V2_MESSAGE_COMMIT_READY ||
        credentials->common.sequence != 1U || commit_ready->common.sequence != 2U ||
        credentials->credentials.device_link_protocol != 1U ||
        credentials->credentials.certificate_der_size == 0 ||
        credentials->credentials.certificate_der_size > DECK_PAIRING_V2_CERTIFICATE_DER_CAPACITY ||
        !equal_bounded(
            credentials->common.session_id,
            sizeof(credentials->common.session_id),
            commit_ready->common.session_id,
            sizeof(commit_ready->common.session_id)
        ) ||
        !equal_bounded(
            credentials->common.transaction_id,
            sizeof(credentials->common.transaction_id),
            commit_ready->common.transaction_id,
            sizeof(commit_ready->common.transaction_id)
        ) ||
        !equal_bounded(
            credentials->credentials.window_nonce,
            sizeof(credentials->credentials.window_nonce),
            commit_ready->commit_ready.window_nonce,
            sizeof(commit_ready->commit_ready.window_nonce)
        ) ||
        !equal_bounded(
            credentials->credentials.companion_nonce,
            sizeof(credentials->credentials.companion_nonce),
            commit_ready->commit_ready.companion_nonce,
            sizeof(commit_ready->commit_ready.companion_nonce)
        ) ||
        !equal_bounded(
            credentials->credentials.certificate_fingerprint,
            sizeof(credentials->credentials.certificate_fingerprint),
            commit_ready->commit_ready.profile_id,
            sizeof(commit_ready->commit_ready.profile_id)
        )) {
        return false;
    }

    uint8_t *serialized = new (std::nothrow) uint8_t[kTranscriptCapacity];
    if (serialized == nullptr) {
        return false;
    }
    TranscriptBuilder transcript(serialized, kTranscriptCapacity);
    const bool encoded =
        transcript.domain("s3-rlcd-pairing-v2-transcript") &&
        transcript.unsigned_integer("protocol_version", DECK_PAIRING_V2_PROTOCOL_VERSION) &&
        transcript.text("session_id", credentials->common.session_id, sizeof(credentials->common.session_id)) &&
        transcript.text(
            "transaction_id",
            credentials->common.transaction_id,
            sizeof(credentials->common.transaction_id)
        ) &&
        transcript.text(
            "window_nonce",
            credentials->credentials.window_nonce,
            sizeof(credentials->credentials.window_nonce)
        ) &&
        transcript.text(
            "companion_nonce",
            credentials->credentials.companion_nonce,
            sizeof(credentials->credentials.companion_nonce)
        ) &&
        transcript.text(
            "hub_service",
            credentials->credentials.hub_service,
            sizeof(credentials->credentials.hub_service)
        ) &&
        transcript.text(
            "hub_address",
            credentials->credentials.hub_address,
            sizeof(credentials->credentials.hub_address)
        ) &&
        transcript.text("token", credentials->credentials.token, sizeof(credentials->credentials.token)) &&
        transcript.text(
            "certificate_fingerprint",
            credentials->credentials.certificate_fingerprint,
            sizeof(credentials->credentials.certificate_fingerprint)
        ) &&
        transcript.field(
            "certificate_der",
            credentials->credentials.certificate_der,
            credentials->credentials.certificate_der_size
        ) &&
        transcript.unsigned_integer(
            "device_link_protocol",
            credentials->credentials.device_link_protocol
        ) &&
        transcript.text(
            "deck_nonce",
            commit_ready->commit_ready.deck_nonce,
            sizeof(commit_ready->commit_ready.deck_nonce)
        ) &&
        transcript.text(
            "device_id",
            commit_ready->commit_ready.device_id,
            sizeof(commit_ready->commit_ready.device_id)
        ) &&
        transcript.text(
            "device_identity",
            commit_ready->commit_ready.device_identity,
            sizeof(commit_ready->commit_ready.device_identity)
        ) &&
        transcript.text(
            "profile_id",
            commit_ready->commit_ready.profile_id,
            sizeof(commit_ready->commit_ready.profile_id)
        );
    uint8_t digest_value[32]{};
    const bool hashed = encoded && crypto->sha256(
        crypto->context,
        transcript.data(),
        transcript.size(),
        digest_value
    );
    if (hashed) {
        encode_digest(digest_value, output);
    }
    secure_clear(digest_value, sizeof(digest_value));
    secure_clear(serialized, kTranscriptCapacity);
    delete[] serialized;
    return hashed;
}
