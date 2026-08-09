#include "deck_boot_diagnostics.h"

#include <cstdlib>
#include <iostream>
#include <string>

namespace {

void append_to_string(void *context, const char *data, size_t size)
{
    static_cast<std::string *>(context)->append(data, size);
}

void require(bool condition, const char *message)
{
    if (!condition) {
        std::cerr << message << '\n';
        std::exit(EXIT_FAILURE);
    }
}

}  // namespace

int main()
{
    const deck_boot_info_t info = {
        "0.1.0-dev",
        "power_on",
        42,
        131072,
    };
    std::string output;
    const deck_diagnostic_sink_t sink = {
        append_to_string,
        &output,
    };

    const bool emitted = deck_boot_diagnostics_emit(&info, sink);

    require(emitted, "expected the boot event to be emitted");
    require(
        output ==
            "{\"type\":\"boot_ok\",\"firmware_version\":\"0.1.0-dev\","
            "\"reset_reason\":\"power_on\",\"uptime_ms\":42,"
            "\"minimum_free_heap_bytes\":131072}\n",
        "expected one complete boot_ok JSON line"
    );
}
