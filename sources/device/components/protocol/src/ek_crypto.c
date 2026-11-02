#include "ek_crypto.h"
#include "frame.h"
#include "psa/crypto.h"
#include <string.h>
void ek_zero(void *p, size_t n) {
    volatile uint8_t *v = p;
    while (n--)
        *v++ = 0;
}
bool ek_equal(const uint8_t *a, const uint8_t *b, size_t n) {
    volatile uint8_t diff = 0;
    while (n--)
        diff |= *a++ ^ *b++;
    return diff == 0;
}
bool ek_hash(const uint8_t *data, size_t n, uint8_t out[32]) {
    size_t size;
    return psa_hash_compute(PSA_ALG_SHA_256, data, n, out, 32, &size) == PSA_SUCCESS && size == 32;
}
bool ek_auth_context(const uint8_t transcript[32], const uint8_t owner[16], bool takeover,
                     uint8_t out[32]) {
    uint8_t data[59];
    memcpy(data, "EK-AUTH-V2", 10);
    memcpy(data + 10, transcript, 32);
    memcpy(data + 42, owner, 16);
    data[58] = takeover ? 1 : 0;
    return ek_hash(data, sizeof(data), out);
}
bool ek_proof(const uint8_t psk[16], const uint8_t context[32], const uint8_t nonce[16],
              const char *label, uint8_t out[32]) {
    size_t length = strlen(label), size = 0;
    if (length > 16)
        return false;
    uint8_t data[64];
    memcpy(data, label, length);
    memcpy(data + length, context, 32);
    memcpy(data + length + 32, nonce, 16);
    psa_key_attributes_t attr = PSA_KEY_ATTRIBUTES_INIT;
    psa_set_key_type(&attr, PSA_KEY_TYPE_HMAC);
    psa_set_key_bits(&attr, 128);
    psa_set_key_usage_flags(&attr, PSA_KEY_USAGE_SIGN_MESSAGE);
    psa_set_key_algorithm(&attr, PSA_ALG_HMAC(PSA_ALG_SHA_256));
    mbedtls_svc_key_id_t key = 0;
    psa_status_t e = psa_import_key(&attr, psk, 16, &key);
    if (e == PSA_SUCCESS)
        e = psa_mac_compute(key, PSA_ALG_HMAC(PSA_ALG_SHA_256), data, length + 48, out, 32, &size);
    psa_destroy_key(key);
    psa_reset_key_attributes(&attr);
    ek_zero(data, sizeof(data));
    return e == PSA_SUCCESS && size == 32;
}
static bool crypt(ek_cipher *c, bool encrypt, const uint8_t header[12], const uint8_t *body,
                  size_t n, uint8_t *out, size_t capacity, size_t *size) {
    if (c->direction > 1 || c->seq >= 0xffffff00u)
        return false;
    uint8_t nonce[12] = {0};
    nonce[0] = c->direction;
    for (unsigned i = 0; i < 4; i++)
        nonce[4 + i] = (uint8_t)(c->seq >> (8 * i));
    psa_key_attributes_t attr = PSA_KEY_ATTRIBUTES_INIT;
    psa_set_key_type(&attr, PSA_KEY_TYPE_AES);
    psa_set_key_bits(&attr, 128);
    psa_set_key_algorithm(&attr, PSA_ALG_GCM);
    psa_set_key_usage_flags(&attr, encrypt ? PSA_KEY_USAGE_ENCRYPT : PSA_KEY_USAGE_DECRYPT);
    mbedtls_svc_key_id_t key = 0;
    psa_status_t e = psa_import_key(&attr, c->key, 16, &key);
    if (e == PSA_SUCCESS) {
        if (encrypt)
            e = psa_aead_encrypt(key, PSA_ALG_GCM, nonce, 12, header, 12, body, n, out, capacity,
                                 size);
        else
            e = psa_aead_decrypt(key, PSA_ALG_GCM, nonce, 12, header, 12, body, n, out, capacity,
                                 size);
    }
    psa_destroy_key(key);
    psa_reset_key_attributes(&attr);
    if (e != PSA_SUCCESS)
        return false;
    c->seq++;
    return true;
}
bool ek_seal(ek_cipher *c, const uint8_t *body, size_t n, uint8_t frame[540], size_t *size) {
    if (!n || n > 512)
        return false;
    frame_header h = {.len = (uint16_t)n, .flags = 1, .seq = c->seq};
    if (!frame_header_encode(frame, &h) || !crypt(c, true, frame, body, n, frame + 12, 528, size))
        return false;
    *size += 12;
    return true;
}
bool ek_open(ek_cipher *c, const uint8_t header[12], const uint8_t *body, size_t n,
             uint8_t plain[512], size_t *size) {
    frame_header h;
    if (!frame_header_decode(header, 1, &h) || h.seq != c->seq || n != h.len + 16u)
        return false;
    return crypt(c, false, header, body, n, plain, 512, size);
}
