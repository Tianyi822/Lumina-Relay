package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

type AttemptKind string

const (
	AttemptConnection AttemptKind = "connection"
	AttemptSession    AttemptKind = "session"
)

var ErrInvalidAttempt = errors.New("invalid attempt")

type Attempt struct {
	ID            string
	Kind          AttemptKind
	Username      string
	AccountExists bool
	AccountID     string
	DeviceID      string
	AuthSalt      []byte
	Challenge     []byte
	ExpiresAt     time.Time
}

type ChallengeStore struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	values   map[string]Attempt
}

func NewChallengeStore(capacity int) *ChallengeStore {
	if capacity <= 0 {
		capacity = 4096
	}
	return &ChallengeStore{
		capacity: capacity,
		ttl:      5 * time.Minute,
		now:      time.Now,
		values:   make(map[string]Attempt),
	}
}

func (s *ChallengeStore) Create(seed Attempt) (Attempt, error) {
	idRaw := make([]byte, 24)
	challenge := make([]byte, 32)
	if _, err := rand.Read(idRaw); err != nil {
		return Attempt{}, fmt.Errorf("生成 attempt id：%w", err)
	}
	if _, err := rand.Read(challenge); err != nil {
		return Attempt{}, fmt.Errorf("生成 challenge：%w", err)
	}
	seed.ID = base64.RawURLEncoding.EncodeToString(idRaw)
	seed.Challenge = challenge
	seed.ExpiresAt = s.now().Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	if len(s.values) >= s.capacity {
		var oldestID string
		var oldest time.Time
		for id, value := range s.values {
			if oldestID == "" || value.ExpiresAt.Before(oldest) {
				oldestID, oldest = id, value.ExpiresAt
			}
		}
		delete(s.values, oldestID)
	}
	s.values[seed.ID] = seed
	return cloneAttempt(seed), nil
}

// Take 单次消费 attempt；失败证明同样消费，避免在线猜测重放。
func (s *ChallengeStore) Take(id string, kind AttemptKind) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	delete(s.values, id)
	if !ok || value.Kind != kind || !value.ExpiresAt.After(s.now()) {
		return Attempt{}, ErrInvalidAttempt
	}
	return cloneAttempt(value), nil
}

func (s *ChallengeStore) cleanupLocked() {
	now := s.now()
	for id, value := range s.values {
		if !value.ExpiresAt.After(now) {
			delete(s.values, id)
		}
	}
}

func cloneAttempt(value Attempt) Attempt {
	value.AuthSalt = append([]byte(nil), value.AuthSalt...)
	value.Challenge = append([]byte(nil), value.Challenge...)
	return value
}
