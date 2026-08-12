package runtime

import (
	"crypto/sha256"
	"crypto/subtle"
)

func constantTimeTokenEqual(left string, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func constantTimeHashEqual(left [sha256.Size]byte, right [sha256.Size]byte) bool {
	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}
