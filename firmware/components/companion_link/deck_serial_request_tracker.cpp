#include "deck_serial_request_tracker.h"

namespace {

uint64_t allocate_service_request_id(deck_serial_request_tracker_t *tracker)
{
    if (tracker->next_service_request_id == 0) {
        tracker->next_service_request_id = 1;
    }
    const uint64_t request_id = tracker->next_service_request_id;
    ++tracker->next_service_request_id;
    if (tracker->next_service_request_id == 0) {
        tracker->next_service_request_id = 1;
    }
    return request_id;
}

}  // namespace

deck_serial_request_begin_result_t deck_serial_request_begin(
    deck_serial_request_tracker_t *tracker,
    uint64_t external_request_id,
    uint64_t *service_request_id
)
{
    if (tracker == nullptr || external_request_id == 0 ||
        service_request_id == nullptr) {
        return DECK_SERIAL_REQUEST_INVALID;
    }
    if (tracker->pending) {
        if (tracker->external_request_id != external_request_id) {
            return DECK_SERIAL_REQUEST_BUSY;
        }
        *service_request_id = tracker->service_request_id;
        return DECK_SERIAL_REQUEST_REPLAY;
    }
    tracker->external_request_id = external_request_id;
    tracker->service_request_id = allocate_service_request_id(tracker);
    tracker->pending = true;
    *service_request_id = tracker->service_request_id;
    return DECK_SERIAL_REQUEST_NEW;
}

bool deck_serial_request_complete(
    deck_serial_request_tracker_t *tracker,
    uint64_t service_request_id,
    uint64_t *external_request_id
)
{
    if (tracker == nullptr || external_request_id == nullptr ||
        !tracker->pending || service_request_id == 0 ||
        service_request_id != tracker->service_request_id) {
        return false;
    }
    *external_request_id = tracker->external_request_id;
    tracker->pending = false;
    tracker->external_request_id = 0;
    tracker->service_request_id = 0;
    return true;
}

void deck_serial_request_transport_reset(
    deck_serial_request_tracker_t *tracker
)
{
    if (tracker == nullptr) {
        return;
    }
    tracker->pending = false;
    tracker->external_request_id = 0;
    tracker->service_request_id = 0;
}
