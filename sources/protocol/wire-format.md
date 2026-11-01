# EK v1 wire format

Normative behavior: [design decisions](../../docs/esp32-kvm-decisions.md), sections 4–6.
Schema: [ek.proto](ek.proto). Bounds: [ek.options](ek.options).

All transports use `45 4b 56 4d | uint16_le body_length | flags | 00 | uint32_le seq`.
Body length is 1–512. TCP handshake flags=0, seq=0, no trailer. Authenticated
TCP flags=1, 16-byte GCM tag. Serial flags=2, nonzero request id, 4-byte IEEE
CRC32 little-endian over header and body. Transport phase fixes the flags.
TCP rejects malformed frames without resynchronization. Serial retains nested
candidates after CRC failure or a fixed one-second candidate deadline.

Consumer usages currently supported: 0 (release), B5, B6, B7, CD, E2, E9, EA.
`testdata/frames.txt` contains named full serial wire frames, shared by C and Go.
