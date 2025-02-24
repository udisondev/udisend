package crypt

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// ECDSAKeyPair хранит разобранные ECDSA публичный и приватный ключи.
type ECDSAKeyPair struct {
	PublicKey  *ecdsa.PublicKey
	PrivateKey *ecdsa.PrivateKey
}

// LoadECDSAKeys читает и парсит ECDSA-ключи из указанных PEM-файлов.
func LoadECDSAKeys(pubPath, privPath string) (*ECDSAKeyPair, error) {
	// Чтение публичного ключа
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать публичный ключ: %w", err)
	}
	pubBlock, _ := pem.Decode(pubBytes)
	if pubBlock == nil {
		return nil, fmt.Errorf("не удалось декодировать PEM-блок публичного ключа")
	}
	pubInterface, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("не удалось распарсить публичный ключ: %w", err)
	}
	pubKey, ok := pubInterface.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("публичный ключ не является ECDSA")
	}

	// Чтение приватного ключа
	privBytes, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать приватный ключ: %w", err)
	}
	privBlock, _ := pem.Decode(privBytes)
	if privBlock == nil {
		return nil, fmt.Errorf("не удалось декодировать PEM-блок приватного ключа")
	}
	privKey, err := x509.ParseECPrivateKey(privBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("не удалось распарсить приватный ключ: %w", err)
	}

	return &ECDSAKeyPair{
		PublicKey:  pubKey,
		PrivateKey: privKey,
	}, nil
}

func SerializePublicKey(pub *ecdsa.PublicKey) ([]byte, error) {
	derBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("ошибка маршалинга публичного ключа: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: derBytes,
	})

	return pemBytes, nil
}
