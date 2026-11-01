#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#define EK_HDR_LEN 12
#define EK_MAX_BODY 512
#define EK_FLAG_ENC 1
#define EK_FLAG_CRC 2
typedef struct {
    uint16_t len;
    uint8_t flags;
    uint32_t seq;
} frame_header;
bool frame_header_decode(const uint8_t raw[12], uint8_t flags, frame_header *out);
bool frame_header_encode(uint8_t raw[12], const frame_header *h);
uint32_t frame_crc32(const uint8_t *bytes, size_t len);
typedef void (*serial_frame_callback)(uint32_t seq, const uint8_t *body, size_t size, void *ctx);
typedef struct {
    uint8_t data[528];
    uint64_t arrived[528];
    size_t len;
} serial_parser;
void serial_parser_feed(serial_parser *p, const uint8_t *bytes, size_t n, uint64_t now_ms,
                        serial_frame_callback callback, void *ctx);
