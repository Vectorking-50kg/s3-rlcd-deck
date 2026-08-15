#include "deck_ota_protocol.h"

#include <cstdio>
#include <cstring>

#include "cJSON.h"
#include "mbedtls/base64.h"

namespace {

constexpr uint32_t kProtocolVersion = 1;

void secure_clear(void *value, size_t size)
{
    auto *bytes = static_cast<volatile uint8_t *>(value);
    while (size != 0) {
        *bytes++ = 0;
        --size;
    }
}

bool child_count(const cJSON *object, size_t expected)
{
    size_t count = 0;
    for (const cJSON *child = object == nullptr ? nullptr : object->child;
         child != nullptr;
         child = child->next) {
        ++count;
    }
    return count == expected;
}

const char *json_string(const cJSON *object, const char *name)
{
    const cJSON *value = cJSON_GetObjectItemCaseSensitive(object, name);
    return cJSON_IsString(value) && value->valuestring != nullptr
               ? value->valuestring
               : nullptr;
}

bool json_u32(const cJSON *object, const char *name, uint32_t *output)
{
    const cJSON *value = cJSON_GetObjectItemCaseSensitive(object, name);
    if (!cJSON_IsNumber(value) || output == nullptr || value->valuedouble < 0.0 ||
        value->valuedouble > static_cast<double>(UINT32_MAX) ||
        value->valuedouble !=
            static_cast<double>(static_cast<uint32_t>(value->valuedouble))) {
        return false;
    }
    *output = static_cast<uint32_t>(value->valuedouble);
    return true;
}

bool lowercase_hex(const char *value, size_t length)
{
    if (value == nullptr || std::strlen(value) != length) {
        return false;
    }
    for (size_t index = 0; index < length; ++index) {
        if (!((value[index] >= '0' && value[index] <= '9') ||
              (value[index] >= 'a' && value[index] <= 'f'))) {
            return false;
        }
    }
    return true;
}

bool decode_hex(const char *value, uint8_t *output, size_t output_size)
{
    if (!lowercase_hex(value, output_size * 2U)) {
        return false;
    }
    for (size_t index = 0; index < output_size; ++index) {
        const auto nibble = [](char byte) -> uint8_t {
            return static_cast<uint8_t>(
                byte <= '9' ? byte - '0' : byte - 'a' + 10
            );
        };
        output[index] = static_cast<uint8_t>(
            (nibble(value[index * 2U]) << 4U) |
            nibble(value[index * 2U + 1U])
        );
    }
    return true;
}

bool bounded_ascii(const char *value, size_t capacity)
{
    if (value == nullptr) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size == 0 || size >= capacity) {
        return false;
    }
    for (size_t index = 0; index < size; ++index) {
        const unsigned char byte = static_cast<unsigned char>(value[index]);
        if (byte < 0x21U || byte > 0x7eU) {
            return false;
        }
    }
    return true;
}

bool parse_common(
    const cJSON *root,
    const char *wanted_type,
    char transaction_id[DECK_OTA_TRANSACTION_ID_CAPACITY]
)
{
    const char *type = json_string(root, "type");
    const char *id = json_string(root, "transaction_id");
    uint32_t protocol = 0;
    if (type == nullptr || std::strcmp(type, wanted_type) != 0 ||
        !json_u32(root, "protocol_version", &protocol) ||
        protocol != kProtocolVersion || !lowercase_hex(id, 32)) {
        return false;
    }
    std::memcpy(transaction_id, id, DECK_OTA_TRANSACTION_ID_CAPACITY);
    return true;
}

bool parse_offer(const cJSON *root, deck_ota_protocol_command_t *command)
{
    if (!child_count(root, 10) ||
        !parse_common(root, "ota.offer", command->transaction_id)) {
        return false;
    }
    const char *version = json_string(root, "version");
    const char *board = json_string(root, "board");
    const char *digest = json_string(root, "image_sha256");
    const char *signature = json_string(root, "signature");
    if (!bounded_ascii(version, sizeof(command->manifest.version)) ||
        !bounded_ascii(board, sizeof(command->manifest.board)) ||
        !json_u32(root, "image_length", &command->manifest.image_length) ||
        !json_u32(root, "signing_key_id", &command->manifest.signing_key_id) ||
        !json_u32(
            root,
            "minimum_protocol_version",
            &command->manifest.minimum_protocol_version
        ) ||
        !decode_hex(
            digest,
            command->manifest.image_sha256,
            sizeof(command->manifest.image_sha256)
        )) {
        return false;
    }
    std::strcpy(command->manifest.version, version);
    std::strcpy(command->manifest.board, board);
    size_t signature_size = 0;
    return signature != nullptr && std::strlen(signature) <= 128 &&
           mbedtls_base64_decode(
               command->manifest.signature,
               sizeof(command->manifest.signature),
               &signature_size,
               reinterpret_cast<const uint8_t *>(signature),
               std::strlen(signature)
           ) == 0 &&
           signature_size == sizeof(command->manifest.signature);
}

