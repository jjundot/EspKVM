# EK v2 wire format (candidate, not frozen)

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

Challenge and SerialPacket require version 2; no downgrade. Nonce, MAC, HMAC
and owner lengths are checked after decoding. Nanopb uses length-bearing arrays
so absent fields cannot silently become valid all-zero arrays.

```text
transcript = SHA256(original Challenge header || original Challenge body)
context = SHA256("EK-AUTH-V2" || transcript[32] || owner[16] || takeover[1])
proof_c = HMAC-SHA256(psk, "EK-AUTH-C" || context || nonce_c[16])
proof_d = HMAC-SHA256(psk, "EK-AUTH-D" || context || nonce_c[16])
key = HMAC-SHA256(psk, "EK-KEY" || context || nonce_c[16])[0:16]
```

Labels are ASCII without NUL; takeover is one byte, 0 or 1. Owner is random per
client process and retained for reconnects. Different-owner replacement requires
explicit authenticated intent. A replaced client receives encrypted SessionEnd
when possible and stops automatic reconnect until an explicit takeover request.

GCM AAD is the original 12-byte header. Nonce is
`[direction,0,0,0,seq_LE32,0,0,0,0]`. Each direction starts at seq=0;
strictly expected sequence only; close before 0xffffff00.

Config optional password/psk: absent preserves the stored credential; explicit
empty password selects an open network; present PSK must be 16 bytes. First
provisioning requires both. Redacted reads omit both. Nonsecret fields replace
the complete snapshot, including six_kro. V1 NVS blobs migrate separately;
wire compatibility with v1 is deliberately rejected.

Heartbeat has nonzero id and reply flag. Reply once to requests, never to replies.
Only a matching, unexpired probe produces an RTT sample. RTT is not input latency
or USB completion latency.

ReleaseAll.id is nonzero and strictly increases per connection. Released.id is
sent only after keyboard, mouse and Consumer zero reports complete on USB.
Requests may share a stronger later barrier, but each id is acknowledged. Both
sides bound waiters to 128. Host calls time out after 500 ms and close the session;
close completes every waiter. Unknown/duplicate acknowledgements are errors.
Unmounted USB keeps pending-release and cannot acknowledge success.

`testdata/v2.txt` contains protobuf, proofs, key and full GCM frames. Regenerate it
with `node tools/generate_vectors.cjs`, an independent Node crypto/manual-wire
oracle; Go protobuf and nanopb plus firmware PSA crypto must both match.
