#include "network_service.h"
#include "config_store.h"
#include "ek_crypto.h"
#include "frame.h"
#include "hid_service.h"
#include "message_validate.h"
#ifdef EK_NETWORK_HOST_TEST
#define EK_FW_VERSION_STRING "native-test"
#include "network_host.h"
#else
#include "ek_build_version.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_mac.h"
#include "esp_netif.h"
#include "esp_random.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "lwip/sockets.h"
#include "lwip/tcp.h"
#endif
#include "pb_decode.h"
#include "pb_encode.h"
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdatomic.h>
#include <string.h>

#ifndef EK_NETWORK_HOST_TEST
static const char *TAG = "ek_network";
#endif

// Four static contexts: one active connection plus at most three handshakes.
typedef struct {
    bool used, authenticated, retired, replaced;
    int fd;
    uint32_t peer, generation, heartbeat;
    int64_t accepted, last_valid, tick;
    uint32_t probe;
    int64_t probe_sent;
    uint32_t release_ids[128],release_last;
    unsigned release_count;
    uint8_t owner[16], raw[540], plain[512], wire[540];
    ek_Msg msg;
    ek_cipher tx, rx;
} connection;
static connection slots[4];
static connection *active;
static StaticSemaphore_t mutex_storage;
static SemaphoreHandle_t state_lock;
static void lock_state(void) { xSemaphoreTake(state_lock, portMAX_DELAY); }
static void unlock_state(void) { xSemaphoreGive(state_lock); }
static uint8_t psk[16], mac[6];
static char name[32];
static uint16_t port;
static atomic_bool online;
static int listener = -1;
static int64_t now_ms(void) { return esp_timer_get_time() / 1000; }
static int64_t minimum(int64_t a, int64_t b) { return a < b ? a : b; }
// Writes and reads never hold state_lock. Shutdown is protected against FD reuse.
static int wait_fd(int fd, bool write, int64_t deadline) {
    for (;;) {
        int64_t left = deadline - now_ms();
        if (left <= 0)
            return 0;
        struct timeval timeout = {.tv_sec = left / 1000, .tv_usec = (left % 1000) * 1000};
        fd_set set;
        FD_ZERO(&set);
        FD_SET(fd, &set);
        int n = select(fd + 1, write ? NULL : &set, write ? &set : NULL, NULL, &timeout);
        if (n < 0 && errno == EINTR)
            continue;
        return n;
    }
}
static bool exact(int fd, uint8_t *out, size_t n, int64_t deadline) {
    while (n) {
        if (wait_fd(fd, false, deadline) <= 0)
            return false;
        int got = recv(fd, out, n, 0);
        if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR))
            continue;
        if (got <= 0)
            return false;
        out += got;
        n -= (size_t)got;
    }
    return true;
}
static bool write_frame(connection *c, const uint8_t *data, size_t n, int64_t deadline) {
    while (n) {
        if (wait_fd(c->fd, true, deadline) <= 0)
            return false;
        int sent = send(c->fd, data, n, 0);
        if (sent < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR))
            continue;
        if (sent <= 0)
            return false;
        data += sent;
        n -= (size_t)sent;
    }
    return true;
}
static bool send_msg(connection *c, bool encrypted, int64_t deadline) {
    pb_ostream_t out = pb_ostream_from_buffer(c->plain, 512);
    if (!pb_encode(&out, ek_Msg_fields, &c->msg))
        return false;
    size_t n = out.bytes_written;
    if (encrypted) {
        if (!ek_seal(&c->tx, c->plain, n, c->wire, &n))
            return false;
    } else {
        frame_header h = {.len = (uint16_t)n};
        if (!frame_header_encode(c->wire, &h))
            return false;
        memcpy(c->wire + 12, c->plain, n);
        n += 12;
    }
    return write_frame(c, c->wire, n, minimum(deadline, now_ms() + 100));
}
// 0 means no first byte before the next scheduled heartbeat; -1 closes the stream.
static int receive(connection *c, bool encrypted, int64_t idle) {
    int ready = wait_fd(c->fd, false, idle);
    if (ready <= 0)
        return ready;
    int64_t deadline =
        minimum(encrypted ? c->last_valid + 3000 : c->accepted + 2000, now_ms() + 1000);
    if (!exact(c->fd, c->raw, 12, deadline))
        return -1;
    frame_header h;
    if (!frame_header_decode(c->raw, encrypted ? 1 : 0, &h))
        return -1;
    size_t n = h.len + (encrypted ? 16 : 0);
    if (!exact(c->fd, c->raw + 12, n, deadline))
        return -1;
    const uint8_t *body = c->raw + 12;
    if (encrypted) {
        if (!ek_open(&c->rx, c->raw, body, n, c->plain, &n))
            return -1;
        body = c->plain;
    }
    c->msg = (ek_Msg)ek_Msg_init_zero;
    pb_istream_t in = pb_istream_from_buffer(body, n);
    return pb_decode(&in, ek_Msg_fields, &c->msg) ? 1 : -1;
}
static bool handshake(connection *c) {
    int64_t deadline = c->accepted + 2000;
    c->msg = (ek_Msg)ek_Msg_init_zero;
    c->msg.which_body = ek_Msg_challenge_tag;
    ek_Challenge *ch = &c->msg.body.challenge;
    ch->proto_ver = EK_PROTO_VERSION;
    ch->fw_ver = EK_FW_VERSION;
    snprintf(ch->fw_version, sizeof(ch->fw_version), "%s", EK_FW_VERSION_STRING);
    ch->paired = true;
    ch->nonce_d.size = 16;
    esp_fill_random(ch->nonce_d.bytes, 16);
    ch->mac.size = 6;
    memcpy(ch->mac.bytes, mac, 6);
    memcpy(ch->name, name, sizeof(name));
    if (!send_msg(c, false, deadline))
        return false;
#ifdef EK_NETWORK_HOST_TEST
    const char *delay=getenv("ESPKVM_TEST_AUTH_DELAY_MS");
    if(delay){puts("CHALLENGE");fflush(stdout);vTaskDelay((unsigned)atoi(delay));}
#endif
    uint8_t transcript[32], bound[32], proof[32], nonce[16];
    size_t n = 12 + (size_t)c->wire[4] + ((size_t)c->wire[5] << 8);
    if (!ek_hash(c->wire, n, transcript) || receive(c, false, deadline) != 1 ||
        !message_validate(&c->msg, EK_AUTH))
        return false;
    ek_Auth *auth = &c->msg.body.auth;
    memcpy(nonce, auth->nonce_c.bytes, 16);
    memcpy(c->owner, auth->owner.bytes, 16);
    bool takeover = auth->takeover;
    if (!ek_auth_context(transcript, c->owner, takeover, bound) ||
        !ek_proof(psk, bound, nonce, "EK-AUTH-C", proof) || !ek_equal(proof, auth->hmac.bytes, 32))
        return false;
    // Arbitration is short and contains no network or USB waits.
    lock_state();
    bool allowed =
        now_ms() < deadline && (!active || ek_equal(active->owner, c->owner, 16) || takeover);
    if (allowed) {
        if (active) {
            active->retired = true;
            active->replaced = true;
            shutdown(active->fd, SHUT_RD);
        }
        active = c;
        c->authenticated = true;
        c->last_valid = now_ms();
        c->generation = hid_release();
        hid_host_disconnected();
    }
    unlock_state();
    if (!allowed || now_ms() >= deadline)
        return false;
    c->msg = (ek_Msg)ek_Msg_init_zero;
    c->msg.which_body = ek_Msg_auth_ok_tag;
    c->msg.body.auth_ok.hmac.size = 32;
    if (!ek_proof(psk, bound, nonce, "EK-AUTH-D", c->msg.body.auth_ok.hmac.bytes) ||
        !ek_proof(psk, bound, nonce, "EK-KEY", proof))
        return false;
    memcpy(c->tx.key, proof, 16);
    memcpy(c->rx.key, proof, 16);
    c->tx.direction = 1;
    c->rx.direction = 0;
    ek_zero(proof, sizeof(proof));
    ek_zero(nonce, sizeof(nonce));
    return send_msg(c, false, deadline);
}
static bool current(connection *c) {
    lock_state();
    bool value = active == c && !c->retired;
    unlock_state();
    return value;
}
static bool status(connection *c) {
    c->msg = (ek_Msg)ek_Msg_init_zero;
    c->msg.which_body = ek_Msg_status_tag;
    ek_Status *s = &c->msg.body.status;
    s->fw_ver = EK_FW_VERSION;
    s->keep_awake_state = hid_keep_awake_status(&s->keep_awake_moves);
    snprintf(s->fw_version, sizeof(s->fw_version), "%s", EK_FW_VERSION_STRING);
    s->uptime_s = now_ms() / 1000;
    s->usb_mounted = hid_mounted();
    s->kb_leds = hid_leds();
    wifi_ap_record_t ap;
    if (esp_wifi_sta_get_ap_info(&ap) == ESP_OK)
        s->rssi = ap.rssi;
    return send_msg(c, true, now_ms() + 100);
}
static bool dispatch(connection *c) {
    if (!message_validate(&c->msg, EK_TO_DEVICE) || !current(c))
        return false;
    lock_state();
    c->last_valid = now_ms();
    unlock_state();
    switch (c->msg.which_body) {
    case ek_Msg_kb_tag:
    case ek_Msg_mouse_tag:
    case ek_Msg_consumer_tag:
        // Arbitration and submission are serialized so an invalidated owner cannot
        // enqueue with the new generation after the takeover barrier.
        lock_state();
        bool ok = active == c && !c->retired && hid_submit(&c->msg, c->generation);
        unlock_state();
        return ok;
    case ek_Msg_release_all_tag:
        if(c->release_count==128 || c->msg.body.release_all.id<=c->release_last)return false;
        c->release_last=c->msg.body.release_all.id;
        c->release_ids[c->release_count++]=c->release_last;
        lock_state();
        if (active == c && !c->retired)
            c->generation = hid_release();
        unlock_state();
        hid_wake_host();
        return true;
    case ek_Msg_heartbeat_tag:
        if (c->msg.body.heartbeat.reply) {
            if(c->msg.body.heartbeat.id==c->probe && now_ms()-c->probe_sent<=1000) {
                c->probe=0;
                hid_host_activity(c->msg.body.heartbeat.host_idle_s,
                                  c->msg.body.heartbeat.host_activity_known);
            }
            return true;
        }
        hid_host_activity(c->msg.body.heartbeat.host_idle_s,
                          c->msg.body.heartbeat.host_activity_known);
        c->msg.body.heartbeat.reply = true;
        return send_msg(c, true, now_ms() + 100);
    case ek_Msg_cfg_get_tag:
        c->msg = (ek_Msg)ek_Msg_init_zero;
        c->msg.which_body = ek_Msg_cfg_data_tag;
        config_get(&c->msg.body.cfg_data, true);
        return send_msg(c, true, now_ms() + 100);
    case ek_Msg_cfg_set_tag: {
        ek_ConfigResult result = config_save(&c->msg.body.cfg_set);
        ek_zero(&c->msg, sizeof(c->msg));
        c->msg.which_body = ek_Msg_cfg_result_tag;
        c->msg.body.cfg_result = result;
        if (!result.code)
            hid_release();
        bool sent = send_msg(c, true, now_ms() + 200);
        if (!result.code)
            esp_restart();
        return sent;
    }
    case ek_Msg_reboot_tag:
        hid_release();
        esp_restart();
        return false;
    default:
        return false;
    }
}
static void connection_task(void *arg) {
    connection *c = arg;
    if (handshake(c)) {
        c->tick = now_ms();
        while (current(c)) {
            if(c->release_count && hid_ready(c->generation)) {
                bool sent=true;
                for(unsigned i=0;i<c->release_count;i++) {
                    c->msg=(ek_Msg)ek_Msg_init_zero;c->msg.which_body=ek_Msg_released_tag;
                    c->msg.body.released.id=c->release_ids[i];
                    if(!current(c) || !send_msg(c,true,now_ms()+100)) {sent=false;break;}
                }
                c->release_count=0;if(!sent)break;
            }
            if (now_ms() >= c->tick) {
                if (!status(c))
                    break;
                c->msg = (ek_Msg)ek_Msg_init_zero;
                c->msg.which_body = ek_Msg_heartbeat_tag;
                if (++c->heartbeat == 0)
                    c->heartbeat++;
                c->msg.body.heartbeat.id = c->heartbeat;
                c->probe=c->heartbeat;c->probe_sent=now_ms();
                if (!send_msg(c, true, now_ms() + 100))
                    break;
                c->tick = now_ms() + 1000;
            }
            // A frame's 1s deadline must not be shortened by the heartbeat schedule.
            int64_t wake=minimum(c->last_valid+3000,c->tick);
            if(c->release_count)wake=minimum(wake,now_ms()+5);
            int n = receive(c, true, wake);
            if (n < 0 || (n == 1 && !dispatch(c)))
                break;
        }
    }
    lock_state();
    bool replaced = c->replaced;
    unlock_state();
    if (replaced && c->authenticated) {
        c->msg = (ek_Msg)ek_Msg_init_zero;
        c->msg.which_body = ek_Msg_session_end_tag;
        c->msg.body.session_end.reason = 1;
        (void)send_msg(c, true, now_ms() + 100);
    }
    // The watchdog holds state_lock when selecting a socket to shutdown; leave
    // the slot reserved until close finishes to prevent descriptor reuse races.
    lock_state();
    if (active == c) {
        active = NULL;
        hid_host_disconnected();
        hid_release();
    }
    c->retired = true;
    int fd = c->fd;
    c->fd = -1;
    unlock_state();
    shutdown(fd, SHUT_RDWR);
    close(fd);
    lock_state();
    ek_zero(c, sizeof(*c));
    c->fd = -1;
    unlock_state();
    vTaskDelete(NULL);
}
static void watchdog(void *arg) {
    (void)arg;
    for (;;) {
        int fd = -1;
        int64_t now = now_ms();
        lock_state();
        if (active && (now - active->last_valid >= 3000 || !atomic_load(&online) ||
                       !hid_healthy(active->generation))) {
            active->retired = true;
            fd = active->fd;
            active = NULL;
            hid_host_disconnected();
            hid_release();
            if (fd >= 0)
                shutdown(fd, SHUT_RDWR);
        }
        // Shutdown while the mutex protects FD ownership; cleanup cannot close
        // and recycle the descriptor until this bounded operation finishes.
        unlock_state();
        vTaskDelay(pdMS_TO_TICKS(25));
    }
}
typedef struct {
    uint32_t ip;
    unsigned attempts;
    int64_t start;
} rate;
static rate sources[8];
static bool admit(uint32_t ip, int64_t now) {
    static int64_t window;
    static unsigned total;
    if (now - window >= 10000) {
        window = now;
        total = 0;
    }
    if (++total > 24)
        return false;
    unsigned index = 0;
    for (unsigned i = 0; i < 8; i++) {
        if (sources[i].ip == ip) {
            index = i;
            break;
        }
        if (sources[i].start < sources[index].start)
            index = i;
    }
    rate *r = &sources[index];
    if (r->ip != ip || now - r->start >= 10000)
        *r = (rate){.ip = ip, .start = now};
    return ++r->attempts <= 6;
}
static void accept_task(void *arg) {
    (void)arg;
    listener = socket(AF_INET, SOCK_STREAM, IPPROTO_IP);
    if (listener < 0) {
        vTaskDelete(NULL);
        return;
    }
    struct sockaddr_in address = {
        .sin_family = AF_INET, .sin_port = htons(port), .sin_addr.s_addr = htonl(INADDR_ANY)};
    if (bind(listener, (struct sockaddr *)&address, sizeof(address)) || listen(listener, 4)) {
        close(listener);
        listener = -1;
        vTaskDelete(NULL);
        return;
    }
#ifdef EK_NETWORK_HOST_TEST
    socklen_t bound_size = sizeof(address);
    getsockname(listener, (struct sockaddr *)&address, &bound_size);
    printf("READY %u\n", ntohs(address.sin_port));
    fflush(stdout);
#endif
    for (;;) {
        struct sockaddr_in peer;
        socklen_t size = sizeof(peer);
        int fd = accept(listener, (struct sockaddr *)&peer, &size);
        if (fd < 0) {
            vTaskDelay(pdMS_TO_TICKS(20));
            continue;
        }
        int64_t accepted = now_ms();
        connection *slot = NULL;
        if (atomic_load(&online) && admit(peer.sin_addr.s_addr, accepted)) {
            lock_state();
            unsigned pending = 0, same = 0;
            for (unsigned i = 0; i < 4; i++)
                if (slots[i].used && !slots[i].authenticated) {
                    pending++;
                    if (slots[i].peer == peer.sin_addr.s_addr)
                        same++;
                }
            if (pending < 3 && same < 2)
                for (unsigned i = 0; i < 4; i++)
                    if (!slots[i].used) {
                        slot = &slots[i];
                        break;
                    }
            if (slot) {
                *slot = (connection){
                    .used = true, .fd = fd, .peer = peer.sin_addr.s_addr, .accepted = accepted};
            }
            unlock_state();
        }
        if (!slot) {
            close(fd);
            continue;
        }
        int yes = 1;
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &yes, sizeof(yes));
        setsockopt(fd, SOL_SOCKET, SO_KEEPALIVE, &yes, sizeof(yes));
        int flags=fcntl(fd,F_GETFL,0);
        if(flags<0 || fcntl(fd,F_SETFL,flags|O_NONBLOCK)<0){close(fd);lock_state();slot->used=false;unlock_state();continue;}
        if (xTaskCreate(connection_task, "session", 8192, slot, 6, NULL) != pdPASS) {
            close(fd);
            lock_state();
            slot->used = false;
            unlock_state();
        }
    }
}
#ifndef EK_NETWORK_HOST_TEST
static void wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data) {
    (void)arg;
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_START) {
        atomic_store(&online, false);
        ESP_LOGI(TAG, "Wi-Fi station started; connecting");
        esp_wifi_connect();
    } else if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        const wifi_event_sta_disconnected_t *event = data;
        atomic_store(&online, false);
        ESP_LOGW(TAG, "Wi-Fi disconnected: reason=%u; reconnecting",
                 (unsigned)(event ? event->reason : 0));
        esp_wifi_connect();
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        const ip_event_got_ip_t *event = data;
        atomic_store(&online, true);
        if (event)
            ESP_LOGI(TAG, "Wi-Fi connected; IP=" IPSTR, IP2STR(&event->ip_info.ip));
        else
            ESP_LOGI(TAG, "Wi-Fi connected and received IP");
    }
}
#endif
esp_err_t network_service_start(const ek_Config *config) {
    state_lock = xSemaphoreCreateMutexStatic(&mutex_storage);
    if (!config->has_psk || config->psk.size != 16)
        return ESP_ERR_INVALID_ARG;
    memcpy(psk, config->psk.bytes, 16);
    memcpy(name, config->name, 32);
    port = config->tcp_port;
#ifndef EK_NETWORK_HOST_TEST
    ESP_ERROR_CHECK(esp_read_mac(mac, ESP_MAC_WIFI_STA));
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_t *netif = esp_netif_create_default_wifi_sta();
    if (!netif)
        return ESP_ERR_NO_MEM;
    ESP_ERROR_CHECK(esp_netif_dhcpc_stop(netif));
    esp_netif_ip_info_t ip = {.ip.addr = htonl(config->ip),
                              .gw.addr = htonl(config->gw),
                              .netmask.addr = htonl(config->mask)};
    ESP_LOGI(TAG, "starting network service: static IP=" IPSTR ", TCP port=%u",
             IP2STR(&ip.ip), (unsigned)port);
    ESP_ERROR_CHECK(esp_netif_set_ip_info(netif, &ip));
    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, wifi_event, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP, wifi_event, NULL));
    wifi_config_t wifi = {0};
    memcpy(wifi.sta.ssid, config->ssid, strlen(config->ssid));
    memcpy(wifi.sta.password, config->password, strlen(config->password));
    ESP_ERROR_CHECK(esp_wifi_set_storage(WIFI_STORAGE_RAM));
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi));
    ek_zero(&wifi, sizeof(wifi));
    ESP_ERROR_CHECK(esp_wifi_start());
    ESP_ERROR_CHECK(esp_wifi_set_ps(WIFI_PS_NONE));
#else
    mac[0] = 2;
    atomic_store(&online, true);
#endif
    if (xTaskCreate(watchdog, "watchdog", 3072, NULL, configMAX_PRIORITIES - 2, NULL) != pdPASS)
        return ESP_ERR_NO_MEM;
    return xTaskCreate(accept_task, "accept", 4096, NULL, 5, NULL) == pdPASS ? ESP_OK
                                                                             : ESP_ERR_NO_MEM;
}
