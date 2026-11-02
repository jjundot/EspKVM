#pragma once
#include "ek.pb.h"
#include "ek_versions.h"
typedef enum { EK_CHALLENGE, EK_AUTH, EK_AUTH_REPLY, EK_TO_DEVICE, EK_TO_CLIENT } ek_phase;
bool message_validate(const ek_Msg *msg, ek_phase phase);
