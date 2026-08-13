package managementtoken

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protectedfile"
)

const maxTokenFileBytes = 256

// LoadOrCreate returns the process-independent management secret used by the
// local desktop shell. The secret never needs to be displayed or placed in a
// command line; the shell exchanges it for a one-time browser grant.
func LoadOrCreate(path string) (string, error) {
	if path == "" {
		return "", errors.New("management token path is required")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		contents := make([]byte, 32)
		if _, err = rand.Read(contents); err != nil {
			return "", fmt.Errorf("generate management token: %w", err)
		}
		token := base64.RawURLEncoding.EncodeToString(contents)
		if _, err = protectedfile.Replace(path, []byte(token+"\n")); err != nil {
			return "", err
		}
		return token, nil
	case err != nil:
		return "", fmt.Errorf("inspect management token: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return "", errors.New("management token must be a regular non-symlink file")
	case info.Size() > maxTokenFileBytes:
		return "", errors.New("management token file is too large")
	}
	if err = protectedfile.EnsurePrivateFile(path); err != nil {
		return "", err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read management token: %w", err)
	}
	token := strings.TrimSuffix(string(contents), "\n")
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("management token file is malformed")
	}
	return token, nil
}
