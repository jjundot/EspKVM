#pragma once
#include "hid_reports.h"
enum { HID_KEYBOARD, HID_MOUSE, HID_CONSUMER, HID_FIFO_SIZE = 128 };
// What the in-flight transfer carries; only FIFO transfers consume queued input.
enum { HID_SUBMIT_FIFO, HID_SUBMIT_BARRIER, HID_SUBMIT_REPEAT, HID_SUBMIT_REFRESH };
typedef struct {
    unsigned kind;
    uint64_t at;
    union {
        keyboard_state keyboard;
        struct {
            uint8_t buttons;
            int64_t x, y, wheel, pan;
        } mouse;
        uint16_t consumer;
    } value;
} hid_event;
typedef struct {
    hid_event fifo[HID_FIFO_SIZE];
    unsigned head, count, release_index, interface, submitted_kind;
    uint32_t generation, submitted_generation;
    uint32_t epoch, submitted_epoch;
    bool mounted, pending_release, in_flight, six_kro, failed;
    // usable: the host engaged the interface; stalled: its endpoint stopped
    // completing; refresh: the host may hold a stale report and needs current state.
    bool usable[3], stalled[3], refresh[3];
    uint64_t submitted_at;
    int64_t x, y, wheel, pan;
    uint8_t report[33];
    size_t report_size;
    keyboard_state keyboard;
    uint8_t buttons;
    uint16_t consumer;
} hid_engine;
void hid_engine_release(hid_engine *e, uint32_t generation);
void hid_engine_mount(hid_engine *e, bool mounted);
void hid_engine_attach(hid_engine *e);
void hid_engine_detach(hid_engine *e);
void hid_engine_suspend(hid_engine *e);
void hid_engine_resume(hid_engine *e);
void hid_engine_use(hid_engine *e, unsigned interface);
void hid_engine_protocol(hid_engine *e, bool six_kro, uint64_t now);
bool hid_engine_offer(hid_engine *e, const hid_event *event, uint32_t generation, uint64_t now);
bool hid_engine_next(hid_engine *e, uint64_t now);
bool hid_engine_repeat(hid_engine *e, unsigned interface, const uint8_t *report, size_t size,
                       uint64_t now);
void hid_engine_complete(hid_engine *e, bool success);
void hid_engine_report_done(hid_engine *e, unsigned interface);
bool hid_engine_ready(const hid_engine *e);
