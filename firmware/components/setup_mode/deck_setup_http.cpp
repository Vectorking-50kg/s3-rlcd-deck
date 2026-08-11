#include "deck_setup_http.h"

#include <cstdarg>
#include <cstdio>
#include <cstring>

namespace {

constexpr char kPage[] =
    "<!doctype html><html><head><meta charset=utf-8><meta name=viewport "
    "content='width=device-width,initial-scale=1'><title>Deck Setup / Recovery</title>"
    "<style>body{font:16px system-ui;max-width:42rem;margin:2rem auto;padding:0 1rem}"
    "button{padding:.7rem 1rem}pre{white-space:pre-wrap}</style></head><body>"
    "<h1>Deck Setup / Recovery</h1><p>Submit a network for transactional validation. "
    "The last working configuration stays active until validation succeeds.</p>"
    "<form id=wifi><label>SSID <input name=ssid maxlength=32 required></label> "
    "<label>Password <input name=password type=password maxlength=63></label> "
    "<button>Validate and activate</button></form>"
    "<button id=scan type=button>Scan networks</button>"
    "<h2>Temperature calibration</h2><form id=temp><label>Offset (C) "
    "<input name=offset type=number min=-15 max=15 step=.1 value=-4.0 required>"
    "</label> <button>Save offset</button></form>"
    "<h2>Wi-Fi recovery</h2><button id=clear type=button>Clear Wi-Fi...</button>"
    "<pre id=state>Loading...</pre>"
    "<p><strong>Companion pairing is planned for M1.</strong></p>"
    "<script>async function load(method='GET',path='/api/status'){let r=await "
    "fetch(path,{method});state.textContent=JSON.stringify(await r.json(),null,2)}"
    "scan.onclick=()=>load('POST','/api/scan');wifi.onsubmit=async e=>{e.preventDefault();"
    "let r=await fetch('/api/wifi',{method:'POST',headers:{'Content-Type':"
    "'application/x-www-form-urlencoded'},body:new URLSearchParams(new FormData(wifi))});"
    "state.textContent=JSON.stringify(await r.json(),null,2);if(r.ok)setTimeout(load,500)};"
    "temp.onsubmit=async e=>{e.preventDefault();let r=await fetch('/api/temperature',"
    "{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},"
    "body:new URLSearchParams(new FormData(temp))});state.textContent=JSON.stringify("
    "await r.json(),null,2);if(r.ok)setTimeout(load,250)};clear.onclick=async()=>{let r="
    "await fetch('/api/wifi/clear/request',{method:'POST'});let j=await r.json();if(!r.ok)"
    "{state.textContent=JSON.stringify(j,null,2);return}if(confirm('Clear saved Wi-Fi and "
    "remain in Setup Mode?')){r=await fetch('/api/wifi/clear/confirm',{method:'POST',"
    "headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams("
    "{token:j.token})});state.textContent=JSON.stringify(await r.json(),null,2);"
    "if(r.ok)setTimeout(load,250)}};"
    "load()</script></body></html>";

constexpr deck_setup_http_route_spec_t kRoutes[] = {
    {DECK_SETUP_HTTP_PAGE, DECK_SETUP_HTTP_GET, "/"},
    {DECK_SETUP_HTTP_STATUS, DECK_SETUP_HTTP_GET, "/api/status"},
    {DECK_SETUP_HTTP_SCAN, DECK_SETUP_HTTP_POST, "/api/scan"},
    {DECK_SETUP_HTTP_WIFI, DECK_SETUP_HTTP_POST, "/api/wifi"},
    {DECK_SETUP_HTTP_TEMPERATURE, DECK_SETUP_HTTP_POST, "/api/temperature"},
    {DECK_SETUP_HTTP_WIFI_CLEAR_REQUEST, DECK_SETUP_HTTP_POST, "/api/wifi/clear/request"},
    {DECK_SETUP_HTTP_WIFI_CLEAR_CONFIRM, DECK_SETUP_HTTP_POST, "/api/wifi/clear/confirm"},
};

class BufferWriter {
  public:
    BufferWriter(char *buffer, size_t capacity) : buffer_(buffer), capacity_(capacity), size_(0), ok_(true)
    {
        if (buffer_ == nullptr || capacity_ == 0) {
            ok_ = false;
        } else {
            buffer_[0] = '\0';
        }
    }

    void append(const char *format, ...)
    {
        if (!ok_) {
            return;
        }
        va_list arguments;
        va_start(arguments, format);
        const int written = std::vsnprintf(buffer_ + size_, capacity_ - size_, format, arguments);
        va_end(arguments);
        if (written < 0 || static_cast<size_t>(written) >= capacity_ - size_) {
            ok_ = false;
            buffer_[capacity_ - 1] = '\0';
            return;
        }
        size_ += static_cast<size_t>(written);
    }

