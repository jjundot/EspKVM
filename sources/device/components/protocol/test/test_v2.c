#include "ek_crypto.h"
#include "message_validate.h"
#include "pb_decode.h"
#include "pb_encode.h"
#include "psa/crypto.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
typedef struct {
    char name[32];
    uint8_t data[540];
    size_t n;
} vector;
static vector vectors[32];
static size_t count;
static vector *get(const char *name) {
    for (size_t i = 0; i < count; i++)
        if (!strcmp(vectors[i].name, name))
            return &vectors[i];
    assert(0);
    return NULL;
}
int main(int argc, char **argv) {
    assert(argc == 2);
    assert(psa_crypto_init() == PSA_SUCCESS);
    FILE *f = fopen(argv[1], "r");
    assert(f);
    char hex[1081];
    while (count < 32 && fscanf(f, "%31s %1080s", vectors[count].name, hex) == 2) {
        vector *v = &vectors[count++];
        v->n = strlen(hex) / 2;
        for (size_t i = 0; i < v->n; i++) {
            unsigned x;
            assert(sscanf(hex + 2 * i, "%2x", &x) == 1);
            v->data[i] = (uint8_t)x;
        }
    }
    fclose(f);
    uint8_t out[540];
    assert(ek_hash(get("challenge_frame")->data, get("challenge_frame")->n, out));
    assert(!memcmp(out, get("transcript")->data, 32));
    assert(ek_auth_context(out, get("owner")->data, true, out));
    assert(!memcmp(out, get("context")->data, 32));
    const char *labels[] = {"EK-AUTH-C", "EK-AUTH-D", "EK-KEY"};
    const char *names[] = {"proof_c", "proof_d", "key"};
    for (unsigned i = 0; i < 3; i++) {
        assert(
            ek_proof(get("psk")->data, get("context")->data, get("nonce")->data, labels[i], out));
        assert(!memcmp(out, get(names[i])->data, get(names[i])->n));
    }
    for (size_t i = 0; i < count; i++)
        if (!strncmp(vectors[i].name, "pb_", 3)) {
            ek_Msg m = ek_Msg_init_zero;
            pb_istream_t in = pb_istream_from_buffer(vectors[i].data, vectors[i].n);
            assert(pb_decode(&in, ek_Msg_fields, &m));
            pb_ostream_t encoded = pb_ostream_from_buffer(out, sizeof(out));
            assert(pb_encode(&encoded, ek_Msg_fields, &m));
            assert(encoded.bytes_written == vectors[i].n &&
                   !memcmp(out, vectors[i].data, vectors[i].n));
            if (!strcmp(vectors[i].name, "pb_password_empty"))
                assert(m.body.cfg_set.has_password && !m.body.cfg_set.has_psk);
        }
    const char *frames[] = {"release_frame", "mouse_frame"}, *bodies[] = {"pb_release", "pb_mouse"};
    for (unsigned i = 0; i < 2; i++) {
        ek_cipher c = {.direction = i};
        memcpy(c.key, get("key")->data, 16);
        size_t n;
        assert(ek_seal(&c, get(bodies[i])->data, get(bodies[i])->n, out, &n));
        assert(n == get(frames[i])->n && !memcmp(out, get(frames[i])->data, n));
        c.seq = 0;
        assert(ek_open(&c, out, out + 12, n - 12, out + 12, &n));
        assert(n == get(bodies[i])->n && !memcmp(out + 12, get(bodies[i])->data, n));
        assert(!ek_open(&c, get(frames[i])->data, get(frames[i])->data + 12, get(frames[i])->n - 12,
                        out, &n));
        c.seq = 0;
        memcpy(out, get(frames[i])->data, get(frames[i])->n);
        out[get(frames[i])->n - 1] ^= 1;
        uint8_t plain[512];
        assert(!ek_open(&c, out, out + 12, get(frames[i])->n - 12, plain, &n));
    }
    ek_Msg m = ek_Msg_init_zero;
    m.which_body = ek_Msg_auth_tag;
    assert(!message_validate(&m, EK_AUTH));
    m.body.auth.nonce_c.size = 16;
    m.body.auth.hmac.size = 32;
    m.body.auth.owner.size = 16;
    assert(message_validate(&m, EK_AUTH));
    assert(!message_validate(&m, EK_TO_DEVICE));
    m = (ek_Msg)ek_Msg_init_zero;
    m.which_body = ek_Msg_status_tag;
    assert(!message_validate(&m, EK_TO_DEVICE));
    assert(message_validate(&m, EK_TO_CLIENT));
    puts("C protobuf and PSA crypto v2 vectors passed");
    return 0;
}
