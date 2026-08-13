#include "deck_companion_pairing_esp.h"

#include "sdkconfig.h"

#include <cstdio>
#include <cstring>
#include <memory>
#include <new>

#include "cJSON.h"
#include "esp_http_client.h"
#include "esp_mac.h"
#include "mbedtls/base64.h"
#include "psa/crypto.h"

namespace {

constexpr size_t kDigestSize = 32;
constexpr size_t kResponseCapacity = 4'096;
constexpr char kIdentityDomain[] = "s3-rlcd-deck/device-identity/v1";

struct HttpResponse {
    char bytes[kResponseCapacity];
    size_t size = 0;
    bool overflow = false;
};

void secure_clear(void *value, size_t size)
{
    auto *bytes = static_cast<volatile uint8_t *>(value);
    while (size != 0) {
        *bytes++ = 0;
        --size;
    }
}

bool constant_time_equal(const char *left, const char *right, size_t size)
{
    uint8_t difference = 0;
    for (size_t index = 0; index < size; ++index) {
        difference |= static_cast<uint8_t>(left[index] ^ right[index]);
    }
    return difference == 0;
}

bool safe_base64url(const char *value, size_t expected_size)
{
    if (value == nullptr || std::strlen(value) != expected_size) {
        return false;
    }
    for (size_t index = 0; index < expected_size; ++index) {
        const char byte = value[index];
        const bool alpha_numeric =
            (byte >= 'A' && byte <= 'Z') || (byte >= 'a' && byte <= 'z') ||
            (byte >= '0' && byte <= '9');
        if (!alpha_numeric && byte != '-' && byte != '_') {
            return false;
        }
    }
    return true;
}

bool valid_fingerprint(const char *value)
{
    constexpr char kPrefix[] = "sha256:";
    if (value == nullptr || std::strlen(value) != 71 ||
        std::memcmp(value, kPrefix, sizeof(kPrefix) - 1) != 0) {
        return false;
    }
    for (size_t index = sizeof(kPrefix) - 1; index < 71; ++index) {
        const char byte = value[index];
        if (!((byte >= '0' && byte <= '9') || (byte >= 'a' && byte <= 'f'))) {
            return false;
        }
    }
    return true;
}

bool sha256(const uint8_t *input, size_t input_size, uint8_t output[kDigestSize])
{
    size_t output_size = 0;
    return psa_crypto_init() == PSA_SUCCESS &&
           psa_hash_compute(
               PSA_ALG_SHA_256,
               input,
               input_size,
               output,
               kDigestSize,
               &output_size
           ) == PSA_SUCCESS &&
           output_size == kDigestSize;
}

bool fingerprint_for_der(
    const uint8_t *certificate,
    size_t certificate_size,
    char output[DECK_COMPANION_FINGERPRINT_CAPACITY]
)
{
    uint8_t digest[kDigestSize]{};
    if (!sha256(certificate, certificate_size, digest)) {
        return false;
    }
    constexpr char kHex[] = "0123456789abcdef";
    std::memcpy(output, "sha256:", 7);
    for (size_t index = 0; index < sizeof(digest); ++index) {
        output[7 + index * 2] = kHex[digest[index] >> 4U];
        output[8 + index * 2] = kHex[digest[index] & 0x0fU];
    }
    output[71] = '\0';
    secure_clear(digest, sizeof(digest));
    return true;
}

esp_err_t http_event(esp_http_client_event_t *event)
{
    if (event == nullptr || event->user_data == nullptr) {
        return ESP_OK;
    }
    auto *response = static_cast<HttpResponse *>(event->user_data);
    if (event->event_id != HTTP_EVENT_ON_DATA || event->data_len <= 0) {
        return ESP_OK;
    }
    const size_t chunk_size = static_cast<size_t>(event->data_len);
    if (chunk_size > sizeof(response->bytes) - response->size) {
        response->overflow = true;
        return ESP_FAIL;
    }
    std::memcpy(response->bytes + response->size, event->data, chunk_size);
    response->size += chunk_size;
    return ESP_OK;
}

bool exact_credential_object(cJSON *root)
{
    if (!cJSON_IsObject(root) || cJSON_GetArraySize(root) != 5) {
        return false;
    }
    constexpr const char *kFields[] = {
        "device_id",
        "token",
        "certificate_fingerprint",
        "certificate_der",
        "protocol_version",
    };
    for (const char *field : kFields) {
        unsigned count = 0;
        for (cJSON *item = root->child; item != nullptr; item = item->next) {
            if (item->string != nullptr && std::strcmp(item->string, field) == 0) {
                ++count;
            }
        }
        if (count != 1) {
            return false;
        }
    }
    return true;
}

bool json_tail_is_whitespace(const char *current, const char *end)
{
    while (current != nullptr && current != end) {
        if (*current != ' ' && *current != '\t' && *current != '\r' &&
            *current != '\n') {
            return false;
        }
        ++current;
    }
    return current == end;
}

bool parse_credential(
    const char *document,
    size_t document_size,
    const char *expected_device_id,
    deck_companion_pairing_credential_t *credential
)
{
    const char *parse_end = nullptr;
    cJSON *root = cJSON_ParseWithLengthOpts(
        document,
        document_size,
        &parse_end,
        false
    );
    if (root == nullptr || !json_tail_is_whitespace(
                               parse_end,
                               document + document_size
                           ) ||
        !exact_credential_object(root)) {
        cJSON_Delete(root);
        return false;
    }
    cJSON *device_id = cJSON_GetObjectItemCaseSensitive(root, "device_id");
    cJSON *token = cJSON_GetObjectItemCaseSensitive(root, "token");
    cJSON *fingerprint = cJSON_GetObjectItemCaseSensitive(
        root,
        "certificate_fingerprint"
    );
    cJSON *certificate = cJSON_GetObjectItemCaseSensitive(root, "certificate_der");
    cJSON *version = cJSON_GetObjectItemCaseSensitive(root, "protocol_version");
    const bool shape_valid =
        cJSON_IsString(device_id) && cJSON_IsString(token) &&
        cJSON_IsString(fingerprint) && cJSON_IsString(certificate) &&
        cJSON_IsNumber(version) && version->valuedouble == 1.0 &&
        device_id->valuestring != nullptr &&
        std::strcmp(device_id->valuestring, expected_device_id) == 0 &&
        safe_base64url(token->valuestring, 43) &&
        valid_fingerprint(fingerprint->valuestring) &&
        certificate->valuestring != nullptr;
    if (!shape_valid) {
        cJSON_Delete(root);
        return false;
    }

    size_t decoded_size = 0;
    const size_t encoded_size = std::strlen(certificate->valuestring);
    const int decode_result = mbedtls_base64_decode(
        credential->certificate_der,
        sizeof(credential->certificate_der),
        &decoded_size,
        reinterpret_cast<const uint8_t *>(certificate->valuestring),
        encoded_size
    );
    char calculated[DECK_COMPANION_FINGERPRINT_CAPACITY]{};
    const bool decoded = decode_result == 0 && decoded_size != 0 &&
                         decoded_size <= sizeof(credential->certificate_der) &&
                         fingerprint_for_der(
                             credential->certificate_der,
                             decoded_size,
                             calculated
                         ) &&
                         constant_time_equal(
                             calculated,
                             fingerprint->valuestring,
                             71
                         );
    if (decoded) {
        std::memcpy(credential->token, token->valuestring, 44);
        std::memcpy(
            credential->certificate_fingerprint,
            fingerprint->valuestring,
            72
        );
        credential->certificate_der_size = decoded_size;
        credential->protocol_version = 1;
    }
    secure_clear(calculated, sizeof(calculated));
    cJSON_Delete(root);
    return decoded;
}

}  // namespace

