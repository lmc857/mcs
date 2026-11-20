package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword pre-hashes with SHA-256 so bcrypt's 72-byte limit does not
// truncate long passphrases.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("empty password")
	}
	sum := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword(sum[:], bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	sum := sha256.Sum256([]byte(password))
	return bcrypt.CompareHashAndPassword([]byte(hash), sum[:]) == nil
}

func RandomPassword(bytesLen int) (string, error) {
	if bytesLen < 12 {
		bytesLen = 12
	}
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
