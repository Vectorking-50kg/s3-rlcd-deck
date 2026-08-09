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

    output.clear();
    const deck_display_ready_info_t display_info = {
        400,
        300,
        15000,
        1,
        1,
        0,
        0,
        0,
    };
    require(
        deck_display_diagnostics_emit(&display_info, sink),
        "expected the display event to be emitted"
    );
    require(
        output ==
            "{\"type\":\"display_ready\",\"width\":400,\"height\":300,"
            "\"frame_bytes\":15000,\"submitted_frames\":1,\"completed_frames\":1,"
            "\"transfer_timeouts\":0,\"start_failures\":0,\"rejected_updates\":0}\n",
        "expected one complete display_ready JSON line"
    );
}
