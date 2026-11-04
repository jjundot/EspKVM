#include "keep_awake.h"
#include <assert.h>
#include <stdio.h>

int main(void) {
    keep_awake s;
    keep_awake_config config = {true, 30, 60, 1800, 120};
    keep_awake_init(&s, config, 0, 0);
    assert(s.next_ms == 30000);
    keep_awake_user(&s, 10000, 30);
    assert(s.next_ms == 70000); // A and B are both included; user input resets A/B.
    assert(!keep_awake_poll(&s, 69999));
    keep_awake_host(&s, 70000, 0, true, HOST_LOCK_UNKNOWN);
    assert(keep_awake_poll(&s, 70000));
    assert(keep_awake_poll(&s, 70001)); // Still due until a movement is actually made.
    keep_awake_moved(&s, 70001, 0);
    assert(s.moves == 1 && s.next_ms == 100001);
    assert(s.last_user_ms == 10000); // Successful synthetic movement never refreshes C.
    assert(!keep_awake_poll(&s, 104000)); // HOST heartbeat stale: recheck in 1 s, not A..B.
    assert(s.next_ms == 105000);
    keep_awake_host(&s, 104500, 0, true, HOST_LOCK_UNKNOWN);
    assert(!keep_awake_poll(&s, 104999));
    assert(keep_awake_poll(&s, 105000)); // Recovered condition moves within a second.
    keep_awake_moved(&s, 105000, 0);
    assert(keep_awake_state(&s, 103000) == KA_NO_HOST);
    keep_awake_host(&s, 110000, 119, true, HOST_LOCK_UNKNOWN);
    assert(keep_awake_state(&s, 110999) == KA_ACTIVE);
    assert(keep_awake_state(&s, 111000) == KA_HOST_IDLE);
    keep_awake_host(&s, 120000, 0, false, HOST_LOCK_UNKNOWN);
    assert(keep_awake_state(&s, 120000) == KA_HOST_IDLE);
    keep_awake_host(&s, 1810000, 0, true, HOST_LOCK_UNKNOWN);
    assert(keep_awake_state(&s, 1810000) == KA_EXPIRED); // C exact boundary.
    keep_awake_host(&s, 1811000, 0, true, HOST_LOCK_UNKNOWN);
    assert(keep_awake_state(&s, 1811000) == KA_EXPIRED); // Heartbeats/reconnect cannot renew C.
    keep_awake_user(&s, 1811000, 0);
    assert(keep_awake_state(&s, 1811000) == KA_ACTIVE);

    config.stop_s = 0;
    keep_awake_init(&s, config, 0, 0);
    keep_awake_host(&s, 1000000000, 0, true, HOST_LOCK_UNKNOWN);
    assert(keep_awake_poll(&s, 1000000000)); // C=0 with a normal D requirement.
    config.stop_s = 60;
    config.host_s = 0;
    keep_awake_init(&s, config, 0, 0);
    assert(keep_awake_state(&s, 59999) == KA_WAITING);
    assert(keep_awake_state(&s, 60000) == KA_EXPIRED); // D=0 does not remove C.
    config.stop_s = config.host_s = 0;
    keep_awake_init(&s, config, 0, 0);
    assert(keep_awake_state(&s, 1000000000) == KA_WAITING); // D=0 removes the HOST activity prerequisite.
    keep_awake_host(&s, 1000000000, UINT32_MAX, false, HOST_LOCK_UNKNOWN);
    assert(keep_awake_poll(&s, 1000000000)); // C=0 never expires, D=0 accepts unknown/idle HOST.
    assert(keep_awake_state(&s, 1000003000) == KA_WAITING);
    s.heartbeat_seen = false;
    assert(keep_awake_state(&s, 1000000000) == KA_WAITING);
    s.config.enabled = false;
    assert(!keep_awake_poll(&s, 2000000000));
    assert(keep_awake_state(&s, 2000000000) == KA_OFF);

    config = (keep_awake_config){true, 30, 60, 1, 1, KA_MODE_HOST_UNLOCKED};
    keep_awake_init(&s, config, 0, 0);
    assert(keep_awake_state(&s, 0) == KA_NO_HOST); // Follows HOST: never runs without heartbeats.
    keep_awake_host(&s, 1000000, UINT32_MAX, false, HOST_LOCK_UNKNOWN);
    assert(keep_awake_state(&s, 1000000) == KA_LOCK_UNKNOWN); // Older HOST or failed detection.
    keep_awake_host(&s, 1000000, UINT32_MAX, false, 3);
    assert(keep_awake_state(&s, 1000000) == KA_LOCK_UNKNOWN); // Future values fail closed.
    keep_awake_host(&s, 1000000, UINT32_MAX, false, HOST_LOCKED);
    assert(keep_awake_state(&s, 1000000) == KA_HOST_LOCKED);
    assert(!keep_awake_poll(&s, 1000000));
    assert(s.next_ms == 1001000); // Paused states recheck every second.
    keep_awake_host(&s, 1000500, UINT32_MAX, false, HOST_UNLOCKED);
    assert(keep_awake_state(&s, 1000500) == KA_WAITING); // C and D expired but are not applied.
    assert(keep_awake_poll(&s, 1001000)); // Moves within a second of unlocking.
    keep_awake_moved(&s, 1001000, 0);
    assert(keep_awake_state(&s, 1003499) == KA_ACTIVE);
    assert(keep_awake_state(&s, 1003500) == KA_NO_HOST); // Stale heartbeat outranks last lock state.
    keep_awake_host(&s, 1004000, 0, true, HOST_UNLOCKED);
    s.heartbeat_seen = false; // hid_host_disconnected
    assert(keep_awake_state(&s, 1004000) == KA_NO_HOST);
    s.config.enabled = false;
    keep_awake_host(&s, 1005000, 0, true, HOST_UNLOCKED);
    assert(keep_awake_state(&s, 1005000) == KA_OFF);
    config.mode = KA_MODE_ACTIVITY;
    config.stop_s = config.host_s = 0;
    keep_awake_init(&s, config, 0, 0);
    keep_awake_host(&s, 1000, 0, true, HOST_LOCKED);
    assert(keep_awake_state(&s, 1000) == KA_WAITING); // Activity mode ignores the lock state.
    puts("Keep-awake random intervals, real-input reset, 1 s recheck, C/D, HOST lock mode and heartbeat expiry passed");
    return 0;
}
