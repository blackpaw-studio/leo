package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	// payloadVersion is the plaintext schema version the app decodes.
	payloadVersion = 1
	// aad binds a ciphertext to this protocol version.
	aad = "leo-push-v1"
	// keyBytes is the AES-256 key size every registration must carry.
	keyBytes = 32
	// collapseIDLen is the hex length of the collapse id.
	collapseIDLen = 32
)

var errBadKey = errors.New("push key must be 32 bytes")

// CollapseID is the APNs collapse/thread id for agent on daemonID: a hash,
// so the agent name never leaves the daemon unencrypted.
func CollapseID(daemonID, agent string) string {
	sum := sha256.Sum256([]byte(daemonID + "/" + agent))
	return hex.EncodeToString(sum[:])[:collapseIDLen]
}

// seal returns base64(nonce ‖ ciphertext ‖ tag) for plaintext under key.
func seal(key, plaintext []byte) (string, error) {
	if len(key) != keyBytes {
		return "", errBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("push: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("push: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("push: nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plaintext, []byte(aad))), nil
}
