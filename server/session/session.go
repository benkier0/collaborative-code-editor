package session

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/benduncanson/collab-editor/ot"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// MsgType identifies the kind of WebSocket message.
type MsgType string

const (
	MsgOp       MsgType = "op"       // client sends an operation
	MsgAck      MsgType = "ack"      // server acks a client's own op
	MsgBroadcast MsgType = "broadcast" // server broadcasts another client's op
	MsgInit     MsgType = "init"     // server sends initial doc state on connect
	MsgPresence MsgType = "presence" // cursor position update
	MsgError    MsgType = "error"    // server signals a problem
)

// Message is the wire format for all WebSocket communication.
type Message struct {
	Type     MsgType         `json:"type"`
	ClientID string          `json:"clientId,omitempty"`
	Rev      int             `json:"rev,omitempty"`      // server revision
	Op       *ot.Op          `json:"op,omitempty"`
	Doc      string          `json:"doc,omitempty"`      // used in init
	Cursor   *CursorPos      `json:"cursor,omitempty"`
	Color    string          `json:"color,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// CursorPos is a client's cursor position in the document.
type CursorPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// client represents one connected WebSocket peer.
type client struct {
	id     string
	conn   *websocket.Conn
	send   chan Message
	color  string
	cursor *CursorPos
}

// Session manages a single shared document and the set of connected clients.
type Session struct {
	id      string
	doc     string
	rev     int
	opLog   []ot.Op // indexed by revision; opLog[i] was applied to produce revision i+1
	clients map[string]*client
	mu      sync.RWMutex

	// Channels
	register   chan *client
	unregister chan *client
	incoming   chan clientMsg
	done       chan struct{}
}

type clientMsg struct {
	client *client
	msg    Message
}

var clientColors = []string{
	"#4F8EF7", "#F76B4F", "#4FBF7A", "#F7C94F",
	"#9B4FF7", "#F74F9B", "#4FF7F0", "#F7904F",
}

func New(id, initialDoc string) *Session {
	s := &Session{
		id:         id,
		doc:        initialDoc,
		rev:        0,
		clients:    make(map[string]*client),
		register:   make(chan *client, 8),
		unregister: make(chan *client, 8),
		incoming:   make(chan clientMsg, 64),
		done:       make(chan struct{}),
	}
	go s.run()
	return s
}

// AddClient registers a WebSocket connection with this session.
func (s *Session) AddClient(conn *websocket.Conn) {
	colorIdx := len(s.clients) % len(clientColors)
	c := &client{
		id:    uuid.New().String(),
		conn:  conn,
		send:  make(chan Message, 32),
		color: clientColors[colorIdx],
	}
	s.register <- c
}

func (s *Session) run() {
	for {
		select {
		case c := <-s.register:
			s.mu.Lock()
			s.clients[c.id] = c
			s.mu.Unlock()

			// Send initial state
			c.send <- Message{
				Type:     MsgInit,
				ClientID: c.id,
				Rev:      s.rev,
				Doc:      s.doc,
				Color:    c.color,
			}

			go s.readPump(c)
			go s.writePump(c)

		case c := <-s.unregister:
			s.mu.Lock()
			if _, ok := s.clients[c.id]; ok {
				delete(s.clients, c.id)
				close(c.send)
			}
			s.mu.Unlock()

		case cm := <-s.incoming:
			s.handleMessage(cm.client, cm.msg)
		}
	}
}

func (s *Session) handleMessage(c *client, msg Message) {
	switch msg.Type {
	case MsgOp:
		s.handleOp(c, msg)
	case MsgPresence:
		s.handlePresence(c, msg)
	}
}

func (s *Session) handleOp(c *client, msg Message) {
	if msg.Op == nil {
		c.send <- Message{Type: MsgError, Error: "missing op"}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	op := *msg.Op
	op.BaseRev = msg.Rev

	// Transform op against all ops committed since its base revision.
	// This is the server-side OT: each incoming op must be rebased
	// on top of everything that happened after its base revision.
	var err error
	for i := op.BaseRev; i < s.rev; i++ {
		serverOp := s.opLog[i]
		op, _, err = ot.Transform(op, serverOp)
		if err != nil {
			log.Printf("transform error: %v", err)
			c.send <- Message{Type: MsgError, Error: "transform failed"}
			return
		}
	}

	// Apply the transformed op to the server document
	newDoc, err := ot.Apply(s.doc, op)
	if err != nil {
		log.Printf("apply error (client=%s): %v", c.id, err)
		c.send <- Message{Type: MsgError, Error: "apply failed — please refresh"}
		return
	}

	s.doc = newDoc
	s.opLog = append(s.opLog, op)
	s.rev++

	// Ack the submitting client
	c.send <- Message{
		Type:     MsgAck,
		Rev:      s.rev,
		ClientID: c.id,
	}

	// Broadcast the transformed op to all other clients
	broadcast := Message{
		Type:     MsgBroadcast,
		ClientID: c.id,
		Rev:      s.rev,
		Op:       &op,
	}
	for id, peer := range s.clients {
		if id != c.id {
			select {
			case peer.send <- broadcast:
			default:
				// Slow client — drop and let them reconnect
				log.Printf("dropping slow client %s", id)
			}
		}
	}
}

func (s *Session) handlePresence(c *client, msg Message) {
	if msg.Cursor == nil {
		return
	}
	c.cursor = msg.Cursor

	// Broadcast cursor position to all other clients
	broadcast := Message{
		Type:     MsgPresence,
		ClientID: c.id,
		Cursor:   msg.Cursor,
		Color:    c.color,
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, peer := range s.clients {
		if id != c.id {
			select {
			case peer.send <- broadcast:
			default:
			}
		}
	}
}

func (s *Session) readPump(c *client) {
	defer func() { s.unregister <- c }()

	c.conn.SetReadLimit(64 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws read error (client=%s): %v", c.id, err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("json decode error: %v", err)
			continue
		}
		s.incoming <- clientMsg{client: c, msg: msg}
	}
}

func (s *Session) writePump(c *client) {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("json encode error: %v", err)
				continue
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// DocSnapshot returns the current document and revision under the lock.
func (s *Session) DocSnapshot() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc, s.rev
}

// Persist saves the session doc to the store. Called periodically.
func (s *Session) Persist(ctx context.Context, store Store) error {
	doc, rev := s.DocSnapshot()
	return store.Save(ctx, s.id, doc, rev)
}
