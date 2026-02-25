# collab-editor

A browser-based collaborative code editor where multiple users edit the same file simultaneously without conflicts.

**Live demo:** [your-app.railway.app](https://collaborative-code-editor-production-acb7.up.railway.app/)

Built from scratch to understand the hard parts: the OT algorithm, the client state machine, and the WebSocket session model. No CRDT library — the transform logic is handwritten and tested.

---

## How it works

### Operational Transforms

Every keystroke becomes an *operation* — a sequence of `retain(n)`, `insert(text)`, and `delete(n)` components that covers the full document. The invariant: `apply(doc, op)` always produces a valid document of the correct length.

The problem: two users make concurrent edits against the same revision. User A inserts `"X"` at position 3; User B deletes position 5. Applied naively, these conflict. OT resolves this by transforming each op *through* the other before applying it, so both clients converge to the same document regardless of network ordering.

```
doc = "hello world"

A: insert "!" at pos 11  →  retain(11) · insert("!")
B: delete " world" at pos 5  →  retain(5) · delete(6)

transform(A, B):
  A' = retain(5) · insert("!")   (position adjusted — " world" is gone)
  B' = retain(5) · delete(6)     (unchanged — A's insert was after B's range)

apply(apply(doc, A), B') == apply(apply(doc, B), A') == "hello!"
```

The Go implementation is in [`server/ot/ot.go`](server/ot/ot.go). The TypeScript mirror (same algorithm) runs in the browser at [`frontend/src/ot-client.ts`](frontend/src/ot-client.ts).

### Client state machine

The client is always in one of three states:

```
Synchronized ──(local edit)──▶ AwaitingAck ──(local edit)──▶ AwaitingAckWithBuffer
      ▲                              │                                │
      └──────────(ack)───────────────┘◀────────────(ack)─────────────┘
```

- **Synchronized** — no in-flight op. Server ops apply directly.
- **AwaitingAck** — one op sent, waiting for ack. Remote ops are transformed against `inFlight` before applying. Further local edits accumulate in `buffer`.
- **AwaitingAckWithBuffer** — same, but `buffer` is non-empty. Remote ops are transformed against *both* `inFlight` and `buffer`. On ack, `buffer` becomes the next `inFlight`.

This ensures the client never has more than one op in-flight at a time, which is what makes the server-side transform tractable — the server only needs to transform incoming ops against its committed log since `baseRev`.

### Server

The Go WebSocket server (`gorilla/websocket`) manages sessions in memory. Each session holds:

- The current document string
- An op log indexed by revision number
- The set of connected clients

On receiving an op at `baseRev=N`, the server transforms it against `opLog[N], opLog[N+1], …, opLog[current]`, applies it, appends to the log, increments `rev`, acks the sender, and broadcasts the *transformed* op to all other clients.

Redis is used for persistence between restarts (TTL 24h). Falls back to in-memory if `REDIS_URL` is not set — fine for demo use.

### Cursor presence

Cursor position changes are sent as lightweight `presence` messages (throttled to 50ms) and broadcast to all peers. Each remote cursor gets a Monaco decoration with a coloured caret. No OT needed here — presence is best-effort.

---

## Architecture

```
Browser A ──ws──┐                          ┌── Browser B
                ▼                          ▼
         ┌─────────────────────────────────────────┐
         │           Go WebSocket server           │
         │                                         │
         │  Session hub (goroutine per session)     │
         │       │                                 │
         │  OT engine ◀── transform(op, opLog[N:]) │
         │       │                                 │
         │  Op log ([]ot.Op, indexed by rev)        │
         │       │                                 │
         │  Presence broadcaster                   │
         │       │                                 │
         │  Redis (doc state + TTL)                 │
         └─────────────────────────────────────────┘
```

---

## Running locally

```bash
# 1. Build frontend (outputs to server/static/)
cd frontend && npm ci && npm run build

# 2. Run Go server
cd server && go run .

# Or with mprocs for both at once:
mprocs   # uses mprocs.yaml
```

Open `http://localhost:8080`. Copy the URL to a second tab — both tabs edit the same session.

**With Redis** (optional, for persistence):
```bash
REDIS_URL=redis://localhost:6379 go run .
```

---

## Tests

```bash
cd server && go test ./ot/... -v
```

10 tests covering:
- Insert/insert at same position (the classic OT conflict)
- Insert/insert at different positions
- Overlapping concurrent deletes
- Insert vs. delete (both orderings)
- `apply` correctness for insert, delete, replace
- `compose` associativity
- Multi-op convergence stress test

---

## Deploying to Railway

```bash
# Install Railway CLI
npm i -g @railway/cli

# Login and init
railway login
railway init

# Add Redis plugin in Railway dashboard, then:
railway up
```

Railway auto-detects the Dockerfile. Set `REDIS_URL` from the Railway Redis plugin — it's injected automatically if you add Redis to the same project.

---

## What I'd build next

- **Undo/redo** — requires storing the inverse of each op and transforming the undo stack as remote ops arrive. Tricky but well-defined.
- **Multi-file sessions** — route ops to per-file sub-sessions within a shared session namespace.
- **Persistent op log in Redis** — currently ops are GC'd from memory; storing them enables audit history and finer-grained reconnect (instead of full doc resync).
- **WASM-compiled OT core** — compile the Go OT engine to WASM, run it in the browser. Single source of truth for the algorithm, eliminates the TypeScript mirror.
- **Fuzzing the transform function** — `go test -fuzz` against arbitrary pairs of concurrent ops to find edge cases the unit tests miss.

---

## References

- [Jupiter OT system](https://dl.acm.org/doi/10.1145/215585.215706) (Nichols et al., 1995) — the client state machine design
- [Understanding and Applying Operational Transformation](https://web.archive.org/web/20070206130927/http://www.waveprotocol.org/whitepapers/operational-transform) — Wave Protocol whitepaper, clearest OT writeup I've found
- [ShareJS](https://github.com/josephg/ShareJS) — Joseph Gentle's reference implementation; the insert-before-delete tie-break convention matches his
