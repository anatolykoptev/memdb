package sparse

import "sync"

// Registry holds named sparse embedders for multi-model dispatch.
// Thread-safe: all methods are guarded by a read-write mutex.
type Registry struct {
	mu       sync.RWMutex
	models   map[string]SparseEmbedder
	fallback string
}

// NewRegistry creates a Registry with the given fallback model name.
// When Get is called with an empty name, the fallback is used.
func NewRegistry(fallback string) *Registry {
	return &Registry{models: make(map[string]SparseEmbedder), fallback: fallback}
}

// Register adds or replaces a named embedder in the registry.
func (r *Registry) Register(name string, e SparseEmbedder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[name] = e
}

// Get returns the embedder for the given name, or the fallback if name is
// empty.
func (r *Registry) Get(name string) (SparseEmbedder, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		name = r.fallback
	}
	e, ok := r.models[name]
	return e, ok
}

// Close releases all registered embedders.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.models {
		_ = e.Close()
	}
	return nil
}
