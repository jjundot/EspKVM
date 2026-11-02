#pragma once
#include "esp_err.h"
#include "hid_reports.h"
esp_err_t usb_device_start(bool six_kro, uint16_t vid, uint16_t pid);
bool usb_device_mounted(void);
uint8_t usb_device_leds(void);
bool usb_device_poll(void);
bool usb_test_keyboard(const keyboard_state *state);
bool usb_test_mouse(uint8_t buttons, int16_t dx, int16_t dy, int8_t wheel, int8_t pan);
bool usb_test_release(void);
