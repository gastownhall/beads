// Package backendnames holds the process-local set of registered storage
// backend names.
//
// This small, standard-library-only package lets configfile classify names
// without importing the typed backend registry and the storage interfaces it
// depends on. The backends package is the only writer.
package backendnames

import "sync"

var (
	mu    sync.RWMutex
	names = make(map[string]struct{})
)

// Add records a registered backend name.
func Add(name string) {
	mu.Lock()
	defer mu.Unlock()
	names[name] = struct{}{}
}

// Remove drops a registered backend name.
func Remove(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(names, name)
}

// Has reports whether name is registered.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := names[name]
	return ok
}
