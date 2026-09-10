package syncauth

import (
	"sync"

	"github.com/zalando/go-keyring"
)

// osKeyring is the default keyring implementation backed by go-keyring.
type osKeyring struct{}

func (osKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (osKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (osKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// DefaultKeyring returns the OS-backed keyring.
func DefaultKeyring() Keyring { return osKeyring{} }

// memoryKeyring is an in-memory keyring for tests.
type memoryKeyring struct {
	mu   sync.Mutex
	data map[string]string
}

// NewMemoryKeyring returns a keyring backed by process memory.
func NewMemoryKeyring() Keyring {
	return &memoryKeyring{data: make(map[string]string)}
}

func (m *memoryKeyring) key(service, user string) string {
	return service + "/" + user
}

func (m *memoryKeyring) Set(service, user, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(service, user)] = password
	return nil
}

func (m *memoryKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[m.key(service, user)]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (m *memoryKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(service, user))
	return nil
}
