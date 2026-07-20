package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

const aetherSecretCipherVersion = "a1"

func EncryptAetherSecret(plaintext string) (string, error) {
	if strings.TrimSpace(plaintext) == "" {
		return "", errors.New("aether secret cannot be empty")
	}
	block, err := aes.NewCipher(aetherSecretKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(aetherSecretCipherVersion))
	encoded := append(nonce, ciphertext...)
	return aetherSecretCipherVersion + ":" + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecryptAetherSecret(value string) (string, error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[0] != aetherSecretCipherVersion || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid encrypted aether secret")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid encrypted aether secret encoding")
	}
	block, err := aes.NewCipher(aetherSecretKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encoded) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted aether secret payload")
	}
	plaintext, err := gcm.Open(nil, encoded[:gcm.NonceSize()], encoded[gcm.NonceSize():], []byte(aetherSecretCipherVersion))
	if err != nil {
		return "", errors.New("could not decrypt aether secret")
	}
	return string(plaintext), nil
}

func aetherSecretKey() []byte {
	key := sha256.Sum256([]byte(CryptoSecret))
	return key[:]
}
