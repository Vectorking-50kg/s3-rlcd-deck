package runtime

import (
	"crypto/sha256"
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

func TestManagementSessionStoreRotatesCSRFWithoutExtendingExpiry(t *testing.T) {
	sessions := newManagementSessions()
	sessions.ttl = time.Minute
	started := time.Unix(1000, 0)
	if !sessions.add("session", "old-csrf", started) {
		t.Fatal("session store rejected entry")
	}
	if !sessions.rotateCSRF("session", "new-csrf", started.Add(30*time.Second)) {
		t.Fatal("session store rejected CSRF rotation")
	}
	entry, found := sessions.lookupKey(sha256.Sum256([]byte("session")), started.Add(30*time.Second))
	if !found || entry.csrfHash != sha256.Sum256([]byte("new-csrf")) {
		t.Fatal("session store did not publish the rotated CSRF hash")
	}
	if entry.expiresAt != started.Add(time.Minute) {
		t.Fatalf("CSRF rotation extended expiry to %v", entry.expiresAt)
	}
}