    void append_json_string(const char *value)
    {
        append("\"");
        if (value != nullptr) {
            for (const unsigned char *cursor = reinterpret_cast<const unsigned char *>(value);
                 *cursor != 0; ++cursor) {
                if (*cursor == '\\' || *cursor == '\"') {
                    append("\\%c", static_cast<int>(*cursor));
                } else if (*cursor < 0x20U) {
                    append("\\u%04x", static_cast<unsigned>(*cursor));
                } else {
                    append("%c", static_cast<int>(*cursor));
                }
            }
        }
        append("\"");
    }

    bool ok() const { return ok_; }

  private:
    char *buffer_;
    size_t capacity_;
    size_t size_;
    bool ok_;
};

const char *reason_name(deck_setup_reason_t reason)
{
    switch (reason) {
        case DECK_SETUP_REASON_NO_WIFI:
            return "no_wifi_config";
        case DECK_SETUP_REASON_BOOT_LONG_PRESS:
            return "boot_long_press";
        case DECK_SETUP_REASON_NONE:
        default:
            return "none";
    }
}

int hex_value(char value)
{
    if (value >= '0' && value <= '9') {
        return value - '0';
    }
    if (value >= 'a' && value <= 'f') {
        return value - 'a' + 10;
    }
    if (value >= 'A' && value <= 'F') {
        return value - 'A' + 10;
    }
    return -1;
}

bool decode_form_component(
    const char *input,
    size_t input_size,
    char *output,
    size_t output_capacity
)
{
    if (input == nullptr || output == nullptr || output_capacity == 0) {
        return false;
    }
    size_t output_size = 0;
    for (size_t index = 0; index < input_size; ++index) {
        unsigned char byte = static_cast<unsigned char>(input[index]);
        if (byte == '%') {
            if (index + 2 >= input_size) {
                return false;
            }
            const int high = hex_value(input[index + 1]);
            const int low = hex_value(input[index + 2]);
            if (high < 0 || low < 0) {
                return false;
            }
            byte = static_cast<unsigned char>((high << 4) | low);
            index += 2;
        } else if (byte == '+') {
            byte = ' ';
        }
        if (byte < 0x20U || byte == 0x7fU || output_size + 1 >= output_capacity) {
            return false;
        }
        output[output_size++] = static_cast<char>(byte);
    }
    output[output_size] = '\0';
    return true;
}

deck_setup_temperature_request_result_t parse_decimal_tenths(
    const char *value,
    int16_t *temperature_offset_tenths_c
)
{
    if (value == nullptr || temperature_offset_tenths_c == nullptr || value[0] == '\0') {
        return DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC;
    }
    size_t index = 0;
    bool negative = false;
    if (value[index] == '+' || value[index] == '-') {
        negative = value[index] == '-';
        ++index;
    }
    if (value[index] < '0' || value[index] > '9') {
        return DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC;
    }
    int32_t integer = 0;
    bool too_large = false;
    while (value[index] >= '0' && value[index] <= '9') {
        if (integer <= 1'000) {
            integer = integer * 10 + (value[index] - '0');
        } else {
            too_large = true;
        }
        ++index;
    }
    int32_t tenth = 0;
    if (value[index] == '.') {
        ++index;
        if (value[index] < '0' || value[index] > '9') {
            return DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC;
        }
        tenth = value[index] - '0';
        ++index;
        while (value[index] >= '0' && value[index] <= '9') {
            if (value[index] != '0') {
                return DECK_SETUP_TEMPERATURE_REQUEST_NOT_EXACT_TENTH;
            }
            ++index;
        }
    }
    if (value[index] != '\0') {
        return DECK_SETUP_TEMPERATURE_REQUEST_NOT_NUMERIC;
    }
    if (too_large || integer > 15 || (integer == 15 && tenth != 0)) {
        return DECK_SETUP_TEMPERATURE_REQUEST_OUT_OF_RANGE;
    }
    const int32_t magnitude = integer * 10 + tenth;
    *temperature_offset_tenths_c = static_cast<int16_t>(negative ? -magnitude : magnitude);
    return DECK_SETUP_TEMPERATURE_REQUEST_OK;
}

}  // namespace

const deck_setup_http_route_spec_t *deck_setup_http_routes(size_t *route_count)
{
    if (route_count == nullptr) {
        return nullptr;
    }
    *route_count = sizeof(kRoutes) / sizeof(kRoutes[0]);
    return kRoutes;
}

