#include "deck_ai_snapshot.h"

#include <cctype>
#include <cstdint>
#include <cstring>
#include <new>

namespace {

constexpr size_t kMaximumNodes = 1024;
constexpr uint64_t kMaximumSafeInteger = 9'007'199'254'740'991ULL;
constexpr uint64_t kNoNode = UINT64_MAX;

enum class NodeType : uint8_t {
    object,
    array,
    string,
    integer,
    boolean,
    null_value,
};

struct Node {
    NodeType type{};
    size_t start{};
    size_t end{};
    size_t first_child{static_cast<size_t>(kNoNode)};
    size_t next_sibling{static_cast<size_t>(kNoNode)};
};

bool valid_utf8(const char *value, size_t size)
{
    size_t index = 0;
    while (index < size) {
        const uint8_t first = static_cast<uint8_t>(value[index]);
        if (first <= 0x7fU) {
            ++index;
            continue;
        }
        unsigned count = 0;
        uint32_t codepoint = 0;
        uint32_t minimum = 0;
        if ((first & 0xe0U) == 0xc0U) {
            count = 2;
            codepoint = first & 0x1fU;
            minimum = 0x80U;
        } else if ((first & 0xf0U) == 0xe0U) {
            count = 3;
            codepoint = first & 0x0fU;
            minimum = 0x800U;
        } else if ((first & 0xf8U) == 0xf0U) {
            count = 4;
            codepoint = first & 0x07U;
            minimum = 0x10000U;
        } else {
            return false;
        }
        if (index + count > size) {
            return false;
        }
        for (unsigned offset = 1; offset < count; ++offset) {
            const uint8_t byte = static_cast<uint8_t>(value[index + offset]);
            if ((byte & 0xc0U) != 0x80U) {
                return false;
            }
            codepoint = (codepoint << 6U) | (byte & 0x3fU);
        }
        if (codepoint < minimum || codepoint > 0x10ffffU ||
            (codepoint >= 0xd800U && codepoint <= 0xdfffU)) {
            return false;
        }
        index += count;
    }
    return true;
}

class Parser {
public:
    Parser(const char *document, size_t size, Node *nodes, size_t capacity)
        : document_(document), size_(size), nodes_(nodes), capacity_(capacity)
    {
    }

    bool parse(size_t *root)
    {
        whitespace();
        if (!value(root, 0) || *root != 0) {
            return false;
        }
        whitespace();
        return position_ == size_;
    }

    size_t count() const { return count_; }

private:
    bool allocate(NodeType type, size_t start, size_t *index)
    {
        if (count_ == capacity_) {
            return false;
        }
        *index = count_++;
        nodes_[*index] = Node{};
        nodes_[*index].type = type;
        nodes_[*index].start = start;
        return true;
    }

    void append_child(size_t parent, size_t child)
    {
        if (nodes_[parent].first_child == static_cast<size_t>(kNoNode)) {
            nodes_[parent].first_child = child;
            return;
        }
        size_t sibling = nodes_[parent].first_child;
        while (nodes_[sibling].next_sibling != static_cast<size_t>(kNoNode)) {
            sibling = nodes_[sibling].next_sibling;
        }
        nodes_[sibling].next_sibling = child;
    }

    bool value(size_t *index, unsigned depth)
    {
        whitespace();
        if (depth > 32U || position_ == size_) {
            return false;
        }
        switch (document_[position_]) {
            case '{':
                return object(index, depth);
            case '[':
                return array(index, depth);
            case '"':
                return string(index);
            case 't':
                return literal("true", NodeType::boolean, index);
            case 'f':
                return literal("false", NodeType::boolean, index);
            case 'n':
                return literal("null", NodeType::null_value, index);
            default:
                return integer(index);
        }
    }

    bool object(size_t *index, unsigned depth)
    {
        const size_t start = position_++;
        if (!allocate(NodeType::object, start, index)) {
            return false;
        }
        whitespace();
        if (consume('}')) {
            nodes_[*index].end = position_;
            return true;
        }
        while (true) {
            size_t key = 0;
            if (!string(&key) || duplicate_key(*index, key)) {
                return false;
            }
            append_child(*index, key);
            whitespace();
            if (!consume(':')) {
                return false;
            }
            size_t child = 0;
            if (!value(&child, depth + 1U)) {
                return false;
            }
            nodes_[key].first_child = child;
            whitespace();
            if (consume('}')) {
                nodes_[*index].end = position_;
                return true;
            }
            if (!consume(',')) {
                return false;
            }
            whitespace();
        }
    }

    bool array(size_t *index, unsigned depth)
    {
        const size_t start = position_++;
        if (!allocate(NodeType::array, start, index)) {
            return false;
        }
        whitespace();
        if (consume(']')) {
            nodes_[*index].end = position_;
            return true;
        }
        while (true) {
            size_t child = 0;
            if (!value(&child, depth + 1U)) {
                return false;
            }
            append_child(*index, child);
            whitespace();
            if (consume(']')) {
                nodes_[*index].end = position_;
                return true;
            }
            if (!consume(',')) {
                return false;
            }
            whitespace();
        }
    }

