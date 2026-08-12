package runtime

import (
	"testing"
	"time"
)

func TestManagementSessionStorePrunesExpiredEntriesAndStaysBounded(t *testing.T) {
	sessions := newManagementSessions()
	sessions.maxSize = 2
	sessions.ttl = time.Minute
	started := time.Unix(1000, 0)
	if !sessions.add("session-one", "csrf-one", started) ||
		!sessions.add("session-two", "csrf-two", started) {
		t.Fatal("session store rejected entries below its capacity")
	}
	if sessions.add("session-three", "csrf-three", started) {
		t.Fatal("session store accepted an entry beyond its capacity")
	}
	if !sessions.add("session-three", "csrf-three", started.Add(2*time.Minute)) {
		t.Fatal("session store did not reclaim expired entries")
	}
	sessions.mu.Lock()
	size := len(sessions.entries)
	sessions.mu.Unlock()
	if size != 1 {
		t.Fatalf("session store size = %d, want 1 after pruning", size)
	}
}