bool parse_chunk(const cJSON *root, deck_ota_protocol_command_t *command)
{
    if (!child_count(root, 6) ||
        !parse_common(root, "ota.chunk", command->transaction_id) ||
        !json_u32(root, "offset", &command->offset)) {
        return false;
    }
    const cJSON *final = cJSON_GetObjectItemCaseSensitive(root, "final");
    const char *data = json_string(root, "data");
    if (!cJSON_IsBool(final) || data == nullptr || std::strlen(data) > 4'096) {
        return false;
    }
    command->final = cJSON_IsTrue(final);
    return mbedtls_base64_decode(
               command->data,
               sizeof(command->data),
               &command->data_size,
               reinterpret_cast<const uint8_t *>(data),
               std::strlen(data)
           ) == 0 &&
           command->data_size != 0;
}

const char *state_name(deck_ota_state_t state)
{
    switch (state) {
        case DECK_OTA_RECEIVING:
            return "receiving";
        case DECK_OTA_READY_TO_REBOOT:
            return "ready_to_reboot";
        case DECK_OTA_FAILED:
            return "failed";
        case DECK_OTA_IDLE:
        default:
            return "idle";
    }
}

const char *result_name(deck_ota_result_t result)
{
    constexpr const char *kNames[] = {
        "ok", "invalid_manifest", "wrong_board", "incompatible_protocol",
        "downgrade_rejected", "image_too_large", "signature_rejected",
        "busy", "stale_offset", "flash_failure", "hash_mismatch",
        "image_invalid", "timed_out",
    };
    const size_t index = static_cast<size_t>(result);
    return index < sizeof(kNames) / sizeof(kNames[0]) ? kNames[index]
                                                      : "invalid_manifest";
}

}  // namespace

bool deck_ota_protocol_parse(
    const char *message,
    size_t message_size,
    deck_ota_protocol_command_t *command
)
{
    if (message == nullptr || message_size == 0 || message_size > 16U * 1'024U ||
        command == nullptr) {
        return false;
    }
    deck_ota_protocol_command_t parsed{};
    const char *end = nullptr;
    cJSON *root = cJSON_ParseWithLengthOpts(message, message_size, &end, false);
    if (!cJSON_IsObject(root) || end != message + message_size) {
        cJSON_Delete(root);
        return false;
    }
    const char *type = json_string(root, "type");
    bool valid = false;
    if (type != nullptr && std::strcmp(type, "ota.offer") == 0) {
        parsed.kind = DECK_OTA_PROTOCOL_OFFER;
        valid = parse_offer(root, &parsed);
    } else if (type != nullptr && std::strcmp(type, "ota.chunk") == 0) {
        parsed.kind = DECK_OTA_PROTOCOL_CHUNK;
        valid = parse_chunk(root, &parsed);
    }
    cJSON_Delete(root);
    if (!valid) {
        deck_ota_protocol_command_clear(&parsed);
        return false;
    }
    *command = parsed;
    return true;
}

bool deck_ota_protocol_format_result(
    const deck_ota_service_result_t *result,
    char *output,
    size_t capacity,
    size_t *size
)
{
    if (result == nullptr || output == nullptr || size == nullptr ||
        !lowercase_hex(result->transaction_id, 32)) {
        return false;
    }
    const int written = std::snprintf(
        output,
        capacity,
        "{\"type\":\"ota.result\",\"protocol_version\":1,"
        "\"transaction_id\":\"%s\",\"state\":\"%s\",\"code\":\"%s\","
        "\"received_bytes\":%u,\"image_length\":%u}",
        result->transaction_id,
        state_name(result->transaction.state),
        result_name(result->transaction.result),
        static_cast<unsigned>(result->transaction.received_bytes),
        static_cast<unsigned>(result->transaction.image_length)
    );
    if (written <= 0 || static_cast<size_t>(written) >= capacity) {
        return false;
    }
    *size = static_cast<size_t>(written);
    return true;
}

void deck_ota_protocol_command_clear(deck_ota_protocol_command_t *command)
{
    if (command != nullptr) {
        secure_clear(command, sizeof(*command));
    }
}