    bool string(size_t *index)
    {
        if (position_ == size_ || document_[position_] != '"') {
            return false;
        }
        ++position_;
        const size_t start = position_;
        if (!allocate(NodeType::string, start, index)) {
            return false;
        }
        while (position_ < size_) {
            const uint8_t byte = static_cast<uint8_t>(document_[position_++]);
            if (byte == '"') {
                nodes_[*index].end = position_ - 1U;
                return true;
            }
            if (byte < 0x20U) {
                return false;
            }
            if (byte != '\\') {
                continue;
            }
            if (position_ == size_) {
                return false;
            }
            const char escaped = document_[position_++];
            if (std::strchr("\"\\/bfnrt", escaped) != nullptr) {
                continue;
            }
            if (escaped != 'u' || position_ + 4U > size_) {
                return false;
            }
            for (unsigned digit = 0; digit < 4U; ++digit) {
                if (!std::isxdigit(static_cast<unsigned char>(document_[position_++]))) {
                    return false;
                }
            }
        }
        return false;
    }

    bool integer(size_t *index)
    {
        const size_t start = position_;
        if (position_ < size_ && document_[position_] == '-') {
            ++position_;
        }
        if (position_ == size_ || document_[position_] < '0' || document_[position_] > '9') {
            return false;
        }
        if (document_[position_] == '0') {
            ++position_;
            if (position_ < size_ && document_[position_] >= '0' && document_[position_] <= '9') {
                return false;
            }
        } else {
            while (position_ < size_ && document_[position_] >= '0' && document_[position_] <= '9') {
                ++position_;
            }
        }
        if (position_ < size_ && (document_[position_] == '.' || document_[position_] == 'e' ||
                                 document_[position_] == 'E')) {
            return false;
        }
        if (!allocate(NodeType::integer, start, index)) {
            return false;
        }
        nodes_[*index].end = position_;
        return true;
    }

    bool literal(const char *literal_value, NodeType type, size_t *index)
    {
        const size_t length = std::strlen(literal_value);
        const size_t start = position_;
        if (position_ + length > size_ ||
            std::memcmp(document_ + position_, literal_value, length) != 0) {
            return false;
        }
        position_ += length;
        if (!allocate(type, start, index)) {
            return false;
        }
        nodes_[*index].end = position_;
        return true;
    }

    bool duplicate_key(size_t object_index, size_t candidate) const;

    bool consume(char expected)
    {
        if (position_ == size_ || document_[position_] != expected) {
            return false;
        }
        ++position_;
        return true;
    }

    void whitespace()
    {
        while (position_ < size_ && (document_[position_] == ' ' ||
                                     document_[position_] == '\t' ||
                                     document_[position_] == '\r' ||
                                     document_[position_] == '\n')) {
            ++position_;
        }
    }

    const char *document_;
    size_t size_;
    Node *nodes_;
    size_t capacity_;
    size_t position_{};
    size_t count_{};
};

class Document {
public:
    Document(const char *text, const Node *nodes) : text_(text), nodes_(nodes) {}

    const Node &node(size_t index) const { return nodes_[index]; }

    bool decode_ascii(size_t index, char *output, size_t capacity) const
    {
        const Node &value = nodes_[index];
        if (value.type != NodeType::string || output == nullptr || capacity == 0) {
            return false;
        }
        size_t written = 0;
        for (size_t cursor = value.start; cursor < value.end;) {
            uint8_t byte = static_cast<uint8_t>(text_[cursor++]);
            if (byte == '\\') {
                if (cursor == value.end) {
                    return false;
                }
                const char escaped = text_[cursor++];
                switch (escaped) {
                    case '"':
                    case '\\':
                    case '/':
                        byte = static_cast<uint8_t>(escaped);
                        break;
                    case 'b': byte = '\b'; break;
                    case 'f': byte = '\f'; break;
                    case 'n': byte = '\n'; break;
                    case 'r': byte = '\r'; break;
                    case 't': byte = '\t'; break;
                    case 'u': {
                        unsigned codepoint = 0;
                        if (cursor + 4U > value.end) {
                            return false;
                        }
                        for (unsigned digit = 0; digit < 4U; ++digit) {
                            const char character = text_[cursor++];
                            codepoint <<= 4U;
                            if (character >= '0' && character <= '9') {
                                codepoint |= static_cast<unsigned>(character - '0');
                            } else if (character >= 'a' && character <= 'f') {
                                codepoint |= static_cast<unsigned>(character - 'a' + 10);
                            } else if (character >= 'A' && character <= 'F') {
                                codepoint |= static_cast<unsigned>(character - 'A' + 10);
                            } else {
                                return false;
                            }
                        }
                        if (codepoint > 0x7fU) {
                            return false;
                        }
                        byte = static_cast<uint8_t>(codepoint);
                        break;
                    }
                    default:
                        return false;
                }
            }
            if (byte > 0x7fU || written + 1U >= capacity) {
                return false;
            }
            output[written++] = static_cast<char>(byte);
        }
        output[written] = '\0';
        return true;
    }

    bool string_equals(size_t index, const char *expected) const
    {
        char decoded[96]{};
        return decode_ascii(index, decoded, sizeof(decoded)) &&
               std::strcmp(decoded, expected) == 0;
    }

    size_t field(size_t object_index, const char *name) const
    {
        if (nodes_[object_index].type != NodeType::object) {
            return static_cast<size_t>(kNoNode);
        }
        for (size_t key = nodes_[object_index].first_child;
             key != static_cast<size_t>(kNoNode); key = nodes_[key].next_sibling) {
            if (string_equals(key, name)) {
                return nodes_[key].first_child;
            }
        }
        return static_cast<size_t>(kNoNode);
    }

