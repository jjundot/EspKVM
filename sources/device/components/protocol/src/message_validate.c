#include "message_validate.h"
static bool axis(int32_t n) { return n >= -1000000 && n <= 1000000; }
bool message_validate(const ek_Msg *m, ek_phase phase) {
    if (!m)
        return false;
    if (phase == EK_CHALLENGE)
        return m->which_body == ek_Msg_challenge_tag && m->body.challenge.proto_ver == EK_PROTO_VERSION &&
               m->body.challenge.nonce_d.size == 16 && m->body.challenge.mac.size == 6;
    if (phase == EK_AUTH)
        return m->which_body == ek_Msg_auth_tag && m->body.auth.nonce_c.size == 16 &&
               m->body.auth.hmac.size == 32 && m->body.auth.owner.size == 16;
    if (phase == EK_AUTH_REPLY)
        return (m->which_body == ek_Msg_auth_ok_tag && m->body.auth_ok.hmac.size == 32) ||
               m->which_body == ek_Msg_auth_fail_tag;
    if (m->which_body == ek_Msg_heartbeat_tag)
        return m->body.heartbeat.id != 0;
    if (phase == EK_TO_CLIENT) {
        switch (m->which_body) {
        case ek_Msg_released_tag:return m->body.released.id!=0;
        case ek_Msg_status_tag:
            return m->body.status.kb_leds <= 7;
        case ek_Msg_cfg_data_tag:
            return !m->body.cfg_data.has_password && !m->body.cfg_data.has_psk;
        case ek_Msg_cfg_result_tag:
            return m->body.cfg_result.code <= 3 && m->body.cfg_result.field <= 11;
        case ek_Msg_session_end_tag:
            return m->body.session_end.reason == 1;
        default:
            return false;
        }
    }
    if (phase != EK_TO_DEVICE)
        return false;
    switch (m->which_body) {
    case ek_Msg_kb_tag:
        return m->body.kb.mods <= 255 && !(m->body.kb.keys_lo & 15) &&
               !(m->body.kb.keys_192_255 & ((uint64_t)255 << 32));
    case ek_Msg_mouse_tag:
        return m->body.mouse.buttons <= 31 && axis(m->body.mouse.dx) && axis(m->body.mouse.dy) &&
               axis(m->body.mouse.wheel) && axis(m->body.mouse.hwheel);
    case ek_Msg_consumer_tag:
        switch (m->body.consumer.usage) {
        case 0:
        case 0xe2:
        case 0xe9:
        case 0xea:
        case 0xcd:
        case 0xb5:
        case 0xb6:
        case 0xb7:
            return true;
        default:
            return false;
        }
    case ek_Msg_release_all_tag:
        return m->body.release_all.id!=0;
    case ek_Msg_cfg_get_tag:
    case ek_Msg_reboot_tag:
        return true;
    // Config fields are validated/merged under the shared transaction lock.
    case ek_Msg_cfg_set_tag:
        return true;
    default:
        return false;
    }
}
