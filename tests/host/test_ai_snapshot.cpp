#include "deck_ai_snapshot.h"

#include <cassert>
#include <fstream>
#include <iterator>
#include <string>
#include <vector>

namespace {

std::string read_file(const std::string &path)
{
    std::ifstream input(path, std::ios::binary);
    assert(input.good());
    return std::string(
        std::istreambuf_iterator<char>(input),
        std::istreambuf_iterator<char>()
    );
}

struct FixtureCase {
    std::string file;
    std::string result;
};

std::string quoted_value(const std::string &document, size_t key_position)
{
    const size_t colon = document.find(':', key_position);
    const size_t opening = document.find('"', colon + 1U);
    const size_t closing = document.find('"', opening + 1U);
    assert(colon != std::string::npos && opening != std::string::npos &&
           closing != std::string::npos);
    return document.substr(opening + 1U, closing - opening - 1U);
}

std::vector<FixtureCase> manifest_cases()
{
    const std::string root = DECK_REPOSITORY_ROOT;
    const std::string manifest = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/manifest.json"
    );
    std::vector<FixtureCase> cases;
    size_t cursor = 0;
    while ((cursor = manifest.find("\"file\"", cursor)) != std::string::npos) {
        const std::string file = quoted_value(manifest, cursor);
        const size_t result_key = manifest.find("\"result\"", cursor);
        assert(result_key != std::string::npos);
        cases.push_back({file, quoted_value(manifest, result_key)});
        cursor = result_key + 8U;
    }
    return cases;
}

deck_ai_snapshot_result_t expected_result(const std::string &value)
{
    if (value == "accepted") {
        return DECK_AI_SNAPSHOT_ACCEPTED;
    }
    if (value == "malformed") {
        return DECK_AI_SNAPSHOT_MALFORMED;
    }
    if (value == "unsupported_version") {
        return DECK_AI_SNAPSHOT_UNSUPPORTED_VERSION;
    }
    assert(value == "private_data");
    return DECK_AI_SNAPSHOT_PRIVATE_DATA;
}

void shared_fixtures_match_the_firmware_contract()
{
    const std::string root = DECK_REPOSITORY_ROOT;
    const std::vector<FixtureCase> cases = manifest_cases();
    assert(cases.size() >= 12U);
    for (const FixtureCase &test_case : cases) {
        const std::string document = read_file(
            root + "/protocol/fixtures/ai-snapshot-v1/" + test_case.file
        );
        deck_ai_snapshot_metadata_t metadata{};
        const deck_ai_snapshot_result_t result = deck_ai_snapshot_validate(
            document.data(), document.size(), &metadata
        );
        assert(result == expected_result(test_case.result));
        if (test_case.file == "valid-full.json") {
            assert(metadata.schema_minor == 0U);
            assert(metadata.provider_count == 1U);
            assert(metadata.session_count == 1U);
            assert(metadata.generated_at_unix_ms == 1'786'624'496'000ULL);
            assert(metadata.next_refresh_seconds == 5U);
        }
    }
}

}  // namespace

int main()
{
    shared_fixtures_match_the_firmware_contract();
    return 0;
}
