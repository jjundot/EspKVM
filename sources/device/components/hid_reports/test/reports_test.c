#include "hid_reports.h"
#include <assert.h>
#include <string.h>

int main(void) {
    keyboard_state state = {.modifiers = 0x11};
    uint8_t report[33];
    state.keys[0] = 1u << 4;
    assert(keyboard_report(&state, true, report) == 8);
    assert(report[0] == 0x11 && report[1] == 0 && report[2] == 4 && report[7] == 0);
    state.keys[1] = 0xff;
    assert(keyboard_report(&state, true, report) == 8);
    for (unsigned i = 2; i < 8; i++)
        assert(report[i] == 1);
    state.keys[0] |= 15;
    state.keys[28] = 0xff;
    assert(keyboard_report(&state, false, report) == 33);
    assert(report[1] == 16 && report[29] == 0 && report[0] == 0x11);
    state = (keyboard_state){0};
    keyboard_report(&state, false, report);
    for (unsigned i = 0; i < 33; i++)
        assert(report[i] == 0);
    mouse_report(0xff, -32767, 32767, -127, 127, report);
    const uint8_t expected[] = {31, 1, 128, 255, 127, 129, 127};
    assert(!memcmp(report, expected, sizeof(expected)));
    consumer_report(0xea, report);
    assert(report[0] == 0xea && report[1] == 0);
    return 0;
}
