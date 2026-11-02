#include "frame.h"
#include <string.h>
static uint32_t get32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static void put32(uint8_t *p, uint32_t v) {
    for (int i = 0; i < 4; i++)
        p[i] = (uint8_t)(v >> (8 * i));
}
bool frame_header_encode(uint8_t b[12], const frame_header *h) {
    if (!h || !h->len || h->len > 512 || h->flags > 2 || (!h->flags && h->seq) ||
        (h->flags == 2 && !h->seq))
        return false;
    memcpy(b, "EKVM", 4);
    b[4] = (uint8_t)h->len;
    b[5] = (uint8_t)(h->len >> 8);
    b[6] = h->flags;
    b[7] = 0;
    put32(b + 8, h->seq);
    return true;
}
bool frame_header_decode(const uint8_t b[12], uint8_t flags, frame_header *h) {
    if (memcmp(b, "EKVM", 4) || b[6] != flags || b[7])
        return false;
    h->len = (uint16_t)(b[4] | ((uint16_t)b[5] << 8));
    h->flags = b[6];
    h->seq = get32(b + 8);
    uint8_t check[12];
    return frame_header_encode(check, h);
}
uint32_t frame_crc32(const uint8_t *p, size_t n) {
    uint32_t crc = 0xffffffffu;
    while (n--) {
        crc ^= *p++;
        for (int i = 0; i < 8; i++)
            crc = (crc >> 1) ^ ((0u - (crc & 1u)) & 0xedb88320u);
    }
    return crc ^ 0xffffffffu;
}
static void discard(serial_parser *p, size_t n) {
    p->len -= n;
    memmove(p->data, p->data + n, p->len);
    memmove(p->arrived, p->arrived + n, p->len * sizeof(uint64_t));
    memset(p->data + p->len, 0, n);
}
static void poll(serial_parser *p, uint64_t now, serial_frame_callback cb, void *ctx) {
    while (p->len >= 4) {
        if (memcmp(p->data, "EKVM", 4) || now - p->arrived[0] >= 1000) {
            discard(p, 1);
            continue;
        }
        if (p->len < 12)
            return;
        frame_header h;
        if (!frame_header_decode(p->data, 2, &h)) {
            discard(p, 1);
            continue;
        }
        size_t total = 12 + h.len + 4;
        if (p->len < total)
            return;
        if (frame_crc32(p->data, total - 4) != get32(p->data + total - 4)) {
            discard(p, 1);
            continue;
        }
        cb(h.seq, p->data + 12, h.len, ctx);
        discard(p, total);
    }
}
void serial_parser_feed(serial_parser *p, const uint8_t *data, size_t n, uint64_t now,
                        serial_frame_callback cb, void *ctx) {
    poll(p, now, cb, ctx);
    for (size_t i = 0; i < n; i++) {
        if (p->len >= sizeof(p->data))
            discard(p, 1);
        p->data[p->len] = data[i];
        p->arrived[p->len++] = now;
        poll(p, now, cb, ctx);
    }
}
