//go:build darwin

package installation

import (
	"bytes"
	"testing"
)

func TestLaunchAgentDocumentEscapesPathsAndUsesExactLabel(t *testing.T) {
	document, err := launchAgentDocument(
		launchAgentLabelPrefix+".0123456789ab",
		launchSpec{Executable: `/Applications/S3 & Deck`, DataDirectory: `/Users/名字/Data`, DeviceHubAddress: "127.0.0.1:7780"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte("com.vectorking.s3-rlcd-deck-companion.0123456789ab"),
		[]byte(`/Applications/S3 &amp; Deck`),
		[]byte(`/Users/名字/Data`),
	} {
		if !bytes.Contains(document, expected) {
			t.Fatalf("LaunchAgent document is missing %q", expected)
		}
	}
}
