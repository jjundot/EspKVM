#include "hid_engine.h"
#include <limits.h>
#include <string.h>
static bool deliverable(const hid_engine *e, unsigned interface) {
    return e->usable[interface] && !e->stalled[interface];
}
// Interfaces the host never engaged, or no longer polls, already show (or will
// be refreshed to) the current state; the barrier cannot wait on them forever.
static void advance_release(hid_engine *e) {
    while (e->release_index < 3 && !deliverable(e, e->release_index))
        e->release_index++;
    if (e->release_index == 3) {
        e->pending_release = false;
        e->failed = false;
    }
}
void hid_engine_release(hid_engine *e, uint32_t generation) {
    e->epoch++;
    e->keyboard = (keyboard_state){0};
    e->buttons = 0;
    e->consumer = 0;
    e->generation = generation;
    e->head = e->count = 0;
    e->pending_release = true;
    e->release_index = 0;
    memset(e->refresh, 0, sizeof(e->refresh));
}
void hid_engine_protocol(hid_engine *e, bool six_kro, uint64_t now) {
    if (e->six_kro == six_kro)
        return;
    keyboard_state keyboard = e->keyboard;
    uint8_t buttons = e->buttons;
    uint16_t consumer = e->consumer;
    hid_engine_release(e, e->generation);
    e->six_kro = six_kro;
    e->keyboard = keyboard;
    e->buttons = buttons;
    e->consumer = consumer;
    e->fifo[0] = (hid_event){.kind = HID_KEYBOARD, .at = now, .value.keyboard = keyboard};
    e->fifo[1] = (hid_event){.kind = HID_MOUSE, .at = now, .value.mouse.buttons = buttons};
    e->fifo[2] = (hid_event){.kind = HID_CONSUMER, .at = now, .value.consumer = consumer};
    e->head = 0;
    e->count = 3;
}
void hid_engine_mount(hid_engine *e, bool mounted) {
    e->mounted = mounted;
    if (!mounted)
        e->in_flight = false; // Endpoint teardown cancels the transfer.
    hid_engine_release(e, e->generation + 1);
}
static void reset_transport(hid_engine *e, bool mounted, bool enumeration) {
    e->mounted = mounted;
    e->in_flight = false;
    e->epoch++;
    e->head = e->count = 0;
    e->keyboard = (keyboard_state){0};
    e->buttons = 0;
    e->consumer = 0;
    e->failed = false;
    e->pending_release = true;
    e->release_index = 0;
    if (enumeration)
        memset(e->usable, 0, sizeof(e->usable));
    memset(e->stalled, 0, sizeof(e->stalled));
    memset(e->refresh, 0, sizeof(e->refresh));
}
void hid_engine_attach(hid_engine *e) { reset_transport(e, true, true); }
void hid_engine_detach(hid_engine *e) { reset_transport(e, false, true); }
void hid_engine_suspend(hid_engine *e) { reset_transport(e, false, false); }
void hid_engine_resume(hid_engine *e) { reset_transport(e, true, false); }
void hid_engine_use(hid_engine *e, unsigned interface) {
    if (interface >= 3 || e->usable[interface])
        return;
    e->usable[interface] = true;
    // A pending barrier will cover it; otherwise announce the current state.
    if (!e->pending_release)
        e->refresh[interface] = true;
}
bool hid_engine_ready(const hid_engine *e) {
    return e->mounted && !e->pending_release && !e->failed;
}
static bool add(int64_t a, int64_t b) {
    return !((b > 0 && a > INT64_MAX - b) || (b < 0 && a < INT64_MIN - b));
}
// Input of the current generation may queue behind a transport barrier (USB
// attach/resume); the barrier is still submitted and completed before it.
bool hid_engine_offer(hid_engine *e, const hid_event *v, uint32_t generation, uint64_t now) {
    if (!e->mounted || e->failed || e->generation != generation || v->kind > HID_CONSUMER)
        return false;
    if (e->count && now - e->fifo[e->head].at >= 100)
        goto fault;
    // Only consecutive, not-yet-submitted movement in the same button segment can merge.
    if (v->kind == HID_MOUSE && e->count && !(e->count == 1 && e->in_flight)) {
        hid_event *last = &e->fifo[(e->head + e->count - 1) % HID_FIFO_SIZE];
        if (last->kind == HID_MOUSE && last->value.mouse.buttons == v->value.mouse.buttons) {
            if (!add(last->value.mouse.x, v->value.mouse.x) ||
                !add(last->value.mouse.y, v->value.mouse.y) ||
                !add(last->value.mouse.wheel, v->value.mouse.wheel) ||
                !add(last->value.mouse.pan, v->value.mouse.pan))
                goto fault;
            last->value.mouse.x += v->value.mouse.x;
            last->value.mouse.y += v->value.mouse.y;
            last->value.mouse.wheel += v->value.mouse.wheel;
            last->value.mouse.pan += v->value.mouse.pan;
            return true;
        }
    }
    if (e->count == HID_FIFO_SIZE)
        goto fault;
    e->fifo[(e->head + e->count) % HID_FIFO_SIZE] = *v;
    e->fifo[(e->head + e->count) % HID_FIFO_SIZE].at = now;
    e->count++;
    if (v->kind == HID_KEYBOARD)
        e->keyboard = v->value.keyboard;
    else if (v->kind == HID_MOUSE)
        e->buttons = v->value.mouse.buttons;
    else
        e->consumer = v->value.consumer;
    return true;
fault:
    e->failed = true;
    hid_engine_release(e, e->generation + 1);
    return false;
}
static size_t state_report(const hid_engine *e, unsigned interface, uint8_t report[33]) {
    if (interface == HID_KEYBOARD)
        return keyboard_report(&e->keyboard, e->six_kro, report);
    if (interface == HID_MOUSE) {
        mouse_report(e->buttons, 0, 0, 0, 0, report);
        return 7;
    }
    consumer_report(e->consumer, report);
    return 2;
}
static void submit(hid_engine *e, unsigned kind, uint64_t now) {
    e->submitted_kind = kind;
    e->submitted_generation = e->generation;
    e->submitted_epoch = e->epoch;
    e->submitted_at = now;
    e->in_flight = true;
}
static int64_t part(int64_t n, int64_t max) { return n > max ? max : n < -max ? -max : n; }
bool hid_engine_next(hid_engine *e, uint64_t now) {
    if (e->in_flight) {
        if (now - e->submitted_at < 100)
            return false;
        // The host stopped polling this endpoint (e.g. a BIOS that only reads the
        // boot keyboard). Abandon the transfer instead of blocking every interface.
        e->stalled[e->interface] = true;
        e->in_flight = false;
        if (!e->failed) {
            e->failed = true;
            hid_engine_release(e, e->generation + 1);
        }
    }
    if (!e->mounted)
        return false;
    if (e->count && now - e->fifo[e->head].at >= 100) {
        e->failed = true;
        hid_engine_release(e, e->generation + 1);
    }
    e->x = e->y = e->wheel = e->pan = 0;
    if (e->pending_release)
        advance_release(e);
    if (e->pending_release) {
        memset(e->report, 0, sizeof(e->report));
        e->interface = e->release_index;
        e->report_size = e->interface == HID_KEYBOARD ? (e->six_kro ? 8 : 33)
                         : e->interface == HID_MOUSE  ? 7
                                                      : 2;
        submit(e, HID_SUBMIT_BARRIER, now);
        return true;
    }
    for (unsigned i = 0; i < 3; i++)
        if (e->refresh[i] && deliverable(e, i)) {
            e->interface = i;
            e->report_size = state_report(e, i, e->report);
            submit(e, HID_SUBMIT_REFRESH, now);
            return true;
        }
    // Input for an interface the host does not read can never be delivered.
    while (e->count && !deliverable(e, e->fifo[e->head].kind)) {
        e->head = (e->head + 1) % HID_FIFO_SIZE;
        e->count--;
    }
    if (!e->count)
        return false;
    hid_event *v = &e->fifo[e->head];
    e->interface = v->kind;
    switch (v->kind) {
    case HID_KEYBOARD:
        e->report_size = keyboard_report(&v->value.keyboard, e->six_kro, e->report);
        break;
    case HID_MOUSE:
        e->x = part(v->value.mouse.x, 32767);
        e->y = part(v->value.mouse.y, 32767);
        e->wheel = part(v->value.mouse.wheel, 127);
        e->pan = part(v->value.mouse.pan, 127);
        mouse_report(v->value.mouse.buttons, (int16_t)e->x, (int16_t)e->y, (int8_t)e->wheel,
                     (int8_t)e->pan, e->report);
        e->report_size = 7;
        break;
    default:
        consumer_report(v->value.consumer, e->report);
        e->report_size = 2;
        break;
    }
    submit(e, HID_SUBMIT_FIFO, now);
    return true;
}
// SET_IDLE repeats are transport-only; their completion never consumes the FIFO.
bool hid_engine_repeat(hid_engine *e, unsigned interface, const uint8_t *report, size_t size,
                       uint64_t now) {
    if (e->in_flight || !hid_engine_ready(e) || interface >= 3 || !deliverable(e, interface) ||
        size > sizeof(e->report))
        return false;
    e->interface = interface;
    e->report_size = size;
    memcpy(e->report, report, size);
    e->x = e->y = e->wheel = e->pan = 0;
    submit(e, HID_SUBMIT_REPEAT, now);
    return true;
}
void hid_engine_complete(hid_engine *e, bool success) {
    if (!e->in_flight)
        return;
    e->in_flight = false;
    if (!success || e->submitted_generation != e->generation || e->submitted_epoch != e->epoch)
        return;
    switch (e->submitted_kind) {
    case HID_SUBMIT_BARRIER:
        e->release_index++;
        advance_release(e);
        return;
    case HID_SUBMIT_REFRESH:
        e->refresh[e->interface] = false;
        return;
    case HID_SUBMIT_REPEAT:
        return;
    }
    if (!e->count)
        return;
    hid_event *v = &e->fifo[e->head];
    if (v->kind == HID_MOUSE) {
        v->value.mouse.x -= e->x;
        v->value.mouse.y -= e->y;
        v->value.mouse.wheel -= e->wheel;
        v->value.mouse.pan -= e->pan;
        if (v->value.mouse.x || v->value.mouse.y || v->value.mouse.wheel || v->value.mouse.pan)
            return;
    }
    e->head = (e->head + 1) % HID_FIFO_SIZE;
    e->count--;
}
// USB transfer-complete callback for one interface.
void hid_engine_report_done(hid_engine *e, unsigned interface) {
    if (interface >= 3)
        return;
    bool late = e->stalled[interface];
    e->stalled[interface] = false;
    e->usable[interface] = true;
    if (late)
        e->refresh[interface] = true; // The host just read an abandoned, possibly stale report.
    else if (e->in_flight && e->interface == interface)
        hid_engine_complete(e, true);
}
