package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store is the persistence interface for session documents.
type Store interface {
	Load(ctx context.Context, sessionID string) (doc string, rev int, err error)
	Save(ctx context.Context, sessionID string, doc string, rev int) error
}

type redisStore struct {
	client *redis.Client
	ttl    time.Duration
}

type sessionRecord struct {
	Doc string `json:"doc"`
	Rev int    `json:"rev"`
}

// NewRedisStore returns a Store backed by Redis.
func NewRedisStore(client *redis.Client) Store {
	return &redisStore{client: client, ttl: 24 * time.Hour}
}

func (s *redisStore) key(sessionID string) string {
	return fmt.Sprintf("collab:session:%s", sessionID)
}

func (s *redisStore) Load(ctx context.Context, sessionID string) (string, int, error) {
	val, err := s.client.Get(ctx, s.key(sessionID)).Result()
	if err == redis.Nil {
		return "", 0, nil // new session
	}
	if err != nil {
		return "", 0, fmt.Errorf("redis get: %w", err)
	}

	var rec sessionRecord
	if err := json.Unmarshal([]byte(val), &rec); err != nil {
		return "", 0, fmt.Errorf("json decode: %w", err)
	}
	return rec.Doc, rec.Rev, nil
}

func (s *redisStore) Save(ctx context.Context, sessionID string, doc string, rev int) error {
	data, err := json.Marshal(sessionRecord{Doc: doc, Rev: rev})
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.key(sessionID), data, s.ttl).Err()
}

// MemStore is an in-memory Store for testing and Railway free tier
// (when Redis isn't available at startup).
type MemStore struct {
	mu   sync.RWMutex
	data map[string]sessionRecord
}

func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string]sessionRecord)}
}

func (m *MemStore) Load(_ context.Context, sessionID string) (string, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if rec, ok := m.data[sessionID]; ok {
		return rec.Doc, rec.Rev, nil
	}
	return "", 0, nil
}

func (m *MemStore) Save(_ context.Context, sessionID string, doc string, rev int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = sessionRecord{Doc: doc, Rev: rev}
	return nil
}
