import * as monaco from 'monaco-editor'
import { OTClient, monacoChangeToOp, applyOp, Op, CursorPos } from './ot-client'

// ── Session ID from URL ──────────────────────────────────────────────────────
function getOrCreateSessionId(): string {
  const path = window.location.pathname.replace(/^\//, '')
  if (path && /^[a-zA-Z0-9-]{1,64}$/.test(path)) return path

  const id = Math.random().toString(36).slice(2, 10)
  window.history.replaceState(null, '', `/${id}`)
  return id
}

const SESSION_ID = getOrCreateSessionId()
document.title = `collab-editor · ${SESSION_ID}`

// ── Monaco setup ─────────────────────────────────────────────────────────────
const editorEl = document.getElementById('editor')!

const editor = monaco.editor.create(editorEl, {
  value: '',
  language: 'go',
  theme: 'vs-dark',
  fontSize: 14,
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  renderWhitespace: 'selection',
  automaticLayout: true,
})

const model = editor.getModel()!

// ── Cursor decorations for remote peers ──────────────────────────────────────
interface PeerState {
  color: string
  decorationIds: string[]
}

const peers = new Map<string, PeerState>()

function updateRemoteCursor(clientId: string, cursor: CursorPos, color: string) {
  let peer = peers.get(clientId)
  if (!peer) {
    peer = { color, decorationIds: [] }
    peers.set(clientId, peer)
  }

  const lineCount = model.getLineCount()
  const line = Math.min(Math.max(cursor.line, 1), lineCount)
  const maxCol = model.getLineMaxColumn(line)
  const col = Math.min(Math.max(cursor.column, 1), maxCol)

  peer.decorationIds = editor.deltaDecorations(peer.decorationIds, [
    {
      range: new monaco.Range(line, col, line, col),
      options: {
        className: `remote-cursor-${clientId.slice(0, 8)}`,
        beforeContentClassName: `remote-cursor-caret`,
        stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
      },
    },
  ])

  // Inject CSS for this peer's cursor color
  const styleId = `cursor-style-${clientId.slice(0, 8)}`
  if (!document.getElementById(styleId)) {
    const style = document.createElement('style')
    style.id = styleId
    style.textContent = `
      .remote-cursor-${clientId.slice(0, 8)} {
        border-left: 2px solid ${color};
      }
    `
    document.head.appendChild(style)
  }
}

function removeRemoteCursor(clientId: string) {
  const peer = peers.get(clientId)
  if (peer) {
    editor.deltaDecorations(peer.decorationIds, [])
    peers.delete(clientId)
  }
}

// ── WebSocket connection ──────────────────────────────────────────────────────
const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
const wsHost = window.location.host
const wsUrl = `${wsProto}//${wsHost}/ws/${SESSION_ID}`

let ws: WebSocket
let otClient: OTClient
let suppressLocalEvents = false
let myClientId = ''
let connected = false

const statusEl = document.getElementById('status')!
const peersEl = document.getElementById('peers')!

function setStatus(text: string, ok: boolean) {
  statusEl.textContent = text
  statusEl.className = ok ? 'status ok' : 'status error'
}

function connect() {
  setStatus('connecting…', false)
  ws = new WebSocket(wsUrl)

  ws.onopen = () => {
    connected = true
    setStatus('connected', true)
  }

  ws.onclose = () => {
    connected = false
    setStatus('disconnected — reconnecting…', false)
    setTimeout(connect, 2000)
  }

  ws.onerror = (e) => {
    console.error('ws error', e)
  }

  ws.onmessage = (event) => {
    const msg = JSON.parse(event.data)

    switch (msg.type) {
      case 'init':
        handleInit(msg)
        break
      case 'ack':
        otClient.serverAck(msg.rev)
        break
      case 'broadcast':
        handleBroadcast(msg)
        break
      case 'presence':
        if (msg.cursor) {
          updateRemoteCursor(msg.clientId, msg.cursor, msg.color)
          updatePeerList()
        }
        break
      case 'error':
        console.error('server error:', msg.error)
        if (msg.error?.includes('refresh')) {
          setStatus('sync error — please refresh', false)
        }
        break
    }
  }
}

function handleInit(msg: { clientId: string; rev: number; doc: string; color: string }) {
  myClientId = msg.clientId

  otClient = new OTClient(
    (op: Op, rev: number) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'op', op, rev }))
      }
    },
    (op: Op) => {
      applyOpToEditor(op)
    }
  )
  otClient.setServerRev(msg.rev)

  suppressLocalEvents = true
  model.setValue(msg.doc)
  suppressLocalEvents = false

  setStatus(`connected · ${msg.clientId.slice(0, 8)}`, true)
}

