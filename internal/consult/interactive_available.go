package consult

// InteractiveAvailable reports whether the dispatcher has an interactive
// runtime, i.e. whether an interactive dispatch can start at all.
func (d *Dispatcher) InteractiveAvailable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.interactiveRuntime != nil
}
