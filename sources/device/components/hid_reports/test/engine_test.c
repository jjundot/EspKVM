#include "hid_engine.h"
#include <assert.h>
#include <stdio.h>
static void release(hid_engine *e) {
    for (unsigned i = 0; i < 3; i++) {
        assert(hid_engine_next(e, 0));
        assert(e->interface == i);
        hid_engine_complete(e, true);
    }
    assert(hid_engine_ready(e));
}
int main(void) {
    hid_engine e = {0};
    hid_engine_mount(&e, true);
    release(&e);
    hid_event down = {.kind = HID_KEYBOARD, .value.keyboard.modifiers = 1},
              up = {.kind = HID_KEYBOARD};
    assert(hid_engine_offer(&e, &down, e.generation, 0));
    assert(hid_engine_offer(&e, &up, e.generation, 0));
    assert(hid_engine_next(&e, 0));
    assert(e.report[0] == 1);
    assert(e.count == 2);
    assert(!hid_engine_next(&e, 0));
    hid_engine_complete(&e, true);
    assert(hid_engine_next(&e, 0));
    assert(e.report[0] == 0);
    hid_engine_complete(&e, true);
    assert(!e.count);
    hid_event m = {.kind = HID_MOUSE, .value.mouse = {.buttons = 1, .x = 70000, .wheel = -300}};
    assert(hid_engine_offer(&e, &m, e.generation, 0));
    int64_t x = 0, w = 0;
    while (e.count) {
        assert(hid_engine_next(&e, 0));
        x += e.x;
        w += e.wheel;
        hid_engine_complete(&e, true);
    }
    assert(x == 70000 && w == -300);
    assert(hid_engine_offer(&e, &down, e.generation, 0));
    assert(hid_engine_next(&e, 0));
    uint32_t old = e.generation;
    hid_engine_release(&e, old + 1);
    assert(!hid_engine_offer(&e, &down, old, 0));
    assert(!hid_engine_next(&e, 0));
    hid_engine_complete(&e, true);
    release(&e);
    assert(!e.count);
    for (unsigned i = 0; i < 128; i++)
        assert(hid_engine_offer(&e, i % 2 ? &up : &down, e.generation, 0));
    assert(!hid_engine_offer(&e, &down, e.generation, 0));
    assert(e.pending_release && !e.count);
    release(&e);
    assert(hid_engine_offer(&e, &down, e.generation, 0));
    assert(hid_engine_next(&e, 100));
    assert(e.pending_release);
    hid_engine_complete(&e, true);
    hid_engine_mount(&e, false);
    assert(!hid_engine_next(&e, 101));
    assert(e.pending_release);
    hid_engine_mount(&e, true);
    release(&e);
    assert(hid_engine_offer(&e, &down, e.generation, 0));
    assert(hid_engine_next(&e, 0));
    uint32_t generation = e.generation;
    hid_engine_protocol(&e, true, 0);
    hid_engine_complete(&e, true); // Old-format completion cannot advance the new barrier.
    assert(hid_engine_offer(&e, &up, e.generation, 0));
    release(&e);
    assert(e.generation == generation && e.count == 4);
    assert(hid_engine_next(&e, 0));
    assert(e.report_size == 8 && e.report[0] == 1);
    hid_engine_complete(&e, true);
    while (e.count) {
        assert(hid_engine_next(&e, 0));
        hid_engine_complete(&e, true);
    }
    assert(e.keyboard.modifiers == 0);
    puts("HID FIFO, completion, movement and release barrier passed");
    return 0;
}