    size_t array_count(size_t array_index) const
    {
        if (nodes_[array_index].type != NodeType::array) {
            return 0;
        }
        size_t count = 0;
        for (size_t child = nodes_[array_index].first_child;
             child != static_cast<size_t>(kNoNode); child = nodes_[child].next_sibling) {
            ++count;
        }
        return count;
    }

    size_t array_at(size_t array_index, size_t wanted) const
    {
        size_t current = 0;
        for (size_t child = nodes_[array_index].first_child;
             child != static_cast<size_t>(kNoNode); child = nodes_[child].next_sibling) {
            if (current++ == wanted) {
                return child;
            }
        }
        return static_cast<size_t>(kNoNode);
    }

    bool unsigned_integer(size_t index, uint64_t *output) const
    {
        const Node &value = nodes_[index];
        if (value.type != NodeType::integer || value.start == value.end ||
            text_[value.start] == '-') {
            return false;
        }
        uint64_t parsed = 0;
        for (size_t cursor = value.start; cursor < value.end; ++cursor) {
            const uint64_t digit = static_cast<uint64_t>(text_[cursor] - '0');
            if (parsed > (UINT64_MAX - digit) / 10U) {
                return false;
            }
            parsed = parsed * 10U + digit;
        }
        *output = parsed;
        return true;
    }

    bool safe_text(size_t index, size_t maximum) const
    {
        const Node &value = nodes_[index];
        if (value.type != NodeType::string || value.end == value.start ||
            value.end - value.start > maximum * 4U) {
            return false;
        }
        for (size_t cursor = value.start; cursor < value.end; ++cursor) {
            if (text_[cursor] != '\\') {
                continue;
            }
            if (++cursor >= value.end) {
                return false;
            }
            const char escaped = text_[cursor];
            if (escaped == 'b' || escaped == 'f' || escaped == 'n' ||
                escaped == 'r' || escaped == 't') {
                return false;
            }
            if (escaped == 'u') {
                if (cursor + 4U >= value.end) {
                    return false;
                }
                unsigned codepoint = 0;
                for (unsigned digit = 0; digit < 4U; ++digit) {
                    const char character = text_[++cursor];
                    codepoint <<= 4U;
                    if (character >= '0' && character <= '9') {
                        codepoint |= static_cast<unsigned>(character - '0');
                    } else if (character >= 'a' && character <= 'f') {
                        codepoint |= static_cast<unsigned>(character - 'a' + 10);
                    } else if (character >= 'A' && character <= 'F') {
                        codepoint |= static_cast<unsigned>(character - 'A' + 10);
                    } else {
                        return false;
                    }
                }
                if (codepoint < 0x20U || codepoint == 0x7fU) {
                    return false;
                }
            }
        }
        return true;
    }

