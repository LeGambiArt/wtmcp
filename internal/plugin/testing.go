package plugin

// NewManagerForTest creates a Manager with no dependencies for use
// in tests that need to populate manifests and handles directly.
func NewManagerForTest() *Manager {
	return &Manager{
		handles:   make(map[string]*Handle),
		manifests: make(map[string]*Manifest),
	}
}

// SetManifest sets a manifest in the manager for testing.
func (m *Manager) SetManifest(name string, manifest *Manifest) {
	m.manifests[name] = manifest
}

// SetHandle marks a plugin as loaded by creating a placeholder handle.
func (m *Manager) SetHandle(name string) {
	manifest := m.manifests[name]
	m.handles[name] = &Handle{manifest: manifest}
}

// SetSessionDir sets the session directory for testing.
func (m *Manager) SetSessionDir(dir string) {
	m.sessionDir = dir
}

// SetDisabledPlugin marks a plugin as disabled for testing.
func (m *Manager) SetDisabledPlugin(name, reason string) {
	if m.disabled == nil {
		m.disabled = make(map[string]DisabledPlugin)
	}
	m.disabled[name] = DisabledPlugin{
		Name:     name,
		Reason:   reason,
		Manifest: m.manifests[name],
	}
}
