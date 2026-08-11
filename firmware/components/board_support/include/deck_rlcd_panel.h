#pragma once

#include <stdbool.h>

#include "deck_display.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct deck_rlcd_panel deck_rlcd_panel_t;

deck_rlcd_panel_t *deck_rlcd_panel_create(void);
bool deck_rlcd_panel_initialize(deck_rlcd_panel_t *panel);
deck_display_panel_adapter_t deck_rlcd_panel_adapter(deck_rlcd_panel_t *panel);
void deck_rlcd_panel_destroy(deck_rlcd_panel_t *panel);

#ifdef __cplusplus
}
#endif
