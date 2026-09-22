package config

import "sync"

// Writer serializes in-process read-modify-write cycles on leo.yaml. Every
// writer in one process (daemon IPC handlers, web handlers) must share the
// same Writer, injected at construction: each save is load → mutate → save,
// so two unserialized writers can interleave and drop an edit. Writes from
// other processes (the CLI) are not covered and remain last-writer-wins.
type Writer struct {
	mu sync.Mutex
}

// NewWriter returns a Writer for one config file.
func NewWriter() *Writer { return &Writer{} }

// Lock takes the write lock and returns its release:
//
//	defer w.Lock()()
func (w *Writer) Lock() func() {
	w.mu.Lock()
	return w.mu.Unlock
}
