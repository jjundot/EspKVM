#pragma once
#include "ek.pb.h"
#include "esp_err.h"
esp_err_t hid_service_start(const ek_Config *config);
uint32_t hid_release(void);
bool hid_ready(uint32_t generation);
bool hid_healthy(uint32_t generation);
bool hid_mounted(void);
// USB host suspended, or resetting the bus while it wakes; release is already safe.
bool hid_sleeping(void);
uint8_t hid_leds(void);
bool hid_submit(const ek_Msg *msg,uint32_t generation);
void hid_host_activity(uint32_t idle_s, bool known);
void hid_host_disconnected(void);
uint32_t hid_keep_awake_status(uint32_t *moves);
void hid_wake_host(void);
