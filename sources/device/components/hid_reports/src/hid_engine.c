#include "hid_engine.h"
#include <limits.h>
#include <string.h>
void hid_engine_release(hid_engine *e, uint32_t generation) {
    e->epoch++;
    e->keyboard = (keyboard_state){0};
    e->buttons = 0;
    e->consumer = 0;
    e->rebuild = false;
    e->generation = generation;
    e->head = e->count = 0;
    e->pending_release = true;
    e->release_index = 0;
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
    e->rebuild = true;
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
bool hid_engine_ready(const hid_engine *e) {
    return e->mounted && !e->pending_release && !e->failed;
}
static bool add(int64_t a, int64_t b) {
    return !((b > 0 && a > INT64_MAX - b) || (b < 0 && a < INT64_MIN - b));
}
bool hid_engine_offer(hid_engine *e, const hid_event *v, uint32_t generation, uint64_t now) {
    if (!e->mounted || e->failed || (e->pending_release && !e->rebuild) ||
        e->generation != generation || v->kind > HID_CONSUMER)
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
static int64_t part(int64_t n, int64_t max) { return n > max ? max : n < -max ? -max : n; }
bool hid_engine_next(hid_engine *e, uint64_t now) {
    if (e->in_flight) {
        if (now - e->submitted_at >= 100 && !e->failed) {
            e->failed = true;
            hid_engine_release(e, e->generation + 1);
        }
        return false;
    }
    if (!e->mounted)
        return false;
    if (e->count && now - e->fifo[e->head].at >= 100) {
        e->failed = true;
        hid_engine_release(e, e->generation + 1);
    }
    e->barrier_report = e->pending_release;
    e->x = e->y = e->wheel = e->pan = 0;
    if (e->pending_release) {
        memset(e->report, 0, sizeof(e->report));
        e->interface = e->release_index;
        e->report_size = e->interface == HID_KEYBOARD ? (e->six_kro ? 8 : 33)
                         : e->interface == HID_MOUSE  ? 7
                                                      : 2;
    } else {
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
        case HID_CONSUMER:
            consumer_report(v->value.consumer, e->report);
            e->report_size = 2;
            break;
        default:
            return false;
        }
    }
    e->submitted_generation = e->generation;
    e->submitted_epoch = e->epoch;
    e->submitted_at = now;
    e->in_flight = true;
    return true;
}
void hid_engine_complete(hid_engine *e, bool success) {
    if (!e->in_flight)
        return;
    e->in_flight = false;
    if (!success || e->submitted_generation != e->generation || e->submitted_epoch != e->epoch)
        return;
    if (e->barrier_report) {
        if (++e->release_index == 3) {
            e->pending_release = false;
            e->failed = false;
            e->rebuild = false;
        }
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
