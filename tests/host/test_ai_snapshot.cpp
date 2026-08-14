#include "deck_ai_snapshot.h"

#include <cassert>
#include <cstring>
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
    std::string encoding;
    std::string result;
};

std::string decode_hex(const std::string &encoded)
{
    std::string decoded;
    unsigned high = 0;
    bool have_high = false;
    for (const char character : encoded) {
        if (character == '\n' || character == '\r' || character == ' ' || character == '\t') {
            continue;
        }
        unsigned value = 0;
        if (character >= '0' && character <= '9') {
            value = static_cast<unsigned>(character - '0');
        } else if (character >= 'a' && character <= 'f') {
            value = static_cast<unsigned>(character - 'a' + 10);
        } else if (character >= 'A' && character <= 'F') {
            value = static_cast<unsigned>(character - 'A' + 10);
        } else {
            assert(false);
        }
        if (!have_high) {
            high = value;
            have_high = true;
        } else {
            decoded.push_back(static_cast<char>((high << 4U) | value));
            have_high = false;
        }
    }
    assert(!have_high);
    return decoded;
}

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
        const size_t encoding_key = manifest.find("\"encoding\"", cursor);
        const std::string encoding = encoding_key < result_key ?
            quoted_value(manifest, encoding_key) : "json";
        cases.push_back({file, encoding, quoted_value(manifest, result_key)});
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
        std::string document = read_file(
            root + "/protocol/fixtures/ai-snapshot-v1/" + test_case.file
        );
        if (test_case.encoding == "hex") {
            document = decode_hex(document);
        } else {
            assert(test_case.encoding == "json");
        }
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

void retained_snapshot_survives_an_unsupported_major()
{
    const std::string root = DECK_REPOSITORY_ROOT;
    const std::string valid = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/valid-full.json"
    );
    const std::string unsupported = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/invalid-major-version.json"
    );
    char storage[DECK_AI_SNAPSHOT_MAX_BYTES]{};
    deck_ai_snapshot_retained_t retained{};
    assert(deck_ai_snapshot_retained_init(&retained, storage, sizeof(storage)));
    assert(deck_ai_snapshot_retained_apply(&retained, valid.data(), valid.size()) ==
           DECK_AI_SNAPSHOT_ACCEPTED);
    const char *before = nullptr;
    size_t before_size = 0;
    deck_ai_snapshot_metadata_t before_metadata{};
    assert(deck_ai_snapshot_retained_current(
        &retained, &before, &before_size, &before_metadata
    ));
    const std::string before_copy(before, before_size);

    assert(deck_ai_snapshot_retained_apply(
        &retained, unsupported.data(), unsupported.size()
    ) == DECK_AI_SNAPSHOT_UNSUPPORTED_VERSION);
    const char *after = nullptr;
    size_t after_size = 0;
    deck_ai_snapshot_metadata_t after_metadata{};
    assert(deck_ai_snapshot_retained_current(
        &retained, &after, &after_size, &after_metadata
    ));
    assert(std::string(after, after_size) == before_copy);
    assert(after_metadata.generated_at_unix_ms == before_metadata.generated_at_unix_ms);
}

void projects_only_bounded_codex_display_fields()
{
    const std::string root = DECK_REPOSITORY_ROOT;
    const std::string document = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/valid-full.json"
    );
    deck_ai_snapshot_codex_projection_t projection{};
    assert(deck_ai_snapshot_project_codex(document.data(), document.size(), &projection));
    assert(projection.has_timezone);
    assert(std::strcmp(projection.timezone, "Asia/Shanghai") == 0);
    assert(projection.provider_present);
    assert(std::strcmp(projection.provider_display_name, "Codex") == 0);
    assert(projection.provider_status == DECK_AI_SNAPSHOT_PROVIDER_OK);
    assert(projection.provider_confidence == DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED);
    assert(projection.has_provider_updated_at);
    assert(projection.window_count == 1U);
    assert(std::strcmp(projection.windows[0].name, "primary") == 0);
    assert(projection.windows[0].has_used_basis_points);
    assert(projection.windows[0].used_basis_points == 3'800U);
    assert(projection.windows[0].has_remaining_basis_points);
    assert(projection.windows[0].remaining_basis_points == 6'200U);
    assert(projection.windows[0].has_window_minutes);
    assert(projection.windows[0].window_minutes == 300U);
    assert(projection.windows[0].has_resets_at);
    assert(projection.has_total_tokens && projection.total_tokens == 146'000U);
    assert(projection.session_count == 1U);
    assert(projection.featured_session.present);
    assert(std::strcmp(projection.featured_session.display_name, "Deck development") == 0);
    assert(projection.featured_session.state == DECK_AI_SNAPSHOT_SESSION_RUNNING);
    assert(projection.featured_session.confidence == DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED);
    assert(projection.featured_session.has_duration_seconds);
    assert(projection.featured_session.duration_seconds == 890U);
    assert(projection.featured_session.has_turn_tokens);
    assert(projection.featured_session.turn_tokens == 18'420U);
    assert(projection.featured_session.has_context_used_basis_points);
    assert(projection.featured_session.context_used_basis_points == 4'100U);
}

void projection_decodes_unicode_and_prioritizes_actionable_sessions()
{
    const std::string root = DECK_REPOSITORY_ROOT;
    const std::string unicode = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/valid-unicode-boundary.json"
    );
    deck_ai_snapshot_codex_projection_t projection{};
    assert(deck_ai_snapshot_project_codex(unicode.data(), unicode.size(), &projection));
    assert(projection.provider_present);
    assert(std::string(projection.provider_display_name).find("甲乙丙") == 0U);

    const std::string enumerations = read_file(
        root + "/protocol/fixtures/ai-snapshot-v1/valid-enumerations.json"
    );
    assert(deck_ai_snapshot_project_codex(
        enumerations.data(), enumerations.size(), &projection
    ));
    assert(projection.session_count == 9U);
    assert(projection.featured_session.present);
    assert(projection.featured_session.state == DECK_AI_SNAPSHOT_SESSION_WAITING_APPROVAL);
    assert(projection.featured_session.confidence == DECK_AI_SNAPSHOT_CONFIDENCE_VERIFIED);
}

void projection_fails_closed_without_mutating_output()
{
    deck_ai_snapshot_codex_projection_t projection{};
    projection.provider_present = true;
    std::strcpy(projection.provider_display_name, "sentinel");
    const char malformed[] = "{}";
    assert(!deck_ai_snapshot_project_codex(malformed, sizeof(malformed) - 1U, &projection));
    assert(!projection.provider_present);
    assert(projection.provider_display_name[0] == '\0');
}

}  // namespace

int main()
{
    shared_fixtures_match_the_firmware_contract();
    retained_snapshot_survives_an_unsupported_major();
    projects_only_bounded_codex_display_fields();
    projection_decodes_unicode_and_prioritizes_actionable_sessions();
    projection_fails_closed_without_mutating_output();
    return 0;
}
