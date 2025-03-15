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
	enctypted, err := EncryptMessage([]byte(text), public)
	assert.NoError(t, err)
	got, err := DecryptMessage(enctypted, private)
	assert.NoError(t, err)
	assert.Equal(t, text, string(got))
}