deck_setup_http_route_t deck_setup_http_route(const char *method, const char *path)
{
    if (method == nullptr || path == nullptr) {
        return DECK_SETUP_HTTP_NOT_FOUND;
    }
    for (const deck_setup_http_route_spec_t &route : kRoutes) {
        if (std::strcmp(path, route.path) != 0) {
            continue;
        }
        const char *expected_method =
            route.method == DECK_SETUP_HTTP_GET ? "GET" : "POST";
        return std::strcmp(method, expected_method) == 0
                   ? route.route
                   : DECK_SETUP_HTTP_METHOD_NOT_ALLOWED;
    }
    return DECK_SETUP_HTTP_NOT_FOUND;
}

bool deck_setup_http_convert_scan_results(
    const deck_setup_scan_observation_t *observations,
    size_t observation_count,
    deck_setup_scan_result_t *results,
    size_t result_capacity,
    size_t *result_count
)
{
    if ((observations == nullptr && observation_count != 0) ||
        (results == nullptr && result_capacity != 0) || result_count == nullptr) {
        return false;
    }
    const size_t count = observation_count < result_capacity
                             ? observation_count
                             : result_capacity;
    for (size_t index = 0; index < count; ++index) {
        if (observations[index].ssid == nullptr && observations[index].ssid_size != 0) {
            return false;
        }
        results[index] = {};
        const size_t copy_size = observations[index].ssid_size < DECK_SETUP_SCAN_SSID_CAPACITY - 1
                                     ? observations[index].ssid_size
                                     : DECK_SETUP_SCAN_SSID_CAPACITY - 1;
        if (copy_size != 0) {
            std::memcpy(results[index].ssid, observations[index].ssid, copy_size);
        }
        results[index].ssid[copy_size] = '\0';
        results[index].rssi = observations[index].rssi;
        results[index].secure = observations[index].secure;
    }
    *result_count = count;
    return true;
}

bool deck_setup_http_render_page(char *buffer, size_t buffer_size)
{
    if (buffer == nullptr || buffer_size < sizeof(kPage)) {
        return false;
    }
    std::memcpy(buffer, kPage, sizeof(kPage));
    return true;
}

deck_setup_wifi_request_result_t deck_setup_http_parse_wifi_request(
    const char *body,
    size_t body_size,
    deck_wifi_credentials_t *credentials
)
{
    constexpr size_t kMaximumBodySize = 256;
    if (body == nullptr || credentials == nullptr || body_size == 0 ||
        body_size > kMaximumBodySize) {
        return DECK_SETUP_WIFI_REQUEST_MALFORMED;
    }
    *credentials = {};
    bool saw_ssid = false;
    bool saw_password = false;
    size_t offset = 0;
    while (offset < body_size) {
        size_t end = offset;
        while (end < body_size && body[end] != '&') {
            ++end;
        }
        size_t equals = offset;
        while (equals < end && body[equals] != '=') {
            ++equals;
        }
        if (equals == end) {
            return DECK_SETUP_WIFI_REQUEST_MALFORMED;
        }
        char key[16];
        if (!decode_form_component(body + offset, equals - offset, key, sizeof(key))) {
            return DECK_SETUP_WIFI_REQUEST_MALFORMED;
        }
        if (std::strcmp(key, "ssid") == 0) {
            if (saw_ssid || !decode_form_component(
                                body + equals + 1,
                                end - equals - 1,
                                credentials->ssid,
                                sizeof(credentials->ssid)
                            )) {
                return DECK_SETUP_WIFI_REQUEST_MALFORMED;
            }
            saw_ssid = true;
        } else if (std::strcmp(key, "password") == 0) {
            if (saw_password) {
                return DECK_SETUP_WIFI_REQUEST_MALFORMED;
            }
            if (end == equals + 1) {
                credentials->password[0] = '\0';
            } else if (!decode_form_component(
                           body + equals + 1,
                           end - equals - 1,
                           credentials->password,
                           sizeof(credentials->password)
                       )) {
                return DECK_SETUP_WIFI_REQUEST_MALFORMED;
            }
            saw_password = true;
        } else {
            return DECK_SETUP_WIFI_REQUEST_MALFORMED;
        }
        offset = end + 1;
    }
    if (!saw_ssid || !saw_password) {
        return DECK_SETUP_WIFI_REQUEST_MALFORMED;
    }
    const size_t ssid_size = std::strlen(credentials->ssid);
    if (ssid_size == 0 || ssid_size >= DECK_WIFI_SSID_CAPACITY) {
        return DECK_SETUP_WIFI_REQUEST_INVALID_SSID;
    }
    const size_t password_size = std::strlen(credentials->password);
    if (password_size != 0 && password_size < 8) {
        return DECK_SETUP_WIFI_REQUEST_INVALID_PASSWORD;
    }
    return DECK_SETUP_WIFI_REQUEST_OK;
}

