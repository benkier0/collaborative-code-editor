/**
 * OT Client state machine.
 *
 * The client is always in one of three states:
 *
 *   Synchronized  — no pending or in-flight ops. Safe to apply server ops directly.
 *
 *   AwaitingAck   — one op has been sent to the server; we are waiting for ack.
 *                   Further local edits go into `buffer`.
 *                   Incoming server ops must be transformed against `inFlight`.
 *
 *   AwaitingAckWithBuffer — same as above but `buffer` is non-empty.
 *
 * On ack: buffer becomes the new inFlight (if non-empty), else → Synchronized.
 *
 * This is the standard OT client algorithm (Jupiter/Wave).
 */

export type OpComponent =
  | { type: 'retain'; n: number }
  | { type: 'insert'; content: string }
  | { type: 'delete'; n: number }

export interface Op {
  components: OpComponent[]
  baseRev?: number
}

export interface CursorPos {
  line: number
  column: number
}

type State = 'synchronized' | 'awaiting_ack' | 'awaiting_ack_with_buffer'

export class OTClient {
  private state: State = 'synchronized'
  private serverRev: number = 0
  private inFlight: Op | null = null
  private buffer: Op | null = null

  constructor(
    private readonly sendOp: (op: Op, rev: number) => void,
    private readonly applyToEditor: (op: Op) => void
  ) {}

  /** Called when the user makes a local edit. */
  applyLocal(op: Op): void {
    switch (this.state) {
      case 'synchronized':
        this.inFlight = op
        this.state = 'awaiting_ack'
        this.sendOp(op, this.serverRev)
        break

      case 'awaiting_ack':
        this.buffer = op
        this.state = 'awaiting_ack_with_buffer'
        break

      case 'awaiting_ack_with_buffer':
        // Compose the new op into the buffer
        this.buffer = composeOps(this.buffer!, op)
        break
    }
  }

  /** Called when the server broadcasts another client's op. */
  applyRemote(op: Op, rev: number): void {
    this.serverRev = rev

    switch (this.state) {
      case 'synchronized':
        this.applyToEditor(op)
        break

      case 'awaiting_ack': {
        const [transformedOp, newInFlight] = transform(op, this.inFlight!)
        this.inFlight = newInFlight
        this.applyToEditor(transformedOp)
        break
      }

      case 'awaiting_ack_with_buffer': {
        // Transform op against inFlight, then against buffer
        const [afterFlight, newInFlight] = transform(op, this.inFlight!)
        const [afterBuffer, newBuffer] = transform(afterFlight, this.buffer!)
        this.inFlight = newInFlight
        this.buffer = newBuffer
        this.applyToEditor(afterBuffer)
        break
      }
    }
  }

  /** Called when the server acks our in-flight op. */
  serverAck(rev: number): void {
    this.serverRev = rev

    if (this.state === 'awaiting_ack') {
      this.inFlight = null
      this.state = 'synchronized'
    } else if (this.state === 'awaiting_ack_with_buffer') {
      this.inFlight = this.buffer
      this.buffer = null
      this.state = 'awaiting_ack'
      this.sendOp(this.inFlight!, this.serverRev)
    }
  }

  getServerRev(): number {
    return this.serverRev
  }

  setServerRev(rev: number): void {
    this.serverRev = rev
  }
}

/**
 * Transform two concurrent ops (a, b) built against the same document.
 * Returns [a', b'] where:
 *   apply(apply(doc, a), b') == apply(apply(doc, b), a')
 *
 * Mirror of the Go implementation — must stay in sync.
 */
export function transform(a: Op, b: Op): [Op, Op] {
  const aPrime: OpComponent[] = []
  const bPrime: OpComponent[] = []

  const ai = new ComponentIter(a.components)
  const bi = new ComponentIter(b.components)

  while (!ai.done() || !bi.done()) {
    const ac = ai.peek()
    const bc = bi.peek()

    if (ac && ac.type === 'insert') {
      aPrime.push(ac)
      bPrime.push({ type: 'retain', n: charCount(ac) })
      ai.consume()
      continue
    }
    if (bc && bc.type === 'insert') {
      bPrime.push(bc)
      aPrime.push({ type: 'retain', n: charCount(bc) })
      bi.consume()
      continue
    }

    if (!ac || !bc) break

    if (ac.type === 'retain' && bc.type === 'retain') {
      const n = Math.min(ac.n, bc.n)
      aPrime.push({ type: 'retain', n })
      bPrime.push({ type: 'retain', n })
      ai.advance(n)
      bi.advance(n)
    } else if (ac.type === 'delete' && bc.type === 'delete') {
      const n = Math.min(ac.n, bc.n)
      ai.advance(n)
      bi.advance(n)
    } else if (ac.type === 'delete' && bc.type === 'retain') {
      const n = Math.min(ac.n, bc.n)
      aPrime.push({ type: 'delete', n })
      ai.advance(n)
      bi.advance(n)
    } else if (ac.type === 'retain' && bc.type === 'delete') {
      const n = Math.min(ac.n, bc.n)
      bPrime.push({ type: 'delete', n })
      ai.advance(n)
      bi.advance(n)
    }
  }

  return [{ components: normalise(aPrime) }, { components: normalise(bPrime) }]
}

