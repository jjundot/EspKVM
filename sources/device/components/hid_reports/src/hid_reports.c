#include "hid_reports.h"
#include <string.h>

size_t keyboard_report(const keyboard_state *state, bool six_kro, uint8_t output[33]) {
    memset(output, 0, 33);
    output[0] = state->modifiers;
    if (!six_kro) {
        memcpy(output + 1, state->keys, 32);
        output[1] &= 0xf0; // Error/reserved usages never become normal keys.
        output[29] = 0;    // E0..E7 live exclusively in the modifier byte.
        return 33;
    }
    size_t count = 0;
    for (unsigned usage = 4; usage < 256; usage++) {
        if (usage >= 0xe0 && usage <= 0xe7)
            continue;
        if (!(state->keys[usage / 8] & (1u << (usage % 8))))
            continue;
        if (++count > 6) {
            memset(output + 2, 1, 6); // ErrorRollOver, not an arbitrary subset.
            return 8;
        }
        output[count + 1] = (uint8_t)usage;
    }
    return 8;
}

void mouse_report(uint8_t buttons, int16_t dx, int16_t dy, int8_t wheel, int8_t pan,
                  uint8_t output[7]) {
    output[0] = buttons & 31;
    output[1] = (uint8_t)dx;
    output[2] = (uint8_t)((uint16_t)dx >> 8);
    output[3] = (uint8_t)dy;
    output[4] = (uint8_t)((uint16_t)dy >> 8);
    output[5] = (uint8_t)wheel;
    output[6] = (uint8_t)pan;
}

void consumer_report(uint16_t usage, uint8_t output[2]) {
    output[0] = (uint8_t)usage;
    output[1] = (uint8_t)(usage >> 8);
}
