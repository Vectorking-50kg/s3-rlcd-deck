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
    "<h1>Deck Setup / Recovery</h1><p>This temporary HTTP page only reports status and "
    "nearby networks. Wi-Fi changes are enabled by the next M0 step.</p>"
    "<button id=scan>Scan networks</button><pre id=state>Loading...</pre>"
    "<p><strong>Companion pairing is planned for M1.</strong></p>"
    "<script>async function load(method='GET',path='/api/status'){let r=await "
    "fetch(path,{method});state.textContent=JSON.stringify(await r.json(),null,2)}"
    "scan.onclick=()=>load('POST','/api/scan');load()</script></body></html>";

constexpr deck_setup_http_route_spec_t kRoutes[] = {
    {DECK_SETUP_HTTP_PAGE, DECK_SETUP_HTTP_GET, "/"},
    {DECK_SETUP_HTTP_STATUS, DECK_SETUP_HTTP_GET, "/api/status"},
    {DECK_SETUP_HTTP_SCAN, DECK_SETUP_HTTP_POST, "/api/scan"},
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

bool deck_setup_http_render_status(
    const deck_setup_snapshot_t *snapshot,
    const deck_setup_scan_result_t *networks,
    size_t network_count,
    char *buffer,
    size_t buffer_size
)
{
    if (snapshot == nullptr || (networks == nullptr && network_count != 0)) {
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
    writer.append(",\"pairing\":\"m1_not_available\",\"networks\":[");
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
