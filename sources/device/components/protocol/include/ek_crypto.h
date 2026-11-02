#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
typedef struct { uint8_t key[16], direction; uint32_t seq; } ek_cipher;
bool ek_hash(const uint8_t *data, size_t n, uint8_t out[32]);
bool ek_auth_context(const uint8_t transcript[32], const uint8_t owner[16], bool takeover,
                     uint8_t out[32]);
bool ek_proof(const uint8_t psk[16], const uint8_t context[32], const uint8_t nonce[16],
              const char *label, uint8_t out[32]);
bool ek_equal(const uint8_t *a, const uint8_t *b, size_t n);
void ek_zero(void *p, size_t n);
bool ek_seal(ek_cipher *c, const uint8_t *body, size_t n, uint8_t frame[540], size_t *size);
bool ek_open(ek_cipher *c, const uint8_t header[12], const uint8_t *body, size_t n,
             uint8_t plain[512], size_t *size);
