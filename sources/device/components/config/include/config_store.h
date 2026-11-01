#pragma once
#include "ek.pb.h"
#include "esp_err.h"
esp_err_t config_init(void);
bool config_get(ek_Config *out, bool redact);
uint32_t config_validate(const ek_Config *candidate, bool paired);
ek_ConfigResult config_save(const ek_Config *candidate);
esp_err_t config_reset(void);
