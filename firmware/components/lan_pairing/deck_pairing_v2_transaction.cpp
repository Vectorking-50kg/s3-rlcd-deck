#include "deck_pairing_v2_transaction.h"

#include <cstdio>
#include <cstring>
#include <memory>
#include <new>

namespace {

void secure_clear(void *buffer, size_t size)
{
    volatile uint8_t *bytes = static_cast<volatile uint8_t *>(buffer);
    while (size > 0) {
        *bytes++ = 0;
        --size;
    }
}

bool copy_string(char *output, size_t capacity, const char *value)
{
    if (output == nullptr || capacity == 0 || value == nullptr) {
        return false;
    }
    const size_t size = std::strlen(value);
    if (size == 0 || size >= capacity) {
        return false;
    }
    std::memcpy(output, value, size + 1U);
    return true;
}

bool exact_string(const char *left, const char *right)
{
    if (left == nullptr || right == nullptr) {
        return false;
    }
    const size_t left_size = std::strlen(left);
    const size_t right_size = std::strlen(right);
    if (left_size != right_size) {
        return false;
    }
    uint8_t difference = 0;
    for (size_t index = 0; index < left_size; ++index) {
        difference |= static_cast<uint8_t>(left[index] ^ right[index]);
    }
    return difference == 0;
}

const char *transaction_error_code(deck_pairing_v2_transaction_result_t result)
{
    switch (result) {
        case DECK_PAIRING_V2_TRANSACTION_CONFLICT:
            return "busy";
        case DECK_PAIRING_V2_TRANSACTION_LINK_REQUIRED:
            return "link_failed";
        case DECK_PAIRING_V2_TRANSACTION_CAPACITY_REACHED:
            return "capacity_reached";
        case DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE:
            return "storage_failure";
        case DECK_PAIRING_V2_TRANSACTION_INVALID:
            return "malformed";
        default:
            return nullptr;
    }
}

bool encode_transaction_error(
    const deck_pairing_v2_common_t &request,
    deck_pairing_v2_transaction_result_t result,
    char *response,
    size_t response_capacity,
    size_t *response_size
)
{
    const char *code = transaction_error_code(result);
    std::unique_ptr<deck_pairing_v2_message_t> failure(
        new (std::nothrow) deck_pairing_v2_message_t{}
    );
    const bool valid = failure != nullptr && code != nullptr && request.sequence < UINT32_MAX &&
                       copy_string(
                           failure->common.session_id,
                           sizeof(failure->common.session_id),
                           request.session_id
                       ) &&
                       copy_string(
                           failure->common.transaction_id,
                           sizeof(failure->common.transaction_id),
                           request.transaction_id
                       ) &&
                       copy_string(failure->error_code, sizeof(failure->error_code), code);
    if (valid) {
        failure->type = DECK_PAIRING_V2_MESSAGE_ERROR;
        failure->common.sequence = request.sequence + 1U;
    }
    const bool encoded = valid && deck_pairing_v2_contract_encode(
                                      failure.get(),
                                      response,
                                      response_capacity,
                                      response_size
                                  );
    if (failure != nullptr) {
        deck_pairing_v2_contract_clear(failure.get());
    }
    return encoded;
}

bool valid_hex_id(const char *value)
{
    if (value == nullptr || std::strlen(value) != 32U) {
        return false;
    }
    for (size_t index = 0; index < 32U; ++index) {
        if (!((value[index] >= '0' && value[index] <= '9') ||
              (value[index] >= 'a' && value[index] <= 'f'))) {
            return false;
        }
    }
    return true;
}

void encode_hex(const uint8_t *input, size_t size, char *output)
{
    constexpr char hexadecimal[] = "0123456789abcdef";
    for (size_t index = 0; index < size; ++index) {
        output[index * 2U] = hexadecimal[input[index] >> 4U];
        output[index * 2U + 1U] = hexadecimal[input[index] & 0x0fU];
    }
    output[size * 2U] = '\0';
}

deck_pairing_v2_transaction_result_t stage_result(
    deck_companion_profile_stage_result_t result
)
{
    switch (result) {
        case DECK_COMPANION_PROFILE_STAGE_CAPACITY_REACHED:
            return DECK_PAIRING_V2_TRANSACTION_CAPACITY_REACHED;
        case DECK_COMPANION_PROFILE_STAGE_STORAGE_FAILURE:
            return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
        case DECK_COMPANION_PROFILE_STAGE_CONFLICT:
        case DECK_COMPANION_PROFILE_STAGE_STALE_GENERATION:
            return DECK_PAIRING_V2_TRANSACTION_CONFLICT;
        case DECK_COMPANION_PROFILE_STAGE_UPDATED:
            return DECK_PAIRING_V2_TRANSACTION_OK;
        case DECK_COMPANION_PROFILE_STAGE_NOT_FOUND:
        case DECK_COMPANION_PROFILE_STAGE_INVALID_ARGUMENT:
        default:
            return DECK_PAIRING_V2_TRANSACTION_INVALID;
    }
}

}  // namespace

