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
    "<h2>Companion pairing</h2><form id=pair><label>Hub host:port "
    "<input name=hub_address maxlength=95 required></label> <label>Six-digit code "
    "<input name=code inputmode=numeric pattern='[0-9]{6}' maxlength=6 required></label> "
    "<button>Pair Companion</button></form><div id=companions></div>"
    "<pre id=state>Loading...</pre>"
    "<script>function show(j){state.textContent=JSON.stringify(j,null,2);"
    "companions.replaceChildren();for(let p of(j.companions?.profiles||[])){let row="
    "document.createElement('p'),label=document.createElement('span');label.textContent="
    "p.display_name+' ('+p.hub_address+') ';row.append(label);for(let a of [['Active',"
    "'/api/companions/select'],['Revoke','/api/companions/revoke']]){let b=document."
    "createElement('button');b.textContent=a[0];b.disabled=a[0]=='Active'&&j.companions."
    "active_profile_id==p.profile_id;b.onclick=async()=>{let r=await fetch(a[1],{method:"
    "'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new "
    "URLSearchParams({profile_id:p.profile_id})});show(await r.json());if(r.ok)setTimeout("
    "load,250)};row.append(b)}let q=document.createElement('input');q.type='number';q.min="
    "'-2147483648';q.max='2147483647';q.value=p.priority;q.setAttribute('aria-label',"
    "'Priority');let save=document.createElement("
    "'button');save.textContent='Save priority';save.onclick=async()=>{let r=await fetch("
    "'/api/companions/priority',{method:'POST',headers:{'Content-Type':"
    "'application/x-www-form-urlencoded'},body:new URLSearchParams({profile_id:p.profile_id,"
    "priority:q.value})});show(await r.json());if(r.ok)setTimeout(load,250)};row.append(q,save);"
    "companions.append(row)}}async function load(method='GET',"
    "path='/api/status'){let r=await fetch(path,{method});show(await r.json())}"
    "scan.onclick=()=>load('POST','/api/scan');wifi.onsubmit=async e=>{e.preventDefault();"
    "let r=await fetch('/api/wifi',{method:'POST',headers:{'Content-Type':"
    "'application/x-www-form-urlencoded'},body:new URLSearchParams(new FormData(wifi))});"
    "show(await r.json());if(r.ok)setTimeout(load,500)};"
    "pair.onsubmit=async e=>{e.preventDefault();let r=await fetch('/api/companions/pair',"
    "{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:"
    "new URLSearchParams(new FormData(pair))});let j=await r.json();state.textContent="
    "JSON.stringify(j);pair.code.value='';if(r.ok&&/^[0-9a-f]{32}$/.test(j.response_ack))"
    "fetch('/api/companions/pair/ack',{method:'POST',headers:{'Content-Type':"
    "'application/x-www-form-urlencoded'},body:new URLSearchParams({response_ack:"
    "j.response_ack})}).catch(()=>{})};"
    "temp.onsubmit=async e=>{e.preventDefault();let r=await fetch('/api/temperature',"
    "{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},"
    "body:new URLSearchParams(new FormData(temp))});show(await r.json());if(r.ok)"
    "setTimeout(load,250)};clear.onclick=async()=>{let r="
    "await fetch('/api/wifi/clear/request',{method:'POST'});let j=await r.json();if(!r.ok)"
    "{show(j);return}if(confirm('Clear saved Wi-Fi and "
    "remain in Setup Mode?')){r=await fetch('/api/wifi/clear/confirm',{method:'POST',"
    "headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams("
    "{token:j.token})});show(await r.json());"
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
    {DECK_SETUP_HTTP_COMPANION_PAIR, DECK_SETUP_HTTP_POST, "/api/companions/pair"},
    {DECK_SETUP_HTTP_COMPANION_PAIR_ACK, DECK_SETUP_HTTP_POST, "/api/companions/pair/ack"},
    {DECK_SETUP_HTTP_COMPANION_SELECT, DECK_SETUP_HTTP_POST, "/api/companions/select"},
    {DECK_SETUP_HTTP_COMPANION_PRIORITY, DECK_SETUP_HTTP_POST, "/api/companions/priority"},
    {DECK_SETUP_HTTP_COMPANION_REVOKE, DECK_SETUP_HTTP_POST, "/api/companions/revoke"},
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

bool deck_setup_http_extract_ipv4(
    const uint8_t *address,
    size_t address_size,
    uint8_t ipv4[4]
)
{
    if (address == nullptr || ipv4 == nullptr) {
        return false;
    }
    const uint8_t *source = address;
    if (address_size == 16) {
        constexpr uint8_t kIpv4MappedPrefix[12] = {
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff,
        };
        if (std::memcmp(address, kIpv4MappedPrefix, sizeof(kIpv4MappedPrefix)) != 0) {
            return false;
        }
        source += sizeof(kIpv4MappedPrefix);
    } else if (address_size != 4) {
        return false;
    }
    std::memcpy(ipv4, source, 4);
    return true;
}