bool deck_companion_device_identity(
    char *device_id,
    size_t device_id_capacity,
    char *device_identity,
    size_t device_identity_capacity
)
{
    if (device_id == nullptr || device_id_capacity < 18 ||
        device_identity == nullptr || device_identity_capacity < 44) {
        return false;
    }
    uint8_t mac[6]{};
    if (esp_read_mac(mac, ESP_MAC_WIFI_STA) != ESP_OK) {
        return false;
    }
    const int id_size = std::snprintf(
        device_id,
        device_id_capacity,
        "deck-%02x%02x%02x%02x%02x%02x",
        mac[0],
        mac[1],
        mac[2],
        mac[3],
        mac[4],
        mac[5]
    );
    uint8_t source[sizeof(kIdentityDomain) - 1 + sizeof(mac)]{};
    std::memcpy(source, kIdentityDomain, sizeof(kIdentityDomain) - 1);
    std::memcpy(source + sizeof(kIdentityDomain) - 1, mac, sizeof(mac));
    uint8_t digest[kDigestSize]{};
    size_t encoded_size = 0;
    const bool encoded =
        id_size == 17 && sha256(source, sizeof(source), digest) &&
        mbedtls_base64_encode(
            reinterpret_cast<uint8_t *>(device_identity),
            device_identity_capacity,
            &encoded_size,
            digest,
            sizeof(digest)
        ) == 0 &&
        encoded_size == 44 && device_identity[43] == '=';
    if (encoded) {
        for (size_t index = 0; index < 43; ++index) {
            if (device_identity[index] == '+') {
                device_identity[index] = '-';
            } else if (device_identity[index] == '/') {
                device_identity[index] = '_';
            }
        }
        device_identity[43] = '\0';
    }
    secure_clear(mac, sizeof(mac));
    secure_clear(source, sizeof(source));
    secure_clear(digest, sizeof(digest));
    if (!encoded) {
        secure_clear(device_id, device_id_capacity);
        secure_clear(device_identity, device_identity_capacity);
    }
    return encoded;
}

