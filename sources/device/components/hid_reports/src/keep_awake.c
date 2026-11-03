#include "keep_awake.h"

static void schedule(keep_awake *s, uint64_t now, uint32_t random) {
    uint32_t span = s->config.max_s - s->config.min_s + 1;
    s->next_ms = now + ((uint64_t)s->config.min_s + random % span) * 1000;
}
void keep_awake_init(keep_awake *s, keep_awake_config config, uint64_t now, uint32_t random) {
    *s = (keep_awake){.config = config, .last_user_ms = now};
    schedule(s, now, random);
}
void keep_awake_user(keep_awake *s, uint64_t now, uint32_t random) {
    s->last_user_ms = now;
    schedule(s, now, random);
}
void keep_awake_host(keep_awake *s, uint64_t now, uint32_t idle_s, bool known) {
    s->heartbeat_seen = true;
    s->heartbeat_ms = now;
    s->host_idle_ms = (uint64_t)idle_s * 1000;
    s->host_known = known;
}
unsigned keep_awake_state(const keep_awake *s, uint64_t now) {
    if (!s->config.enabled) return KA_OFF;
    if (s->config.stop_s && now - s->last_user_ms >= (uint64_t)s->config.stop_s * 1000)
        return KA_EXPIRED;
    if (s->config.host_s && (!s->heartbeat_seen || now - s->heartbeat_ms >= 3000)) return KA_NO_HOST;
    if (s->config.host_s && (!s->host_known ||
        s->host_idle_ms + now - s->heartbeat_ms >= (uint64_t)s->config.host_s * 1000))
        return KA_HOST_IDLE;
    return s->moves ? KA_ACTIVE : KA_WAITING;
}
// A due check stays due until a movement is made: a paused state is rechecked
// every second and a busy USB engine on the next poll, instead of waiting for
// another full A..B interval.
bool keep_awake_poll(keep_awake *s, uint64_t now) {
    if (now < s->next_ms) return false;
    unsigned state = keep_awake_state(s, now);
    if (state == KA_WAITING || state == KA_ACTIVE) return true;
    s->next_ms = now + 1000;
    return false;
}
void keep_awake_moved(keep_awake *s, uint64_t now, uint32_t random) {
    s->moves++;
    schedule(s, now, random);
}