bool deck_setup_http_address_is_setup_gateway(
    const uint8_t *local_address,
    size_t local_address_size
)
{
    uint8_t local_ipv4[4]{};
    if (!deck_setup_http_extract_ipv4(
            local_address,
            local_address_size,
            local_ipv4
        )) {
        return false;
    }
    constexpr uint8_t kSetupGateway[] = {192, 168, 4, 1};
    return std::memcmp(local_ipv4, kSetupGateway, sizeof(kSetupGateway)) == 0;
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

const char *deck_setup_http_page(void)
{
    return kPage;
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

deck_setup_companion_request_result_t deck_setup_http_parse_companion_pair_request(
    const char *body,
    size_t body_size,
    deck_companion_pair_request_t *request
)
{
    constexpr size_t kMaximumBodySize = 160;
    if (body == nullptr || request == nullptr || body_size == 0 ||
        body_size > kMaximumBodySize) {
        return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
    }
    *request = {};
    bool saw_address = false;
    bool saw_code = false;
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
            return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
        }
        char key[20];
        if (!decode_form_component(body + offset, equals - offset, key, sizeof(key))) {
            return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
        }
        if (std::strcmp(key, "hub_address") == 0) {
            if (saw_address || !decode_form_component(
                                   body + equals + 1,
                                   end - equals - 1,
                                   request->hub_address,
                                   sizeof(request->hub_address)
                               )) {
                return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
            }
            saw_address = true;
        } else if (std::strcmp(key, "code") == 0) {
            if (saw_code || !decode_form_component(
                                body + equals + 1,
                                end - equals - 1,
                                request->code,
                                sizeof(request->code)
                            )) {
                return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
            }
            saw_code = true;
        } else {
            return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
        }
        offset = end + 1;
    }
    if (!saw_address || !saw_code) {
        return DECK_SETUP_COMPANION_REQUEST_MALFORMED;
    }
    if (!deck_companion_pairing_code_valid(request->code)) {
        return DECK_SETUP_COMPANION_REQUEST_INVALID_CODE;
    }
    if (!deck_companion_hub_address_valid(request->hub_address)) {
        return DECK_SETUP_COMPANION_REQUEST_INVALID_ADDRESS;
    }
    return DECK_SETUP_COMPANION_REQUEST_OK;
}

bool deck_setup_http_parse_pair_ack_request(
    const char *body,
    size_t body_size,
    uint8_t response_ack[DECK_SETUP_PAIR_ACK_SIZE]
)
{
    constexpr char kPrefix[] = "response_ack=";
    constexpr size_t kHexSize = 32;
    if (body == nullptr || response_ack == nullptr ||
        body_size != sizeof(kPrefix) - 1 + kHexSize ||
        std::memcmp(body, kPrefix, sizeof(kPrefix) - 1) != 0) {
        return false;
    }
    for (size_t index = 0; index < DECK_SETUP_PAIR_ACK_SIZE; ++index) {
        const int high = hex_value(body[sizeof(kPrefix) - 1 + index * 2]);
        const int low = hex_value(body[sizeof(kPrefix) + index * 2]);
        if (high < 0 || low < 0) {
            std::memset(response_ack, 0, DECK_SETUP_PAIR_ACK_SIZE);
            return false;
        }
        response_ack[index] = static_cast<uint8_t>((high << 4) | low);
    }
    return true;
}

bool deck_setup_http_parse_companion_profile_request(
    const char *body,
    size_t body_size,
    char *profile_id,
    size_t profile_id_capacity
)
{
    constexpr char kPrefix[] = "profile_id=";
    if (body == nullptr || profile_id == nullptr ||
        profile_id_capacity < DECK_COMPANION_PROFILE_ID_CAPACITY ||
        body_size < sizeof(kPrefix) - 1 || body_size > 128 ||
        std::memcmp(body, kPrefix, sizeof(kPrefix) - 1) != 0 ||
        std::memchr(body, '&', body_size) != nullptr ||
        !decode_form_component(
            body + sizeof(kPrefix) - 1,
            body_size - sizeof(kPrefix) + 1,
            profile_id,
            profile_id_capacity
        ) || std::strlen(profile_id) != 71 ||
        std::memcmp(profile_id, "sha256:", 7) != 0) {
        if (profile_id != nullptr && profile_id_capacity != 0) {
            profile_id[0] = '\0';
        }
        return false;
    }
    for (size_t index = 7; index < 71; ++index) {
        if (!((profile_id[index] >= '0' && profile_id[index] <= '9') ||
              (profile_id[index] >= 'a' && profile_id[index] <= 'f'))) {
            profile_id[0] = '\0';
            return false;
        }
    }
    return true;
}