struct deck_pairing_v2_transaction {
    deck_pairing_v2_transaction_options_t options;
    char window_nonce[DECK_PAIRING_V2_ID_CAPACITY];
    std::unique_ptr<deck_pairing_v2_message_t> credentials;
    std::unique_ptr<deck_pairing_v2_message_t> ready;
    deck_companion_profile_stage_ticket_t ticket;
    uint64_t server_utc_ms;
    bool staged;
    bool link_proven;
    bool committed;
};

namespace {

void cancel_staged(deck_pairing_v2_transaction_t *transaction)
{
    if (transaction->staged && !transaction->committed && transaction->credentials != nullptr) {
        (void)deck_companion_profiles_cancel_staged(
            transaction->options.profiles,
            transaction->credentials->common.session_id,
            transaction->credentials->common.transaction_id
        );
    }
    transaction->staged = false;
}

void clear_state(deck_pairing_v2_transaction_t *transaction, bool clear_window)
{
    cancel_staged(transaction);
    if (transaction->credentials != nullptr) {
        deck_pairing_v2_contract_clear(transaction->credentials.get());
        transaction->credentials.reset();
    }
    if (transaction->ready != nullptr) {
        deck_pairing_v2_contract_clear(transaction->ready.get());
        transaction->ready.reset();
    }
    secure_clear(&transaction->ticket, sizeof(transaction->ticket));
    transaction->server_utc_ms = 0;
    transaction->link_proven = false;
    transaction->committed = false;
    if (clear_window) {
        secure_clear(transaction->window_nonce, sizeof(transaction->window_nonce));
    }
}

deck_pairing_v2_transaction_result_t accept_credentials(
    deck_pairing_v2_transaction_t *transaction,
    std::unique_ptr<deck_pairing_v2_message_t> message,
    char *response,
    size_t response_capacity,
    size_t *response_size,
    deck_pairing_v2_transaction_action_t *action
)
{
    if (transaction->staged || transaction->committed ||
        !exact_string(message->credentials.window_nonce, transaction->window_nonce)) {
        return DECK_PAIRING_V2_TRANSACTION_CONFLICT;
    }
    deck_companion_profile_stage_request_t stage{};
    const bool request_copied =
        copy_string(stage.session_id, sizeof(stage.session_id), message->common.session_id) &&
        copy_string(
            stage.transaction_id,
            sizeof(stage.transaction_id),
            message->common.transaction_id
        ) &&
        copy_string(
            stage.hub_service,
            sizeof(stage.hub_service),
            message->credentials.hub_service
        ) &&
        copy_string(
            stage.hub_address,
            sizeof(stage.hub_address),
            message->credentials.hub_address
        ) &&
        copy_string(stage.token, sizeof(stage.token), message->credentials.token) &&
        copy_string(
            stage.certificate_fingerprint,
            sizeof(stage.certificate_fingerprint),
            message->credentials.certificate_fingerprint
        );
    if (!request_copied ||
        message->credentials.certificate_der_size > sizeof(stage.certificate_der)) {
        secure_clear(&stage, sizeof(stage));
        return DECK_PAIRING_V2_TRANSACTION_INVALID;
    }
    stage.certificate_der_size = message->credentials.certificate_der_size;
    stage.protocol_version = static_cast<uint8_t>(message->credentials.device_link_protocol);
    std::memcpy(
        stage.certificate_der,
        message->credentials.certificate_der,
        message->credentials.certificate_der_size
    );
    const deck_companion_profile_stage_result_t staged =
        deck_companion_profiles_stage_authenticated(
            transaction->options.profiles,
            &stage,
            &transaction->ticket
        );
    secure_clear(&stage, sizeof(stage));
    const deck_pairing_v2_transaction_result_t mapped = stage_result(staged);
    if (mapped != DECK_PAIRING_V2_TRANSACTION_OK) {
        return mapped;
    }
    transaction->staged = true;

    std::unique_ptr<deck_pairing_v2_message_t> ready(
        new (std::nothrow) deck_pairing_v2_message_t{}
    );
    uint8_t nonce[16]{};
    if (ready == nullptr || !transaction->options.random(
                               transaction->options.context,
                               nonce,
                               sizeof(nonce)
                           ) ||
        !copy_string(
            ready->common.session_id,
            sizeof(ready->common.session_id),
            message->common.session_id
        ) ||
        !copy_string(
            ready->common.transaction_id,
            sizeof(ready->common.transaction_id),
            message->common.transaction_id
        ) ||
        !copy_string(
            ready->commit_ready.window_nonce,
            sizeof(ready->commit_ready.window_nonce),
            message->credentials.window_nonce
        ) ||
        !copy_string(
            ready->commit_ready.companion_nonce,
            sizeof(ready->commit_ready.companion_nonce),
            message->credentials.companion_nonce
        ) ||
        !copy_string(
            ready->commit_ready.profile_id,
            sizeof(ready->commit_ready.profile_id),
            message->credentials.certificate_fingerprint
        ) ||
        !transaction->options.identity(
            transaction->options.context,
            ready->commit_ready.device_id,
            sizeof(ready->commit_ready.device_id),
            ready->commit_ready.device_identity,
            sizeof(ready->commit_ready.device_identity)
        )) {
        secure_clear(nonce, sizeof(nonce));
        clear_state(transaction, false);
        return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
    }
    encode_hex(nonce, sizeof(nonce), ready->commit_ready.deck_nonce);
    secure_clear(nonce, sizeof(nonce));
    ready->type = DECK_PAIRING_V2_MESSAGE_COMMIT_READY;
    ready->common.sequence = 2;
    if (!deck_pairing_v2_contract_transcript_sha256(
            message.get(),
            ready.get(),
            &transaction->options.crypto,
            ready->commit_ready.transcript_sha256
        ) ||
        !deck_pairing_v2_contract_encode(
            ready.get(),
            response,
            response_capacity,
            response_size
        )) {
        clear_state(transaction, false);
        return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
    }
    transaction->credentials = std::move(message);
    transaction->ready = std::move(ready);
    *action = DECK_PAIRING_V2_ACTION_START_LINK_PROOF;
    return DECK_PAIRING_V2_TRANSACTION_OK;
}

deck_pairing_v2_transaction_result_t accept_commit(
    deck_pairing_v2_transaction_t *transaction,
    const deck_pairing_v2_message_t &message,
    char *response,
    size_t response_capacity,
    size_t *response_size,
    deck_pairing_v2_transaction_action_t *action
)
{
    if (!transaction->staged || transaction->credentials == nullptr ||
        transaction->ready == nullptr || !transaction->link_proven ||
        transaction->server_utc_ms == 0) {
        return DECK_PAIRING_V2_TRANSACTION_LINK_REQUIRED;
    }
    const bool exact =
        exact_string(message.common.session_id, transaction->credentials->common.session_id) &&
        exact_string(
            message.common.transaction_id,
            transaction->credentials->common.transaction_id
        ) &&
        exact_string(
            message.commit.deck_nonce,
            transaction->ready->commit_ready.deck_nonce
        ) &&
        exact_string(
            message.commit.transcript_sha256,
            transaction->ready->commit_ready.transcript_sha256
        );
    if (!exact) {
        return DECK_PAIRING_V2_TRANSACTION_CONFLICT;
    }
    std::unique_ptr<deck_pairing_v2_message_t> receipt(
        new (std::nothrow) deck_pairing_v2_message_t{}
    );
    if (receipt == nullptr ||
        !copy_string(
            receipt->common.session_id,
            sizeof(receipt->common.session_id),
            message.common.session_id
        ) ||
        !copy_string(
            receipt->common.transaction_id,
            sizeof(receipt->common.transaction_id),
            message.common.transaction_id
        ) ||
        !copy_string(
            receipt->profile_id,
            sizeof(receipt->profile_id),
            transaction->ready->commit_ready.profile_id
        ) ||
        !copy_string(
            receipt->transcript_sha256,
            sizeof(receipt->transcript_sha256),
            transaction->ready->commit_ready.transcript_sha256
        )) {
        return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
    }
    receipt->type = DECK_PAIRING_V2_MESSAGE_COMMIT_RECEIPT;
    receipt->common.sequence = 4;
    // Prove allocation and response capacity before crossing the persistent
    // commit boundary. The real generation is never wider than UINT32_MAX.
    receipt->profile_generation = UINT32_MAX;
    if (!deck_pairing_v2_contract_encode(
            receipt.get(),
            response,
            response_capacity,
            response_size
        )) {
        deck_pairing_v2_contract_clear(receipt.get());
        return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
    }
    secure_clear(response, response_capacity);
    *response_size = 0;
    const deck_companion_profile_stage_result_t committed =
        deck_companion_profiles_commit_staged(
            transaction->options.profiles,
            &transaction->ticket,
            transaction->server_utc_ms
        );
    const deck_pairing_v2_transaction_result_t mapped = stage_result(committed);
    if (mapped != DECK_PAIRING_V2_TRANSACTION_OK) {
        deck_pairing_v2_contract_clear(receipt.get());
        return mapped;
    }
    transaction->staged = false;
    transaction->committed = true;
    receipt->profile_generation = transaction->ticket.profile_generation;
    if (!deck_pairing_v2_contract_encode(
            receipt.get(),
            response,
            response_capacity,
            response_size
        )) {
        deck_pairing_v2_contract_clear(receipt.get());
        return DECK_PAIRING_V2_TRANSACTION_STORAGE_FAILURE;
    }
    deck_pairing_v2_contract_clear(receipt.get());
    *action = DECK_PAIRING_V2_ACTION_PROFILE_COMMITTED;
    return DECK_PAIRING_V2_TRANSACTION_OK;
}

}  // namespace

