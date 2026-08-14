package sessionidentity

import "testing"

func TestCodexIdentifierIsStableAndDoesNotRevealUpstreamValue(t *testing.T) {
	const upstream = "0198bcaa-private-session-value"
	first := Codex(upstream)
	if first != Codex(upstream) || first == upstream || len(first) != len("codex_")+16 {
		t.Fatalf("Codex identifier = %q", first)
	}
	if first == Codex(upstream+"-other") {
		t.Fatal("different upstream identifiers collided in the test vector")
	}
}
