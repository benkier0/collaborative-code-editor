package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/benduncanson/collab-editor/session"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // handled by Railway / Cloudflare in prod
	},
}

type hub struct {
	sessions map[string]*session.Session
	mu       sync.RWMutex
	store    session.Store
}

func newHub(store session.Store) *hub {
	return &hub{
		sessions: make(map[string]*session.Session),
		store:    store,
	}
}

const defaultDoc = `// Welcome to collab-editor!
// Open this URL in multiple tabs — edits sync in real time.
//
// This is powered by Operational Transforms (OT) — the same
// algorithm behind Google Docs. Every edit is a sequence of
// retain/insert/delete operations. The server transforms
// concurrent ops to guarantee convergence.

package main

import "fmt"

func main() {
	fmt.Println("hello, world")
}
`

func (h *hub) getOrCreate(ctx context.Context, id string) *session.Session {
	h.mu.RLock()
	s, ok := h.sessions[id]
	h.mu.RUnlock()
	if ok {
		return s
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if s, ok = h.sessions[id]; ok {
		return s
	}

	doc, _, err := h.store.Load(ctx, id)
	if err != nil {
		log.Printf("store.Load(%s): %v — starting fresh", id, err)
	}
	if doc == "" {
		doc = defaultDoc
	}

	s = session.New(id, doc)
	h.sessions[id] = s

	// Persist every 30 seconds
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if err := s.Persist(context.Background(), h.store); err != nil {
				log.Printf("persist(%s): %v", id, err)
			}
		}
	}()

	return s
}

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	// URL pattern: /ws/{sessionID}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ws/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	sessionID := parts[0]

	// Sanitise: allow only alphanumeric + hyphens
	for _, ch := range sessionID {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-') {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
	}
	if len(sessionID) > 64 {
		http.Error(w, "session id too long", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	s := h.getOrCreate(r.Context(), sessionID)
	s.AddClient(conn)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	redisURL := os.Getenv("REDIS_URL")
	var store session.Store
	if redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("redis parse url: %v — falling back to mem store", err)
			store = session.NewMemStore()
		} else {
			rdb := redis.NewClient(opt)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := rdb.Ping(ctx).Err(); err != nil {
				log.Printf("redis ping failed: %v — falling back to mem store", err)
				store = session.NewMemStore()
			} else {
				log.Println("connected to Redis")
				store = session.NewRedisStore(rdb)
			}
		}
	} else {
		log.Println("REDIS_URL not set — using in-memory store (no persistence between restarts)")
		store = session.NewMemStore()
	}

	h := newHub(store)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", h.serveWS)
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("collab-editor listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