bool deck_setup_http_parse_companion_priority_request(
    const char *body,
    size_t body_size,
    char *profile_id,
    size_t profile_id_capacity,
    int32_t *priority
)
{
    constexpr char kProfilePrefix[] = "profile_id=";
    constexpr char kPrioritySeparator[] = "&priority=";
    if (body == nullptr || profile_id == nullptr || priority == nullptr ||
        profile_id_capacity < DECK_COMPANION_PROFILE_ID_CAPACITY ||
        body_size == 0 || body_size > 160 ||
        body_size <= sizeof(kProfilePrefix) - 1 +
                         sizeof(kPrioritySeparator) - 1 ||
        std::memcmp(body, kProfilePrefix, sizeof(kProfilePrefix) - 1) != 0) {
        return false;
    }
    const char *separator = static_cast<const char *>(std::memchr(
        body + sizeof(kProfilePrefix) - 1,
        '&',
        body_size - sizeof(kProfilePrefix) + 1
    ));
    if (separator == nullptr ||
        static_cast<size_t>(body + body_size - separator) <=
            sizeof(kPrioritySeparator) - 1 ||
        std::memcmp(
            separator,
            kPrioritySeparator,
            sizeof(kPrioritySeparator) - 1
        ) != 0 ||
        std::memchr(
            separator + 1,
            '&',
            static_cast<size_t>(body + body_size - separator - 1)
        ) !=
            nullptr ||
        !decode_form_component(
            body + sizeof(kProfilePrefix) - 1,
            static_cast<size_t>(separator - body) - sizeof(kProfilePrefix) + 1,
            profile_id,
            profile_id_capacity
        ) ||
        std::strlen(profile_id) != 71 ||
        std::memcmp(profile_id, "sha256:", 7) != 0) {
        profile_id[0] = '\0';
        return false;
    }
    for (size_t index = 7; index < 71; ++index) {
        if (!((profile_id[index] >= '0' && profile_id[index] <= '9') ||
              (profile_id[index] >= 'a' && profile_id[index] <= 'f'))) {
            profile_id[0] = '\0';
            return false;
        }
    }
    char decoded_priority[16]{};
    const char *encoded_priority = separator + sizeof(kPrioritySeparator) - 1;
    const size_t encoded_priority_size =
        static_cast<size_t>(body + body_size - encoded_priority);
    if (!decode_form_component(
            encoded_priority,
            encoded_priority_size,
            decoded_priority,
            sizeof(decoded_priority)
        )) {
        profile_id[0] = '\0';
        return false;
    }
    const char *cursor = decoded_priority;
    bool negative = false;
    if (*cursor == '-') {
        negative = true;
        ++cursor;
    }
    if (*cursor == '\0') {
        profile_id[0] = '\0';
        return false;
    }
    uint64_t magnitude = 0;
    const uint64_t maximum = negative
                                 ? UINT64_C(2147483648)
                                 : UINT64_C(2147483647);
    for (; *cursor != '\0'; ++cursor) {
        if (*cursor < '0' || *cursor > '9' ||
            magnitude > (maximum - static_cast<uint64_t>(*cursor - '0')) / 10U) {
            profile_id[0] = '\0';
            return false;
        }
        magnitude = magnitude * 10U + static_cast<uint64_t>(*cursor - '0');
    }
    *priority = negative
                    ? magnitude == UINT64_C(2147483648)
                          ? INT32_MIN
                          : -static_cast<int32_t>(magnitude)
                    : static_cast<int32_t>(magnitude);
    return true;
}

bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_wifi_config_snapshot_t *wifi,
    const deck_device_settings_snapshot_t *settings,
    const deck_companion_profiles_snapshot_t *companions,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
)
{
    if (snapshot == nullptr || wifi == nullptr || settings == nullptr ||
        companions == nullptr ||
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
    writer.append(",\"wifi\":{");
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
        "\"temperature_offset_tenths_c\":%d},",
        deck_device_settings_state_name(settings->state),
        deck_device_settings_record_status_name(settings->record_status),
        deck_device_settings_record_status_name(settings->candidate_record_status),
        settings->has_active ? "true" : "false",
        settings->has_candidate ? "true" : "false",
        static_cast<unsigned>(settings->generation),
        static_cast<int>(settings->temperature_offset_tenths_c)
    );
    writer.append("\"companions\":{");
    writer.append(
        "\"generation\":%u,\"storage_faulted\":%s,\"has_active\":%s,"
        "\"active_profile_id\":",
        static_cast<unsigned>(companions->generation),
        companions->storage_faulted ? "true" : "false",
        companions->has_active ? "true" : "false"
    );
    writer.append_json_string(companions->active_profile_id);
    writer.append(",\"profiles\":[");
    for (size_t index = 0; index < companions->count; ++index) {
        if (index != 0) {
            writer.append(",");
        }
        const deck_companion_profile_view_t &profile = companions->profiles[index];
        writer.append("{\"profile_version\":%u,\"profile_id\":", static_cast<unsigned>(profile.profile_version));
        writer.append_json_string(profile.profile_id);
        writer.append(",\"display_name\":");
        writer.append_json_string(profile.display_name);
        writer.append(",\"hub_address\":");
        writer.append_json_string(profile.hub_address);
        writer.append(",\"certificate_fingerprint\":");
        writer.append_json_string(profile.certificate_fingerprint);
        writer.append(
            ",\"priority\":%ld,\"last_success_unix_ms\":%llu}",
            static_cast<long>(profile.priority),
            static_cast<unsigned long long>(profile.last_success_unix_ms)
        );
    }
    writer.append("]},\"networks\":[");
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