deck_pairing_v2_transaction_t *deck_pairing_v2_transaction_create(
    const deck_pairing_v2_transaction_options_t *options
)
{
    if (options == nullptr || options->profiles == nullptr || options->crypto.sha256 == nullptr ||
        options->random == nullptr || options->identity == nullptr) {
        return nullptr;
    }
    auto *transaction = new (std::nothrow) deck_pairing_v2_transaction_t{};
    if (transaction != nullptr) {
        transaction->options = *options;
    }
    return transaction;
}

bool deck_pairing_v2_transaction_begin_window(
    deck_pairing_v2_transaction_t *transaction,
    const char *window_nonce
)
{
    if (transaction == nullptr || !valid_hex_id(window_nonce)) {
        return false;
    }
    clear_state(transaction, true);
    return copy_string(
        transaction->window_nonce,
        sizeof(transaction->window_nonce),
        window_nonce
    );
}

deck_pairing_v2_transaction_result_t deck_pairing_v2_transaction_exchange(
    deck_pairing_v2_transaction_t *transaction,
    const char *document,
    size_t document_size,
    char *response,
    size_t response_capacity,
    size_t *response_size,
    deck_pairing_v2_transaction_action_t *action
)
{
    if (response != nullptr && response_capacity != 0) {
        secure_clear(response, response_capacity);
    }
    if (response_size != nullptr) {
        *response_size = 0;
    }
    if (action != nullptr) {
        *action = DECK_PAIRING_V2_ACTION_NONE;
    }
    if (transaction == nullptr || document == nullptr || document_size == 0 ||
        response == nullptr || response_capacity == 0 || response_size == nullptr ||
        action == nullptr || transaction->window_nonce[0] == '\0') {
        return DECK_PAIRING_V2_TRANSACTION_INVALID;
    }
    std::unique_ptr<deck_pairing_v2_message_t> message(
        new (std::nothrow) deck_pairing_v2_message_t{}
    );
    if (message == nullptr || !deck_pairing_v2_contract_decode(
                                  document,
                                  document_size,
                                  &transaction->options.crypto,
                                  message.get()
                              )) {
        return DECK_PAIRING_V2_TRANSACTION_INVALID;
    }
    const deck_pairing_v2_common_t request_common = message->common;
    deck_pairing_v2_transaction_result_t result = DECK_PAIRING_V2_TRANSACTION_INVALID;
    if (message->type == DECK_PAIRING_V2_MESSAGE_CREDENTIALS) {
        result = accept_credentials(
            transaction,
            std::move(message),
            response,
            response_capacity,
            response_size,
            action
        );
    } else if (message->type == DECK_PAIRING_V2_MESSAGE_COMMIT) {
        result = accept_commit(
            transaction,
            *message,
            response,
            response_capacity,
            response_size,
            action
        );
    }
    if (message != nullptr) {
        deck_pairing_v2_contract_clear(message.get());
    }
    if (result != DECK_PAIRING_V2_TRANSACTION_OK) {
        encode_transaction_error(
            request_common,
            result,
            response,
            response_capacity,
            response_size
        );
    }
    return result;
}

