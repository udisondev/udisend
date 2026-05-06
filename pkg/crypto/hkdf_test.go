package crypto_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/crypto"
)

func TestHKDF_Deterministic(t *testing.T) {
	t.Parallel()
	a, err := crypto.HKDF([]byte("secret"), []byte("salt"), []byte("info"), 32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := crypto.HKDF([]byte("secret"), []byte("salt"), []byte("info"), 32)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("HKDF must be deterministic")
	}
}

func TestHKDF_DiffersWithContext(t *testing.T) {
	t.Parallel()
	a, _ := crypto.HKDF([]byte("secret"), []byte("salt"), []byte("info-A"), 32)
	b, _ := crypto.HKDF([]byte("secret"), []byte("salt"), []byte("info-B"), 32)
	if bytes.Equal(a, b) {
		t.Fatalf("different info should give different output")
	}
}

func TestHKDF_RejectsZeroLength(t *testing.T) {
	t.Parallel()
	if _, err := crypto.HKDF([]byte("s"), nil, nil, 0); !errors.Is(err, crypto.ErrEmptyOutput) {
		t.Fatalf("err = %v, want ErrEmptyOutput", err)
	}
}

func TestHKDF_LengthMatches(t *testing.T) {
	t.Parallel()
	for _, l := range []int{1, 32, 64, 128} {
		out, err := crypto.HKDF([]byte("s"), nil, nil, l)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != l {
			t.Errorf("length %d: got %d", l, len(out))
		}
	}
}
