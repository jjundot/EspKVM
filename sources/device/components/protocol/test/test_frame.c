#include "frame.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
static unsigned received;
static void callback(uint32_t seq, const uint8_t *body, size_t n, void *ctx) {
    (void)ctx;
    assert(seq);
    assert(n);
    assert(body);
    received++;
}
int main(int argc, char **argv) {
    assert(argc == 2);
    assert(frame_crc32((const uint8_t *)"123456789", 9) == 0xcbf43926u);
    FILE *f = fopen(argv[1], "r");
    assert(f);
    char name[64], hex[1057];
    while (fscanf(f, "%63s %1056s", name, hex) == 2) {
        size_t n = strlen(hex) / 2;
        uint8_t raw[528];
        assert(n <= 528);
        for (size_t i = 0; i < n; i++) {
            unsigned v;
            assert(sscanf(hex + 2 * i, "%2x", &v) == 1);
            raw[i] = (uint8_t)v;
        }
        frame_header h;
        assert(frame_header_decode(raw, 2, &h));
        uint8_t encoded[12];
        assert(frame_header_encode(encoded, &h));
        assert(!memcmp(encoded, raw, 12));
        serial_parser p = {0};
        unsigned before = received;
        for (size_t i = 0; i < n; i++)
            serial_parser_feed(&p, raw + i, 1, 0, callback, NULL);
        assert(received == before + 1);
        // Noise and fragmented valid frames recover without a parser reset.
        const uint8_t noise[] = {0, 'E', 'K', 'E', 0xff};
        serial_parser_feed(&p, noise, sizeof(noise), 0, callback, NULL);
        before = received;
        serial_parser_feed(&p, raw, n, 0, callback, NULL);
        assert(received == before + 1);
        // An incomplete outer frame must not hide a nested valid frame forever.
        p = (serial_parser){0};
        uint8_t outer[12];
        frame_header incomplete = {.len = 512, .flags = 2, .seq = 99};
        assert(frame_header_encode(outer, &incomplete));
        serial_parser_feed(&p, outer, sizeof(outer), 0, callback, NULL);
        before = received;
        serial_parser_feed(&p, raw, n, 500, callback, NULL);
        serial_parser_feed(&p, NULL, 0, 1000, callback, NULL);
        assert(received == before + 1);
        // A truncated frame expires; a later frame is accepted.
        p = (serial_parser){0};
        serial_parser_feed(&p, raw, 8, 0, callback, NULL);
        before = received;
        serial_parser_feed(&p, raw, n, 1000, callback, NULL);
        assert(received == before + 1);
        raw[n - 1] ^= 1;
        p = (serial_parser){0};
        before = received;
        serial_parser_feed(&p, raw, n, 0, callback, NULL);
        assert(received == before);
    }
    fclose(f);
    assert(received);
    puts("shared C wire vectors passed");
    return 0;
}