    const char *text() const { return text_; }

private:
    const char *text_;
    const Node *nodes_;
};

bool Parser::duplicate_key(size_t object_index, size_t candidate) const
{
    const Document document(document_, nodes_);
    char candidate_text[65]{};
    if (!document.decode_ascii(candidate, candidate_text, sizeof(candidate_text))) {
        return true;
    }
    for (size_t key = nodes_[object_index].first_child;
         key != static_cast<size_t>(kNoNode); key = nodes_[key].next_sibling) {
        char existing[65]{};
        if (!document.decode_ascii(key, existing, sizeof(existing)) ||
            std::strcmp(candidate_text, existing) == 0) {
            return true;
        }
    }
    return false;
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
    const unsigned day_of_year = (153U * adjusted_month + 2U) / 5U + day - 1U;
    const unsigned day_of_era =
        year_of_era * 365U + year_of_era / 4U - year_of_era / 100U + day_of_year;
    return static_cast<int64_t>(era) * 146'097 +
           static_cast<int64_t>(day_of_era) - 719'468;
}

bool parse_canonical_utc(const Document &document, size_t node_index, uint64_t *unix_ms)
{
    char value[31]{};
    if (!document.decode_ascii(node_index, value, sizeof(value))) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size < 20U || size > 30U || value[4] != '-' || value[7] != '-' ||
        value[10] != 'T' || value[13] != ':' || value[16] != ':' ||
        value[size - 1U] != 'Z') {
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
        !parse_digits(value + 14, 2, &minute) || !parse_digits(value + 17, 2, &second) ||
        year < 1970U || month == 0U || month > 12U || day == 0U ||
        day > month_days(year, month) || hour > 23U || minute > 59U || second > 59U) {
        return false;
    }
    uint64_t fraction_ns = 0;
    if (size > 20U) {
        const size_t digits = size - 21U;
        if (value[19] != '.' || digits == 0U || digits > 9U || value[size - 2U] == '0') {
            return false;
        }
        for (size_t index = 0; index < digits; ++index) {
            if (value[20U + index] < '0' || value[20U + index] > '9') {
                return false;
            }
            fraction_ns = fraction_ns * 10U +
                          static_cast<uint64_t>(value[20U + index] - '0');
        }
        for (size_t index = digits; index < 9U; ++index) {
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

bool is_null(const Document &document, size_t index)
{
    return index != static_cast<size_t>(kNoNode) &&
           document.node(index).type == NodeType::null_value;
}

bool boolean_value(const Document &document, size_t index, bool *output)
{
    if (index == static_cast<size_t>(kNoNode) ||
        document.node(index).type != NodeType::boolean) {
        return false;
    }
    *output = document.text()[document.node(index).start] == 't';
    return true;
}

bool unsigned_between(
    const Document &document,
    size_t index,
    uint64_t minimum,
    uint64_t maximum,
    uint64_t *output = nullptr
)
{
    uint64_t value = 0;
    if (index == static_cast<size_t>(kNoNode) ||
        !document.unsigned_integer(index, &value) || value < minimum || value > maximum) {
        return false;
    }
    if (output != nullptr) {
        *output = value;
    }
    return true;
}

bool safe_identifier(const Document &document, size_t index, size_t maximum)
{
    char value[65]{};
    if (maximum >= sizeof(value) || !document.decode_ascii(index, value, sizeof(value))) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size == 0U || size > maximum || value[0] < 'a' || value[0] > 'z') {
        return false;
    }
    for (size_t position = 1; position < size; ++position) {
        const char character = value[position];
        if (!((character >= 'a' && character <= 'z') ||
              (character >= '0' && character <= '9') || character == '_' ||
              character == '-')) {
            return false;
        }
    }
    return true;
}

bool safe_opaque_id(const Document &document, size_t index)
{
    char value[65]{};
    if (!document.decode_ascii(index, value, sizeof(value))) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size < 8U || size > 64U) {
        return false;
    }
    for (size_t position = 0; position < size; ++position) {
        const char character = value[position];
        if (!((character >= 'a' && character <= 'z') ||
              (character >= 'A' && character <= 'Z') ||
              (character >= '0' && character <= '9') || character == '_' ||
              character == '-')) {
            return false;
        }
    }
    return true;
}

bool one_of(const Document &document, size_t index, const char *const *values, size_t count)
{
    for (size_t candidate = 0; candidate < count; ++candidate) {
        if (document.string_equals(index, values[candidate])) {
            return true;
        }
    }
    return false;
}

bool key_is_private(const Document &document, size_t key)
{
    constexpr const char *kPrivate[] = {
        "absolutepath", "accesstoken", "apikey", "attributes", "command",
        "commandoutput", "credential", "credentials", "path", "prompt", "raw",
        "rawresponse", "refreshtoken", "reply", "response", "toolarguments",
        "toolargs", "toolparams", "upstream",
    };
    char decoded[65]{};
    if (!document.decode_ascii(key, decoded, sizeof(decoded))) {
        return true;
    }
    char normalized[65]{};
    size_t written = 0;
    for (size_t index = 0; decoded[index] != '\0'; ++index) {
        char character = decoded[index];
        if (character >= 'A' && character <= 'Z') {
            character = static_cast<char>(character + ('a' - 'A'));
        }
        if ((character >= 'a' && character <= 'z') ||
            (character >= '0' && character <= '9')) {
            normalized[written++] = character;
        }
    }
    for (const char *private_name : kPrivate) {
        if (std::strcmp(normalized, private_name) == 0) {
            return true;
        }
    }
    return false;
}

bool string_starts_absolute_path(const Document &document, size_t index)
{
    const Node &value = document.node(index);
    if (value.type != NodeType::string) {
        return false;
    }
    char decoded[4]{};
    size_t written = 0;
    size_t cursor = value.start;
    while (cursor < value.end && written < 3U) {
        uint8_t byte = static_cast<uint8_t>(document.text()[cursor++]);
        if (byte == '\\') {
            if (cursor == value.end) {
                return false;
            }
            const char escaped = document.text()[cursor++];
            if (escaped == '/' || escaped == '\\') {
                byte = static_cast<uint8_t>(escaped);
            } else if (escaped == 'u' && cursor + 4U <= value.end) {
                unsigned codepoint = 0;
                for (unsigned digit = 0; digit < 4U; ++digit) {
                    const char character = document.text()[cursor++];
                    codepoint <<= 4U;
                    if (character >= '0' && character <= '9') {
                        codepoint |= static_cast<unsigned>(character - '0');
                    } else if (character >= 'a' && character <= 'f') {
                        codepoint |= static_cast<unsigned>(character - 'a' + 10);
                    } else if (character >= 'A' && character <= 'F') {
                        codepoint |= static_cast<unsigned>(character - 'A' + 10);
                    } else {
                        return false;
                    }
                }
                if (codepoint > 0x7fU) {
                    return false;
                }
                byte = static_cast<uint8_t>(codepoint);
            } else {
                return false;
            }
        }
        if (byte > 0x7fU) {
            return false;
        }
        decoded[written++] = static_cast<char>(byte);
    }
    return decoded[0] == '/' || (decoded[0] == '~' && decoded[1] == '/') ||
           (decoded[0] == '\\' && decoded[1] == '\\') ||
           (((decoded[0] >= 'A' && decoded[0] <= 'Z') ||
             (decoded[0] >= 'a' && decoded[0] <= 'z')) &&
            decoded[1] == ':' && (decoded[2] == '\\' || decoded[2] == '/'));
}

bool contains_private_content(const Document &document, size_t index)
{
    const Node &node = document.node(index);
    if (node.type == NodeType::string && string_starts_absolute_path(document, index)) {
        return true;
    }
    if (node.type == NodeType::object) {
        for (size_t key = node.first_child; key != static_cast<size_t>(kNoNode);
             key = document.node(key).next_sibling) {
            if (key_is_private(document, key) ||
                contains_private_content(document, document.node(key).first_child)) {
                return true;
            }
        }
    } else if (node.type == NodeType::array) {
        for (size_t child = node.first_child; child != static_cast<size_t>(kNoNode);
             child = document.node(child).next_sibling) {
            if (contains_private_content(document, child)) {
                return true;
            }
        }
    }
    return false;
}

bool safe_forward_scalar(const Document &document, size_t index)
{
    const Node &node = document.node(index);
    if (node.type == NodeType::null_value || node.type == NodeType::boolean) {
        return true;
    }
    if (node.type != NodeType::integer) {
        return false;
    }
    const bool negative = document.text()[node.start] == '-';
    size_t cursor = node.start + (negative ? 1U : 0U);
    uint64_t magnitude = 0;
    for (; cursor < node.end; ++cursor) {
        const uint64_t digit = static_cast<uint64_t>(document.text()[cursor] - '0');
        if (magnitude > (kMaximumSafeInteger - digit) / 10U) {
            return false;
        }
        magnitude = magnitude * 10U + digit;
    }
    return magnitude <= kMaximumSafeInteger;
}

deck_ai_snapshot_result_t object_fields(
    const Document &document,
    size_t object_index,
    uint16_t minor,
    const char *const *known,
    size_t known_count
)
{
    if (document.node(object_index).type != NodeType::object) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    for (size_t key = document.node(object_index).first_child;
         key != static_cast<size_t>(kNoNode); key = document.node(key).next_sibling) {
        if (key_is_private(document, key)) {
            return DECK_AI_SNAPSHOT_PRIVATE_DATA;
        }
        if (one_of(document, key, known, known_count)) {
            continue;
        }
        if (minor == DECK_AI_SNAPSHOT_SCHEMA_MINOR) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
        if (!safe_forward_scalar(document, document.node(key).first_child)) {
            return DECK_AI_SNAPSHOT_PRIVATE_DATA;
        }
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t parse_version(
    const Document &document,
    size_t object_index,
    uint16_t *minor
)
{
    if (object_index == static_cast<size_t>(kNoNode)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    constexpr const char *kFields[] = {"major", "minor"};
    const deck_ai_snapshot_result_t fields_result =
        object_fields(document, object_index, 0, kFields, 2);
    if (fields_result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return fields_result;
    }
    const size_t major_node = document.field(object_index, "major");
    const size_t minor_node = document.field(object_index, "minor");
    uint64_t major = 0;
    uint64_t minor_value = 0;
    if (!unsigned_between(document, major_node, 0, UINT16_MAX, &major) ||
        !unsigned_between(document, minor_node, 0, UINT16_MAX, &minor_value)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    if (major != DECK_AI_SNAPSHOT_SCHEMA_MAJOR) {
        return DECK_AI_SNAPSHOT_UNSUPPORTED_VERSION;
    }
    *minor = static_cast<uint16_t>(minor_value);
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

bool required(const Document &document, size_t object, const char *name, bool nullable = false)
{
    const size_t field = document.field(object, name);
    return field != static_cast<size_t>(kNoNode) && (nullable || !is_null(document, field));
}

bool nullable_unsigned(
    const Document &document,
    size_t index,
    uint64_t minimum,
    uint64_t maximum,
    uint64_t *output = nullptr,
    bool *present = nullptr
)
{
    if (index == static_cast<size_t>(kNoNode)) {
        return false;
    }
    if (is_null(document, index)) {
        if (present != nullptr) {
            *present = false;
        }
        return true;
    }
    if (!unsigned_between(document, index, minimum, maximum, output)) {
        return false;
    }
    if (present != nullptr) {
        *present = true;
    }
    return true;
}

bool nullable_time(
    const Document &document,
    size_t index,
    uint64_t maximum,
    uint64_t *output = nullptr,
    bool *present = nullptr
)
{
    if (index == static_cast<size_t>(kNoNode)) {
        return false;
    }
    if (is_null(document, index)) {
        if (present != nullptr) {
            *present = false;
        }
        return true;
    }
    uint64_t parsed = 0;
    if (!parse_canonical_utc(document, index, &parsed) || parsed > maximum) {
        return false;
    }
    if (output != nullptr) {
        *output = parsed;
    }
    if (present != nullptr) {
        *present = true;
    }
    return true;
}

deck_ai_snapshot_result_t validate_money(
    const Document &document,
    size_t index,
    uint16_t minor
)
{
    if (is_null(document, index)) {
        return DECK_AI_SNAPSHOT_ACCEPTED;
    }
    constexpr const char *kFields[] = {"amount_micros", "currency"};
    const deck_ai_snapshot_result_t fields = object_fields(document, index, minor, kFields, 2);
    const size_t amount = document.field(index, "amount_micros");
    const size_t currency = document.field(index, "currency");
    if (fields != DECK_AI_SNAPSHOT_ACCEPTED || !required(document, index, "amount_micros") ||
        !required(document, index, "currency") ||
        !unsigned_between(document, amount, 0, kMaximumSafeInteger)) {
        return fields == DECK_AI_SNAPSHOT_PRIVATE_DATA ? fields : DECK_AI_SNAPSHOT_MALFORMED;
    }
    char currency_text[4]{};
    if (!document.decode_ascii(currency, currency_text, sizeof(currency_text)) ||
        std::strlen(currency_text) != 3U) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    for (char character : currency_text) {
        if (character == '\0') {
            break;
        }
        if (character < 'A' || character > 'Z') {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_window(
    const Document &document,
    size_t index,
    uint16_t minor
)
{
    constexpr const char *kFields[] = {
        "name", "used_basis_points", "remaining_basis_points", "window_minutes", "resets_at",
    };
    const deck_ai_snapshot_result_t fields = object_fields(document, index, minor, kFields, 5);
    if (fields != DECK_AI_SNAPSHOT_ACCEPTED) {
        return fields;
    }
    for (const char *field : kFields) {
        if (!required(document, index, field, std::strcmp(field, "name") != 0)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    if (!safe_identifier(document, document.field(index, "name"), 24)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    uint64_t used = 0;
    uint64_t remaining = 0;
    bool has_used = false;
    bool has_remaining = false;
    if (!nullable_unsigned(document, document.field(index, "used_basis_points"), 0, 10'000,
                           &used, &has_used) ||
        !nullable_unsigned(document, document.field(index, "remaining_basis_points"), 0, 10'000,
                           &remaining, &has_remaining) ||
        !nullable_unsigned(document, document.field(index, "window_minutes"), 1, 525'600) ||
        !nullable_time(document, document.field(index, "resets_at"), UINT64_MAX)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    if (has_used && has_remaining && used + remaining != 10'000U) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_tokens(
    const Document &document,
    size_t index,
    uint16_t minor
)
{
    if (is_null(document, index)) {
        return DECK_AI_SNAPSHOT_ACCEPTED;
    }
    constexpr const char *kFields[] = {"input", "cached_input", "output", "reasoning", "total"};
    const deck_ai_snapshot_result_t fields = object_fields(document, index, minor, kFields, 5);
    if (fields != DECK_AI_SNAPSHOT_ACCEPTED) {
        return fields;
    }
    uint64_t values[5]{};
    bool present[5]{};
    for (size_t field = 0; field < 5U; ++field) {
        const size_t child = document.field(index, kFields[field]);
        if (child == static_cast<size_t>(kNoNode) ||
            !nullable_unsigned(document, child, 0, kMaximumSafeInteger,
                               &values[field], &present[field])) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    if (present[0] && present[1] && values[1] > values[0]) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    if (present[0] && present[2] && present[3] && present[4] &&
        values[0] + values[2] + values[3] != values[4]) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_provider_error(
    const Document &document,
    size_t index,
    uint16_t minor
)
{
    if (is_null(document, index)) {
        return DECK_AI_SNAPSHOT_ACCEPTED;
    }
    constexpr const char *kFields[] = {"code", "retryable"};
    constexpr const char *kCodes[] = {
        "auth_stale", "permission_denied", "timeout", "process_exited",
        "schema_changed", "unavailable",
    };
    const deck_ai_snapshot_result_t fields = object_fields(document, index, minor, kFields, 2);
    bool retryable = false;
    if (fields != DECK_AI_SNAPSHOT_ACCEPTED || !required(document, index, "code") ||
        !required(document, index, "retryable") ||
        !one_of(document, document.field(index, "code"), kCodes, 6) ||
        !boolean_value(document, document.field(index, "retryable"), &retryable)) {
        return fields == DECK_AI_SNAPSHOT_PRIVATE_DATA ? fields : DECK_AI_SNAPSHOT_MALFORMED;
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_provider(
    const Document &document,
    size_t index,
    uint64_t generated_at,
    size_t *provider_id
)
{
    constexpr const char *kFields[] = {
        "schema_version", "provider_id", "display_name", "status", "source", "confidence",
        "experimental", "updated_at", "stale_after_seconds", "balance", "windows", "tokens",
        "error",
    };
    const size_t version_index = document.field(index, "schema_version");
    uint16_t minor = 0;
    deck_ai_snapshot_result_t result = parse_version(document, version_index, &minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    result = object_fields(document, index, minor, kFields, 13);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    for (const char *field : kFields) {
        const bool nullable = std::strcmp(field, "updated_at") == 0 ||
                              std::strcmp(field, "stale_after_seconds") == 0 ||
                              std::strcmp(field, "balance") == 0 ||
                              std::strcmp(field, "tokens") == 0 ||
                              std::strcmp(field, "error") == 0;
        if (!required(document, index, field, nullable)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    const size_t id = document.field(index, "provider_id");
    const size_t display_name = document.field(index, "display_name");
    const size_t status = document.field(index, "status");
    const size_t source = document.field(index, "source");
    const size_t confidence = document.field(index, "confidence");
    constexpr const char *kStatuses[] = {"ok", "degraded", "unavailable"};
    constexpr const char *kSources[] = {"codex_app_server", "cursor_local", "structured_http", "none"};
    constexpr const char *kConfidences[] = {"verified", "inferred", "unavailable"};
    bool experimental = false;
    if (!safe_identifier(document, id, 32) || !document.safe_text(display_name, 48) ||
        !one_of(document, status, kStatuses, 3) || !one_of(document, source, kSources, 4) ||
        !one_of(document, confidence, kConfidences, 3) ||
        !boolean_value(document, document.field(index, "experimental"), &experimental) ||
        !nullable_time(document, document.field(index, "updated_at"), generated_at) ||
        !nullable_unsigned(document, document.field(index, "stale_after_seconds"), 1, 86'400)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    const bool source_none = document.string_equals(source, "none");
    const bool unavailable_confidence = document.string_equals(confidence, "unavailable");
    const bool status_unavailable = document.string_equals(status, "unavailable");
    if (source_none != unavailable_confidence || (status_unavailable && !source_none) ||
        (document.string_equals(source, "codex_app_server") &&
         !document.string_equals(confidence, "verified")) ||
        (document.string_equals(source, "cursor_local") &&
         !document.string_equals(confidence, "inferred"))) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    result = validate_money(document, document.field(index, "balance"), minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    const size_t windows = document.field(index, "windows");
    if (document.node(windows).type != NodeType::array ||
        document.array_count(windows) > DECK_AI_SNAPSHOT_MAX_WINDOWS) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    for (size_t window = document.node(windows).first_child;
         window != static_cast<size_t>(kNoNode); window = document.node(window).next_sibling) {
        result = validate_window(document, window, minor);
        if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
            return result;
        }
    }
    result = validate_tokens(document, document.field(index, "tokens"), minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    const size_t error = document.field(index, "error");
    result = validate_provider_error(document, error, minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED ||
        (document.string_equals(status, "ok") && !is_null(document, error)) ||
        (!document.string_equals(status, "ok") && is_null(document, error))) {
        return result == DECK_AI_SNAPSHOT_PRIVATE_DATA ? result : DECK_AI_SNAPSHOT_MALFORMED;
    }
    *provider_id = id;
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_session(
    const Document &document,
    size_t index,
    uint64_t generated_at,
    const size_t *provider_ids,
    size_t provider_count
)
{
    constexpr const char *kFields[] = {
        "schema_version", "session_id", "provider_id", "display_name", "state", "source",
        "confidence", "started_at", "last_activity_at", "duration_seconds", "turn_tokens",
        "context_used_basis_points",
    };
    uint16_t minor = 0;
    deck_ai_snapshot_result_t result =
        parse_version(document, document.field(index, "schema_version"), &minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    result = object_fields(document, index, minor, kFields, 12);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    for (const char *field : kFields) {
        const bool nullable = std::strcmp(field, "display_name") == 0 ||
                              std::strcmp(field, "started_at") == 0 ||
                              std::strcmp(field, "last_activity_at") == 0 ||
                              std::strcmp(field, "duration_seconds") == 0 ||
                              std::strcmp(field, "turn_tokens") == 0 ||
                              std::strcmp(field, "context_used_basis_points") == 0;
        if (!required(document, index, field, nullable)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    const size_t session_id = document.field(index, "session_id");
    const size_t provider_id = document.field(index, "provider_id");
    const size_t display_name = document.field(index, "display_name");
    const size_t state = document.field(index, "state");
    const size_t source = document.field(index, "source");
    const size_t confidence = document.field(index, "confidence");
    constexpr const char *kStates[] = {
        "running", "waiting_approval", "waiting_input", "completed", "failed",
        "recent", "ended", "unknown", "unavailable",
    };
    constexpr const char *kSources[] = {
        "codex_app_server_owned", "process_jsonl_observer", "none",
    };
    constexpr const char *kConfidences[] = {"verified", "inferred", "unavailable"};
    if (!safe_opaque_id(document, session_id) || !safe_identifier(document, provider_id, 32) ||
        (!is_null(document, display_name) && !document.safe_text(display_name, 48)) ||
        !one_of(document, state, kStates, 9) || !one_of(document, source, kSources, 3) ||
        !one_of(document, confidence, kConfidences, 3)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    bool provider_exists = false;
    for (size_t provider = 0; provider < provider_count; ++provider) {
        char wanted[33]{};
        char actual[33]{};
        if (document.decode_ascii(provider_id, wanted, sizeof(wanted)) &&
            document.decode_ascii(provider_ids[provider], actual, sizeof(actual)) &&
            std::strcmp(wanted, actual) == 0) {
            provider_exists = true;
            break;
        }
    }
    if (!provider_exists) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    const bool source_none = document.string_equals(source, "none");
    const bool unavailable_confidence = document.string_equals(confidence, "unavailable");
    if (source_none != unavailable_confidence ||
        (source_none && !document.string_equals(state, "unavailable")) ||
        (document.string_equals(source, "codex_app_server_owned") &&
         !document.string_equals(confidence, "verified")) ||
        (document.string_equals(source, "process_jsonl_observer") &&
         !document.string_equals(confidence, "inferred"))) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    if (document.string_equals(source, "codex_app_server_owned")) {
        constexpr const char *kOwnedStates[] = {
            "running", "waiting_approval", "waiting_input", "completed", "failed",
        };
        if (!one_of(document, state, kOwnedStates, 5)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    if (document.string_equals(source, "process_jsonl_observer")) {
        constexpr const char *kObservedStates[] = {"running", "recent", "ended", "unknown"};
        if (!one_of(document, state, kObservedStates, 4)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    uint64_t started_at = 0;
    uint64_t last_activity_at = 0;
    bool has_started = false;
    bool has_last_activity = false;
    if (!nullable_time(document, document.field(index, "started_at"), generated_at,
                       &started_at, &has_started) ||
        !nullable_time(document, document.field(index, "last_activity_at"), generated_at,
                       &last_activity_at, &has_last_activity) ||
        !nullable_unsigned(document, document.field(index, "duration_seconds"), 0, 31'536'000) ||
        !nullable_unsigned(document, document.field(index, "turn_tokens"), 0,
                           kMaximumSafeInteger) ||
        !nullable_unsigned(document, document.field(index, "context_used_basis_points"), 0,
                           10'000) ||
        (has_started && has_last_activity && last_activity_at < started_at)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

deck_ai_snapshot_result_t validate_snapshot(
    const Document &document,
    size_t root,
    deck_ai_snapshot_metadata_t *metadata
)
{
    constexpr const char *kFields[] = {
        "type", "protocol_version", "schema_version", "generated_at", "timezone",
        "provider_order", "providers", "sessions", "next_refresh_seconds",
    };
    if (document.node(root).type != NodeType::object) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    for (const char *field : kFields) {
        if (!required(document, root, field, std::strcmp(field, "timezone") == 0)) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
    }
    uint16_t minor = 0;
    deck_ai_snapshot_result_t result =
        parse_version(document, document.field(root, "schema_version"), &minor);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    result = object_fields(document, root, minor, kFields, 9);
    if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
        return result;
    }
    uint64_t protocol_version = 0;
    uint64_t generated_at = 0;
    uint64_t next_refresh = 0;
    const size_t timezone = document.field(root, "timezone");
    if (!document.string_equals(document.field(root, "type"), "snapshot.ai") ||
        !unsigned_between(document, document.field(root, "protocol_version"), 1, 1,
                          &protocol_version) ||
        !parse_canonical_utc(document, document.field(root, "generated_at"), &generated_at) ||
        (!is_null(document, timezone) && !document.safe_text(timezone, 64)) ||
        !unsigned_between(document, document.field(root, "next_refresh_seconds"), 1, 3'600,
                          &next_refresh)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    const size_t provider_order = document.field(root, "provider_order");
    const size_t providers = document.field(root, "providers");
    const size_t sessions = document.field(root, "sessions");
    if (document.node(provider_order).type != NodeType::array ||
        document.node(providers).type != NodeType::array ||
        document.node(sessions).type != NodeType::array) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    const size_t provider_count = document.array_count(providers);
    const size_t order_count = document.array_count(provider_order);
    const size_t session_count = document.array_count(sessions);
    if (provider_count > DECK_AI_SNAPSHOT_MAX_PROVIDERS || order_count != provider_count ||
        session_count > DECK_AI_SNAPSHOT_MAX_SESSIONS) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    size_t provider_ids[DECK_AI_SNAPSHOT_MAX_PROVIDERS]{};
    for (size_t provider = 0; provider < provider_count; ++provider) {
        const size_t provider_node = document.array_at(providers, provider);
        result = validate_provider(document, provider_node, generated_at, &provider_ids[provider]);
        if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
            return result;
        }
        const size_t ordered_id = document.array_at(provider_order, provider);
        char actual[33]{};
        char ordered[33]{};
        if (!document.decode_ascii(provider_ids[provider], actual, sizeof(actual)) ||
            !document.decode_ascii(ordered_id, ordered, sizeof(ordered)) ||
            std::strcmp(actual, ordered) != 0) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
        for (size_t previous = 0; previous < provider; ++previous) {
            char previous_id[33]{};
            if (!document.decode_ascii(provider_ids[previous], previous_id, sizeof(previous_id)) ||
                std::strcmp(actual, previous_id) == 0) {
                return DECK_AI_SNAPSHOT_MALFORMED;
            }
        }
    }
    size_t session_ids[DECK_AI_SNAPSHOT_MAX_SESSIONS]{};
    for (size_t session = 0; session < session_count; ++session) {
        const size_t session_node = document.array_at(sessions, session);
        result = validate_session(document, session_node, generated_at, provider_ids, provider_count);
        if (result != DECK_AI_SNAPSHOT_ACCEPTED) {
            return result;
        }
        session_ids[session] = document.field(session_node, "session_id");
        char actual[65]{};
        if (!document.decode_ascii(session_ids[session], actual, sizeof(actual))) {
            return DECK_AI_SNAPSHOT_MALFORMED;
        }
        for (size_t previous = 0; previous < session; ++previous) {
            char previous_id[65]{};
            if (!document.decode_ascii(session_ids[previous], previous_id, sizeof(previous_id)) ||
                std::strcmp(actual, previous_id) == 0) {
                return DECK_AI_SNAPSHOT_MALFORMED;
            }
        }
    }
    if (metadata != nullptr) {
        metadata->schema_minor = minor;
        metadata->generated_at_unix_ms = generated_at;
        metadata->provider_count = static_cast<uint8_t>(provider_count);
        metadata->session_count = static_cast<uint8_t>(session_count);
        metadata->next_refresh_seconds = static_cast<uint32_t>(next_refresh);
    }
    return DECK_AI_SNAPSHOT_ACCEPTED;
}

}  // namespace

deck_ai_snapshot_result_t deck_ai_snapshot_validate(
    const char *document,
    size_t document_size,
    deck_ai_snapshot_metadata_t *metadata
)
{
    if (metadata != nullptr) {
        *metadata = deck_ai_snapshot_metadata_t{};
    }
    if (document == nullptr || document_size == 0U ||
        document_size > DECK_AI_SNAPSHOT_MAX_BYTES || !valid_utf8(document, document_size)) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    Node *nodes = new (std::nothrow) Node[kMaximumNodes];
    if (nodes == nullptr) {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    Parser parser(document, document_size, nodes, kMaximumNodes);
    size_t root = 0;
    deck_ai_snapshot_result_t result = DECK_AI_SNAPSHOT_MALFORMED;
    if (parser.parse(&root) && parser.count() != 0U) {
        const Document parsed(document, nodes);
        result = contains_private_content(parsed, root)
                     ? DECK_AI_SNAPSHOT_PRIVATE_DATA
                     : validate_snapshot(parsed, root, metadata);
    }
    delete[] nodes;
    return result;
}
