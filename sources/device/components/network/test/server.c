#include "config_store.h"
#include "hid_service.h"
#include "network_service.h"
#include "psa/crypto.h"
#include <assert.h>
#include <stdatomic.h>
#include <string.h>
#include <unistd.h>
static atomic_uint generation;
static ek_Config saved;
uint32_t hid_release(void) { return atomic_fetch_add(&generation, 1) + 1; }
bool hid_ready(uint32_t gen) { return atomic_load(&generation) == gen; }
bool hid_healthy(uint32_t gen) { return hid_ready(gen); }
bool hid_mounted(void) { return true; }
uint8_t hid_leds(void) { return 0; }
void hid_host_activity(uint32_t idle_s, bool known) { (void)idle_s; (void)known; }
void hid_host_disconnected(void) {}
uint32_t hid_keep_awake_status(uint32_t *moves) { *moves = 0; return 0; }
void hid_wake_host(void) {}
bool hid_submit(const ek_Msg *m, uint32_t gen) {
    (void)m;
    return hid_ready(gen);
}
bool config_get(ek_Config *out, bool redact) {
    *out = saved;
    if (redact) {
        out->has_password = false;
        memset(out->password, 0, sizeof(out->password));
        out->has_psk = false;
        memset(&out->psk, 0, sizeof(out->psk));
    }
    return true;
}
ek_ConfigResult config_save(const ek_Config *c) {
    (void)c;
    return (ek_ConfigResult){.code = 1, .field = 5};
}
int main(void) {
    assert(psa_crypto_init() == PSA_SUCCESS);
    saved = (ek_Config)ek_Config_init_zero;
    saved.has_password = true;
    saved.has_psk = true;
    saved.psk.size = 16;
    strcpy(saved.name, "native firmware test");
    strcpy(saved.ssid, "test");
    saved.usb_vid = 1;
    saved.usb_pid = 1;
    saved.ip = 0xc0a801c8;
    saved.gw = 0xc0a80101;
    saved.mask = 0xffffff00;
    saved.tcp_port = 24800;
    ek_Config network = saved;
    network.tcp_port = 0;
    assert(network_service_start(&network) == ESP_OK);
    for (;;)
        pause();
}
