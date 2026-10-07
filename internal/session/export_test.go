package session

import (
	"testing"
	"time"
)

var FailureKey = failureKey

// SetEdgeTimeout, test süresince tek bir edge'e ayrılan süreyi değiştirir.
func SetEdgeTimeout(t *testing.T, d time.Duration) {
	old := edgeTimeout
	edgeTimeout = d
	t.Cleanup(func() { edgeTimeout = old })
}

// DownCount, ulaşılamadığı kaydedilmiş edge sayısıdır.
func DownCount(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.down)
}
