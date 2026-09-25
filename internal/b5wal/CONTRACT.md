# B5 S7a emergency WAL contract (feature off)

This package is an isolated S7a contract implementation. It is not imported by
any production entry point. `CanonicalEvent` means already-canonical
`agentsql.audit.event.v4` bytes; the existing 27-field audit-chain V1 encoder is
not used. Event-v4 and the formal PostgreSQL receipt schema remain S1b work.

## Record wire layout

All integers are unsigned big-endian. Every record occupies one 20,480-byte
(5 x 4KiB) extent. AES-256-GCM's 16-byte tag is stored separately from the
ciphertext. The complete header is GCM AAD.

| Offset | Width | Header field |
|---:|---:|---|
| 0 | 8 | magic `ASQLB5W4` |
| 8 | 2 | WAL format version = 1 |
| 10 | 2 | flags = 0 |
| 12 | 2 | header length = 512 |
| 14 | 2 | extension encoded length, 0..212 |
| 16 | 4 | unpadded total/frame length |
| 20 | 4 | charged bytes = 20,480 |
| 24 | 4 | ciphertext length, 0..16,384 |
| 28 | 2 | event schema version |
| 30 | 2 | event schema ID length, 1..64 |
| 32 | 2 | key ID length, 1..96 |
| 34 | 2 | independent GCM tag length = 16 |
| 36 | 2 | trailer length = 32 |
| 38 | 2 | reserved = 0 |
| 40 | 16 | random segment ID |
| 56 | 8 | monotonically increasing record ordinal |
| 64 | 16 | reservation ID |
| 80 | 16 | event UUID |
| 96 | 32 | SHA-256 canonical event/payload digest |
| 128 | 12 | nonce: random segment domain32 + ordinal uint64 |
| 140 | 64 | visible-ASCII event schema ID + zero fill |
| 204 | 96 | visible-ASCII key ID + zero fill |
| 300 | 212 | canonical extensions + zero fill |

Extensions are strictly increasing unique nonzero uint16 tag, uint16 value
length, then value. Their complete encoding is at most 212 bytes. Unknown tags
are authenticated and preserved; duplicate, unsorted, truncated, oversized, or
nonzero padding is rejected.

The body is `ciphertext || 16-byte tag || 32-byte trailer`. The trailer is:

| Offset | Width | Trailer field |
|---:|---:|---|
| 0 | 4 | CRC32C(header + ciphertext + tag) |
| 4 | 4 | duplicate frame length |
| 8 | 8 | duplicate ordinal |
| 16 | 2 | trailer version = 1 |
| 18 | 2 | flags = 0 |
| 20 | 4 | reserved = 0 |
| 24 | 8 | fixed commit marker |

The bytes after the trailer are zero padding up to offset 19,968. The final 512
bytes are real accounting/index metadata in the *same extent*, with its own
CRC32C and record digest. They are not an independent physical allocation.

Maximum byte account:

```text
512 header + 16,384 ciphertext + 16 tag + 32 trailer = 16,944 frame bytes
align_up(16,944, 4,096) = 20,480 physical bytes
accounting starts at 19,968, leaving 3,024 zero bytes after the maximum frame
6 records x 20,480 = 122,880 bytes (120 KiB)
```

## Emission and rotation

`EmissionPaths()` is the executable state x transition table. The two maximum
paths are statement-effect/commit-intent timeout, each with six unique records:
current fact, rollback, terminal outcome, session terminal, one coalesced
durability diagnostic, and one coalesced recovery event. Result receipts are a
separate plane and do not consume a WAL slot.

After a timeout, the writer and segment stay `WEDGED_UNCONFIRMED` even if fsync
later succeeds. One new segment/key may be opened for the same reservation so
later distinct facts can be attempted; rotation emits no record and the timed
out record is never retried. If the replacement wedges, no second rotation,
diagnostic, retry, or recovery emission is allowed for that reservation.
Remaining rollback/terminal/session/recovery records may therefore be
permanently absent and the receipt remains `DURABILITY_LOST`. This fail-closed
rule is what makes six a total bound instead of an optimistic path count.

Every process start, ownership change, and rotation creates a random 128-bit
segment ID, random 256-bit segment key, random nonce domain, registry CAS key
ID, and a create-exclusive manifest. Manifest file fsync and parent-directory
fsync complete before record use. Old segments have no reopen-for-append API.
If the host cannot fsync directory handles (the current Windows host returns
access denied), creation fails closed after the file sync and the segment is
never usable; target-filesystem qualification remains an activation gate.

## Result receipts

The write-once key is `(session_id, request_id, event_uuid,
attempt_generation)`. Immutable data binds the business event SHA-256 digest,
the exact WAL append receipt digest, and `reported_durability_at_response`.
`PersistThenSend` persists `PREPARED`, durably advances `SEND_STARTED`, and only
then calls the response sender. A persistence failure means no terminal response
may be sent. Delivery cannot be proven across a network crash window, but retry
always reproduces the same result.

The only append path is `UNKNOWN -> TIMEOUT -> LATE_CONFIRMED -> RECOVERED`
(`TIMEOUT -> RECOVERED` is allowed at restart). Reconciliation is
`NONE -> REPLAY_STAGED -> PRIMARY_DURABLE`; delivery is monotonic. The reported
durability never changes. A valid record with no result receipt yields
`HistoricalResponseUnknown`; scanner/replay must never infer historical
`AUDIT_PENDING`.