/** Compose: returns single op equivalent to applying a then b. */
export function composeOps(a: Op, b: Op): Op {
  const result: OpComponent[] = []
  const ai = new ComponentIter(a.components)
  const bi = new ComponentIter(b.components)

  while (!ai.done() || !bi.done()) {
    const bc = bi.peek()
    if (bc && bc.type === 'insert') {
      result.push(bc)
      bi.consume()
      continue
    }

    const ac = ai.peek()
    if (ac && ac.type === 'delete') {
      result.push(ac)
      ai.consume()
      continue
    }

    if (!ac || !bc) break

    if (ac.type === 'retain' && bc.type === 'retain') {
      const n = Math.min(ac.n, bc.n)
      result.push({ type: 'retain', n })
      ai.advance(n)
      bi.advance(n)
    } else if (ac.type === 'retain' && bc.type === 'delete') {
      const n = Math.min(ac.n, bc.n)
      result.push({ type: 'delete', n })
      ai.advance(n)
      bi.advance(n)
    } else if (ac.type === 'insert' && bc.type === 'retain') {
      const n = Math.min(ac.content.length, bc.n)
      result.push({ type: 'insert', content: ac.content.slice(0, n) })
      ai.advanceInsert(n)
      bi.advance(n)
    } else if (ac.type === 'insert' && bc.type === 'delete') {
      const n = Math.min(ac.content.length, bc.n)
      ai.advanceInsert(n)
      bi.advance(n)
    }
  }

  return { components: normalise(result) }
}

/** Convert a Monaco model change event to an Op. */
export function monacoChangeToOp(
  change: { rangeOffset: number; rangeLength: number; text: string },
  docLength: number
): Op {
  const components: OpComponent[] = []

  if (change.rangeOffset > 0) {
    components.push({ type: 'retain', n: change.rangeOffset })
  }
  if (change.rangeLength > 0) {
    components.push({ type: 'delete', n: change.rangeLength })
  }
  if (change.text.length > 0) {
    components.push({ type: 'insert', content: change.text })
  }

  const tail = docLength - change.rangeOffset - change.rangeLength
  if (tail > 0) {
    components.push({ type: 'retain', n: tail })
  }

  return { components: normalise(components) }
}

/** Apply an op to a string. Used for verification. */
export function applyOp(doc: string, op: Op): string {
  let result = ''
  let pos = 0

  for (const c of op.components) {
    if (c.type === 'retain') {
      result += doc.slice(pos, pos + c.n)
      pos += c.n
    } else if (c.type === 'insert') {
      result += c.content
    } else if (c.type === 'delete') {
      pos += c.n
    }
  }

  return result
}

function charCount(c: OpComponent): number {
  if (c.type === 'insert') return c.content.length
  if (c.type === 'retain' || c.type === 'delete') return c.n
  return 0
}

function normalise(cs: OpComponent[]): OpComponent[] {
  const out: OpComponent[] = []
  for (const c of cs) {
    if (out.length === 0) { out.push({ ...c }); continue }
    const last = out[out.length - 1]
    if (last.type === 'retain' && c.type === 'retain') {
      (last as { type: 'retain'; n: number }).n += c.n
    } else if (last.type === 'delete' && c.type === 'delete') {
      (last as { type: 'delete'; n: number }).n += c.n
    } else if (last.type === 'insert' && c.type === 'insert') {
      (last as { type: 'insert'; content: string }).content += c.content
    } else {
      out.push({ ...c })
    }
  }
  return out.filter(c => charCount(c) > 0)
}

class ComponentIter {
  private idx = 0
  private offset = 0

  constructor(private comps: OpComponent[]) {}

  done(): boolean { return this.idx >= this.comps.length }

  peek(): OpComponent | null {
    if (this.done()) return null
    const c = this.comps[this.idx]
    if (c.type === 'retain') return { type: 'retain', n: c.n - this.offset }
    if (c.type === 'delete') return { type: 'delete', n: c.n - this.offset }
    if (c.type === 'insert') return { type: 'insert', content: c.content.slice(this.offset) }
    return null
  }

  consume(): void { this.idx++; this.offset = 0 }

  advance(n: number): void {
    let rem = n
    while (rem > 0 && !this.done()) {
      const c = this.comps[this.idx]
      const size = c.type === 'insert'
        ? c.content.length - this.offset
        : (c as { n: number }).n - this.offset
      if (size <= rem) {
        rem -= size; this.idx++; this.offset = 0
      } else {
        this.offset += rem; rem = 0
      }
    }
  }

  advanceInsert(n: number): void { this.advance(n) }
}
