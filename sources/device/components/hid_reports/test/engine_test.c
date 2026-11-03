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
static void use_all(hid_engine *e) {
    for (unsigned i = 0; i < 3; i++)
        hid_engine_use(e, i);
}
int main(void) {
    hid_engine e = {0};
    hid_engine_mount(&e, true);
    use_all(&e);
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
    hid_engine resumed = {0};
    hid_engine_mount(&resumed, true);
    use_all(&resumed);
    release(&resumed);
    uint32_t connected_generation = resumed.generation;
    hid_engine_detach(&resumed);
    assert(!resumed.mounted && resumed.generation == connected_generation);
    hid_engine_attach(&resumed);
    assert(resumed.mounted && resumed.generation == connected_generation);
    use_all(&resumed); // A new enumeration must engage the interfaces again.
    release(&resumed);
    assert(hid_engine_offer(&resumed, &down, connected_generation, 0));
    assert(hid_engine_next(&resumed, 0));
    hid_engine_complete(&resumed, true);
    hid_engine_suspend(&resumed);
    assert(!resumed.mounted && !hid_engine_ready(&resumed));
    assert(resumed.generation == connected_generation); // Suspend does not invalidate the TCP session.
    hid_engine_resume(&resumed);
    assert(resumed.mounted && resumed.generation == connected_generation && resumed.pending_release);
    release(&resumed);
    assert(hid_engine_ready(&resumed));
    assert(hid_engine_offer(&resumed, &up, connected_generation, 1));
    // A SET_IDLE repeat completion must not consume input queued meanwhile.
    hid_engine idle = {0};
    hid_engine_mount(&idle, true);
    use_all(&idle);
    release(&idle);
    uint8_t zero[33] = {0};
    assert(hid_engine_repeat(&idle, HID_KEYBOARD, zero, 33, 0));
    assert(hid_engine_offer(&idle, &down, idle.generation, 0));
    hid_engine_report_done(&idle, HID_KEYBOARD);
    assert(idle.count == 1);
    assert(hid_engine_next(&idle, 0) && idle.report[0] == 1);
    hid_engine_report_done(&idle, HID_KEYBOARD);
    assert(!idle.count);
    // A BIOS that never engages mouse/consumer cannot block the release barrier.
    hid_engine bios = {0};
    hid_engine_attach(&bios);
    hid_engine_use(&bios, HID_KEYBOARD);
    assert(hid_engine_next(&bios, 0) && bios.interface == HID_KEYBOARD);
    hid_engine_report_done(&bios, HID_KEYBOARD);
    assert(hid_engine_ready(&bios));
    hid_event click = {.kind = HID_MOUSE, .value.mouse.buttons = 1};
    assert(hid_engine_offer(&bios, &click, bios.generation, 0));
    assert(!hid_engine_next(&bios, 0) && !bios.count);
    // An engaged endpoint that stops completing is abandoned and skipped.
    hid_engine stall = {0};
    hid_engine_mount(&stall, true);
    use_all(&stall);
    release(&stall);
    hid_event move = {.kind = HID_MOUSE, .value.mouse.x = 5};
    assert(hid_engine_offer(&stall, &move, stall.generation, 0));
    assert(hid_engine_next(&stall, 0) && stall.interface == HID_MOUSE);
    assert(!hid_engine_next(&stall, 50));
    assert(hid_engine_next(&stall, 100));
    assert(stall.stalled[HID_MOUSE] && stall.pending_release && stall.interface == HID_KEYBOARD);
    hid_engine_report_done(&stall, HID_KEYBOARD);
    assert(hid_engine_next(&stall, 100) && stall.interface == HID_CONSUMER);
    hid_engine_report_done(&stall, HID_CONSUMER);
    assert(hid_engine_ready(&stall));
    hid_engine_report_done(&stall, HID_MOUSE); // Late completion of the abandoned report.
    assert(!stall.stalled[HID_MOUSE] && hid_engine_next(&stall, 101));
    assert(stall.interface == HID_MOUSE && stall.submitted_kind == HID_SUBMIT_REFRESH);
    hid_engine_report_done(&stall, HID_MOUSE);
    assert(!stall.refresh[HID_MOUSE]);
    // Same-generation input queues behind a USB resume barrier.
    hid_engine_suspend(&stall);
    hid_engine_resume(&stall);
    assert(hid_engine_offer(&stall, &down, stall.generation, 200));
    assert(hid_engine_next(&stall, 200) && stall.submitted_kind == HID_SUBMIT_BARRIER);
    puts("HID FIFO, completion, movement and release barrier passed");
    return 0;
}