bool deck_pairing_v2_transaction_link_request(
    const deck_pairing_v2_transaction_t *transaction,
    deck_pairing_v2_link_request_t *request
)
{
    if (request != nullptr) {
        secure_clear(request, sizeof(*request));
    }
    if (transaction == nullptr || request == nullptr || !transaction->staged ||
        transaction->credentials == nullptr || transaction->ready == nullptr ||
        !copy_string(
            request->session_id,
            sizeof(request->session_id),
            transaction->credentials->common.session_id
        ) ||
        !copy_string(
            request->transaction_id,
            sizeof(request->transaction_id),
            transaction->credentials->common.transaction_id
        ) ||
        !copy_string(
            request->device_id,
            sizeof(request->device_id),
            transaction->ready->commit_ready.device_id
        ) ||
        !copy_string(
            request->device_identity,
            sizeof(request->device_identity),
            transaction->ready->commit_ready.device_identity
        ) ||
        !deck_companion_profiles_staged_secret(
            transaction->options.profiles,
            &transaction->ticket,
            &request->secret
        )) {
        if (request != nullptr) {
            deck_pairing_v2_link_request_clear(request);
        }
        return false;
    }
    return true;
}

bool deck_pairing_v2_transaction_mark_link_proven(
    deck_pairing_v2_transaction_t *transaction,
    const char *session_id,
    const char *transaction_id,
    uint64_t server_utc_ms
)
{
    if (transaction == nullptr || transaction->credentials == nullptr ||
        !transaction->staged || transaction->committed || server_utc_ms == 0 ||
        !exact_string(session_id, transaction->credentials->common.session_id) ||
        !exact_string(transaction_id, transaction->credentials->common.transaction_id)) {
        return false;
    }
    transaction->server_utc_ms = server_utc_ms;
    transaction->link_proven = true;
    return true;
}

void deck_pairing_v2_link_request_clear(deck_pairing_v2_link_request_t *request)
{
    if (request != nullptr) {
        secure_clear(request, sizeof(*request));
    }
}

void deck_pairing_v2_transaction_reset(deck_pairing_v2_transaction_t *transaction)
{
    if (transaction != nullptr) {
        clear_state(transaction, true);
    }
}

void deck_pairing_v2_transaction_destroy(deck_pairing_v2_transaction_t *transaction)
{
    if (transaction != nullptr) {
        clear_state(transaction, true);
        delete transaction;
    }
}
