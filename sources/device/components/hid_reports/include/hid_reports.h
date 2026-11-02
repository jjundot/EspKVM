#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef struct {
    uint8_t modifiers;
    uint8_t keys[32];
} keyboard_state;

// Report protocol: modifier byte + 256-bit bitmap. Boot/compatibility: 8 bytes.
size_t keyboard_report(const keyboard_state *state, bool six_kro, uint8_t output[33]);
void mouse_report(uint8_t buttons, int16_t dx, int16_t dy, int8_t wheel, int8_t pan,
                  uint8_t output[7]);
void consumer_report(uint16_t usage, uint8_t output[2]);