deck_setup_temperature_request_result_t deck_setup_http_parse_temperature_request(
    const char *body,
    size_t body_size,
    int16_t *temperature_offset_tenths_c
)
{
    constexpr char kPrefix[] = "offset=";
    constexpr size_t kMaximumBodySize = 64;
    if (body == nullptr || temperature_offset_tenths_c == nullptr || body_size == 0 ||
        body_size > kMaximumBodySize || body_size < sizeof(kPrefix) - 1 ||
        std::memcmp(body, kPrefix, sizeof(kPrefix) - 1) != 0 ||
        std::memchr(body, '&', body_size) != nullptr) {
        return DECK_SETUP_TEMPERATURE_REQUEST_MALFORMED;
    }
    char decoded[32];
    if (!decode_form_component(
            body + sizeof(kPrefix) - 1,
            body_size - sizeof(kPrefix) + 1,
            decoded,
            sizeof(decoded)
        )) {
        return DECK_SETUP_TEMPERATURE_REQUEST_MALFORMED;
    }
    return parse_decimal_tenths(decoded, temperature_offset_tenths_c);
}

bool deck_setup_http_parse_confirmation_request(
    const char *body,
    size_t body_size,
    char *token,
    size_t token_capacity
)
{
    constexpr char kPrefix[] = "token=";
    if (body == nullptr || token == nullptr ||
        token_capacity < DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY ||
        body_size != sizeof(kPrefix) - 1 + DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY - 1 ||
        std::memcmp(body, kPrefix, sizeof(kPrefix) - 1) != 0) {
        return false;
    }
    if (!decode_form_component(
            body + sizeof(kPrefix) - 1,
            body_size - sizeof(kPrefix) + 1,
            token,
            token_capacity
        )) {
        return false;
    }
    for (size_t index = 0; index < DECK_SETUP_CONFIRMATION_TOKEN_CAPACITY - 1; ++index) {
        if (!((token[index] >= '0' && token[index] <= '9') ||
              (token[index] >= 'a' && token[index] <= 'f'))) {
            token[0] = '\0';
            return false;
        }
    }
    return true;
}

bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_wifi_config_snapshot_t *wifi,
    const deck_device_settings_snapshot_t *settings,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
)
{
    if (snapshot == nullptr || wifi == nullptr || settings == nullptr ||
        (networks == nullptr && network_count != 0)) {
        return false;
    }
    BufferWriter writer(buffer, buffer_size);
    writer.append(
        "{\"active\":%s,\"reason\":\"%s\",\"session_id\":%u,\"address\":",
        snapshot->active ? "true" : "false",
        reason_name(snapshot->reason),
        static_cast<unsigned>(snapshot->session_id)
    );
    writer.append_json_string(snapshot->address);
    writer.append(",\"pairing\":\"m1_not_available\",\"wifi\":{");
    writer.append(
        "\"state\":\"%s\",\"record\":\"%s\",\"candidate_record\":\"%s\","
        "\"has_active\":%s,\"has_candidate\":%s,\"generation\":%u,"
        "\"active_ssid\":",
        deck_wifi_config_state_name(wifi->state),
        deck_wifi_record_status_name(wifi->record_status),
        deck_wifi_record_status_name(wifi->candidate_record_status),
        wifi->has_active ? "true" : "false",
        wifi->has_candidate ? "true" : "false",
        static_cast<unsigned>(wifi->generation)
    );
    writer.append_json_string(wifi->active_ssid);
    writer.append(",\"candidate_ssid\":");
    writer.append_json_string(wifi->candidate_ssid);
    writer.append("},\"device_settings\":{");
    writer.append(
        "\"state\":\"%s\",\"record\":\"%s\",\"candidate_record\":\"%s\","
        "\"has_active\":%s,\"has_candidate\":%s,\"generation\":%u,"
        "\"temperature_offset_tenths_c\":%d},\"networks\":[",
        deck_device_settings_state_name(settings->state),
        deck_device_settings_record_status_name(settings->record_status),
        deck_device_settings_record_status_name(settings->candidate_record_status),
        settings->has_active ? "true" : "false",
        settings->has_candidate ? "true" : "false",
        static_cast<unsigned>(settings->generation),
        static_cast<int>(settings->temperature_offset_tenths_c)
    );
    for (size_t index = 0; index < network_count; ++index) {
        if (index != 0) {
            writer.append(",");
        }
        writer.append("{\"ssid\":");
        writer.append_json_string(networks[index].ssid);
        writer.append(
            ",\"rssi\":%d,\"secure\":%s}",
            static_cast<int>(networks[index].rssi),
            networks[index].secure ? "true" : "false"
        );
    }
    writer.append("]}");
    return writer.ok();
}
