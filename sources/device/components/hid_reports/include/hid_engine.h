#pragma once
#include "hid_reports.h"
enum { HID_KEYBOARD, HID_MOUSE, HID_CONSUMER, HID_FIFO_SIZE = 128 };
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
    unsigned head, count, release_index, interface;
    uint32_t generation, submitted_generation;
    uint32_t epoch, submitted_epoch;
    bool mounted, pending_release, in_flight, barrier_report, six_kro, failed;
    uint64_t submitted_at;
    int64_t x, y, wheel, pan;
    uint8_t report[33];
    size_t report_size;
    keyboard_state keyboard;
    uint8_t buttons;
    uint16_t consumer;
    bool rebuild;
} hid_engine;
void hid_engine_release(hid_engine *e, uint32_t generation);
void hid_engine_mount(hid_engine *e, bool mounted);
void hid_engine_protocol(hid_engine *e, bool six_kro, uint64_t now);
bool hid_engine_offer(hid_engine *e, const hid_event *event, uint32_t generation, uint64_t now);
bool hid_engine_next(hid_engine *e, uint64_t now);
void hid_engine_complete(hid_engine *e, bool success);
bool hid_engine_ready(const hid_engine *e);
