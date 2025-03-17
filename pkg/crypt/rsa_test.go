package crypt

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test(t *testing.T) {
	text := rand.Text()
	private, public, err := LoadOrGenerateRSAKeys("", "")
	assert.NoError(t, err)
	enctypted, err := EncryptRSA([]byte(text), public)
	assert.NoError(t, err)
	got, err := DecryptRSA(enctypted, private)
	assert.NoError(t, err)
	assert.Equal(t, text, string(got))
}
