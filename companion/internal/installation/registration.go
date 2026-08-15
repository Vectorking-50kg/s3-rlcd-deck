package installation

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
)

func registrationIdentifier(root string) string {
	normalized := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:6])
}