function handleBroadcast(msg: { clientId: string; rev: number; op: Op }) {
  otClient.applyRemote(msg.op, msg.rev)
}

/**
 * Apply a remote op to the Monaco model without triggering our own
 * change listener. We suppress, apply, restore.
 */
function applyOpToEditor(op: Op) {
  suppressLocalEvents = true
  try {
    const currentDoc = model.getValue()
    const newDoc = applyOp(currentDoc, op)

    // Compute Monaco edit from the op components
    const edits: monaco.editor.IIdentifiedSingleEditOperation[] = []
    let pos = 0

    for (const c of op.components) {
      if (c.type === 'retain') {
        pos += c.n
      } else if (c.type === 'insert') {
        const monacoPos = model.getPositionAt(pos)
        edits.push({
          range: new monaco.Range(
            monacoPos.lineNumber, monacoPos.column,
            monacoPos.lineNumber, monacoPos.column
          ),
          text: c.content,
        })
        pos += 0 // insert doesn't advance pos in old doc
      } else if (c.type === 'delete') {
        const startPos = model.getPositionAt(pos)
        const endPos = model.getPositionAt(pos + c.n)
        edits.push({
          range: new monaco.Range(
            startPos.lineNumber, startPos.column,
            endPos.lineNumber, endPos.column
          ),
          text: '',
        })
        pos += c.n
      }
    }

    model.applyEdits(edits)

    // Verify convergence in dev mode
    if (import.meta.env.DEV) {
      const actual = model.getValue()
      if (actual !== newDoc) {
        console.warn('OT divergence detected', { expected: newDoc, actual })
      }
    }
  } finally {
    suppressLocalEvents = false
  }
}

// ── Local change listener ─────────────────────────────────────────────────────
model.onDidChangeContent((event) => {
  if (suppressLocalEvents || !otClient) return

  // Monaco batches changes — we apply them sequentially
  for (const change of event.changes) {
    const docLengthBefore =
      model.getValue().length -
      change.text.length +
      change.rangeLength

    const op = monacoChangeToOp(
      {
        rangeOffset: change.rangeOffset,
        rangeLength: change.rangeLength,
        text: change.text,
      },
      docLengthBefore
    )

    otClient.applyLocal(op)
  }
})

// ── Cursor presence ──────────────────────────────────────────────────────────
let cursorThrottle: ReturnType<typeof setTimeout> | null = null

editor.onDidChangeCursorPosition((event) => {
  if (!connected || !ws || ws.readyState !== WebSocket.OPEN) return

  if (cursorThrottle) clearTimeout(cursorThrottle)
  cursorThrottle = setTimeout(() => {
    ws.send(JSON.stringify({
      type: 'presence',
      cursor: {
        line: event.position.lineNumber,
        column: event.position.column,
      },
    }))
  }, 50)
})

// ── Peer list UI ─────────────────────────────────────────────────────────────
function updatePeerList() {
  peersEl.textContent = `${peers.size + 1} connected`
}

// ── Share URL ────────────────────────────────────────────────────────────────
const shareBtn = document.getElementById('share-btn')!
shareBtn.addEventListener('click', () => {
  navigator.clipboard.writeText(window.location.href).then(() => {
    shareBtn.textContent = 'copied!'
    setTimeout(() => { shareBtn.textContent = 'share' }, 1500)
  })
})

// ── Boot ─────────────────────────────────────────────────────────────────────
connect()
