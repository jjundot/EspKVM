#pragma once
#include "ek.pb.h"
#include "esp_err.h"
esp_err_t hid_service_start(const ek_Config *config);
uint32_t hid_release(void);
bool hid_ready(uint32_t generation);
bool hid_healthy(uint32_t generation);
bool hid_mounted(void);
uint8_t hid_leds(void);
bool hid_submit(const ek_Msg *msg,uint32_t generation);
