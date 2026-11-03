#pragma once
#include <stdbool.h>
#include <stdint.h>

enum { KA_OFF, KA_NO_HOST, KA_HOST_IDLE, KA_EXPIRED, KA_WAITING, KA_ACTIVE };
typedef struct {
    bool enabled;
    uint32_t min_s, max_s, stop_s, host_s;
} keep_awake_config;
typedef struct {
    keep_awake_config config;
    uint64_t last_user_ms, next_ms, heartbeat_ms, host_idle_ms;
    bool heartbeat_seen, host_known;
    uint32_t moves;
} keep_awake;
void keep_awake_init(keep_awake *s, keep_awake_config config, uint64_t now, uint32_t random);
void keep_awake_user(keep_awake *s, uint64_t now, uint32_t random);
void keep_awake_host(keep_awake *s, uint64_t now, uint32_t idle_s, bool known);
unsigned keep_awake_state(const keep_awake *s, uint64_t now);
bool keep_awake_poll(keep_awake *s, uint64_t now, uint32_t random);