bool deck_companion_pairing_esp_redeem(
    void *,
    const char *hub_address,
    const char *pairing_address,
    const char *pairing_code,
    deck_companion_pairing_credential_t *credential
)
{
#if !CONFIG_ESP_TLS_SKIP_SERVER_CERT_VERIFY
#error "Setup-AP Pairing bootstrap requires CONFIG_ESP_TLS_SKIP_SERVER_CERT_VERIFY"
#endif
    if (credential == nullptr || !deck_companion_hub_address_valid(hub_address) ||
        !deck_companion_hub_address_valid(pairing_address) ||
        !deck_companion_pairing_code_valid(pairing_code)) {
        return false;
    }
    *credential = {};
    char device_id[18]{};
    char device_identity[44]{};
    if (!deck_companion_device_identity(
            device_id,
            sizeof(device_id),
            device_identity,
            sizeof(device_identity)
        )) {
        return false;
    }
    char url[160]{};
    char request_body[256]{};
    const int url_size = std::snprintf(
        url,
        sizeof(url),
        "https://%s/api/v1/pairing/redeem",
        pairing_address
    );
    const int body_size = std::snprintf(
        request_body,
        sizeof(request_body),
        "{\"code\":\"%s\",\"device_id\":\"%s\",\"device_identity\":\"%s\",\"protocol_version\":1}",
        pairing_code,
        device_id,
        device_identity
    );
    std::unique_ptr<HttpResponse> response(new (std::nothrow) HttpResponse{});
    if (url_size <= 0 || static_cast<size_t>(url_size) >= sizeof(url) ||
        body_size <= 0 || static_cast<size_t>(body_size) >= sizeof(request_body) ||
        response == nullptr) {
        secure_clear(device_identity, sizeof(device_identity));
        secure_clear(request_body, sizeof(request_body));
        return false;
    }

    esp_http_client_config_t config{};
    config.url = url;
    config.method = HTTP_METHOD_POST;
    config.timeout_ms = 10'000;
    config.disable_auto_redirect = true;
    config.transport_type = HTTP_TRANSPORT_OVER_SSL;
    config.skip_cert_common_name_check = true;
    config.event_handler = http_event;
    config.user_data = response.get();
    config.buffer_size = 1'024;
    config.buffer_size_tx = 512;
    esp_http_client_handle_t client = esp_http_client_init(&config);
    const bool success = client != nullptr &&
                         esp_http_client_set_header(
                             client,
                             "Content-Type",
                             "application/json"
                         ) == ESP_OK &&
                         esp_http_client_set_post_field(
                             client,
                             request_body,
                             body_size
                         ) == ESP_OK &&
                         esp_http_client_perform(client) == ESP_OK &&
                         esp_http_client_get_status_code(client) == 200 &&
                         !response->overflow && response->size != 0 &&
                         parse_credential(
                             response->bytes,
                             response->size,
                             device_id,
                             credential
                         );
    if (client != nullptr) {
        esp_http_client_cleanup(client);
    }
    secure_clear(response.get(), sizeof(*response));
    secure_clear(request_body, sizeof(request_body));
    secure_clear(device_identity, sizeof(device_identity));
    if (!success) {
        secure_clear(credential, sizeof(*credential));
    }
    return success;
}
