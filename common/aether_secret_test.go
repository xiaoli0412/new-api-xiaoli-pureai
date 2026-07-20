package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAetherSecretEncryptionRoundTripRejectsWrongKey(t *testing.T) {
	originalSecret := CryptoSecret
	CryptoSecret = "aether-secret-encryption-test-key"
	t.Cleanup(func() {
		CryptoSecret = originalSecret
	})

	ciphertext, err := EncryptAetherSecret("aether-control-secret")
	require.NoError(t, err)
	assert.NotContains(t, ciphertext, "aether-control-secret")

	plaintext, err := DecryptAetherSecret(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "aether-control-secret", plaintext)

	CryptoSecret = "different-aether-secret-key"
	_, err = DecryptAetherSecret(ciphertext)
	require.Error(t, err)
}
