package crypt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// GenerateRSAKeys генерирует пару RSA 2048-битных ключей
func GenerateRSAKeys() (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	if err = privateKey.Validate(); err != nil {
		return nil, nil, err
	}

	return privateKey, &privateKey.PublicKey, nil
}

func DecryptMessage(ciphertext []byte, privateKey *rsa.PrivateKey) ([]byte, error) {
	return rsa.DecryptOAEP(
		sha256.New(),
		rand.Reader,
		privateKey,
		ciphertext,
		nil,
	)
}

// MarshalPublicKey преобразует публичный ключ в PEM-кодированные байты
func MarshalPublicKey(pubKey *rsa.PublicKey) ([]byte, error) {
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return nil, err
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	})

	return pubPEM, nil
}

// ParsePublicKey преобразует PEM-кодированные байты обратно в публичный ключ
func ParsePublicKey(pubPEM []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pubPEM)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("failed to parse PEM block containing public key")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return pub.(*rsa.PublicKey), nil
}

func EncryptMessage(plaintext []byte, publicKey *rsa.PublicKey) ([]byte, error) {
	return rsa.EncryptOAEP(
		sha256.New(),
		rand.Reader,
		publicKey,
		plaintext,
		nil,
	)
}

func LoadOrGenerateRSAKeys(privateKeyPath, publicKeyPath string) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	if privateKeyPath == "" && publicKeyPath == "" {
		privateKeyPath = "private_key.pem"
		publicKeyPath = "public_key.pem"

		privateExists := fileExists(privateKeyPath)
		publicExists := fileExists(publicKeyPath)

		if privateExists && publicExists {
			return LoadRSAKeys(privateKeyPath, publicKeyPath)
		}

		privateKey, publicKey, err := GenerateRSAKeys()
		if err != nil {
			return nil, nil, err
		}

		err = savePrivateKey(privateKey, privateKeyPath)
		if err != nil {
			return nil, nil, err
		}
		err = savePublicKey(publicKey, publicKeyPath)
		if err != nil {
			return nil, nil, err
		}

		return privateKey, publicKey, nil
	}

	return LoadRSAKeys(privateKeyPath, publicKeyPath)
}

func LoadRSAKeys(privateKeyPath, publicKeyPath string) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privatePEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read private key: %v", err)
	}

	privateBlock, _ := pem.Decode(privatePEM)
	if privateBlock == nil {
		return nil, nil, errors.New("failed to decode private key PEM")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(privateBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %v", err)
	}

	publicPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read public key: %v", err)
	}

	publicBlock, _ := pem.Decode(publicPEM)
	if publicBlock == nil {
		return nil, nil, errors.New("failed to decode public key PEM")
	}

	publicKey, err := x509.ParsePKIXPublicKey(publicBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		return nil, nil, errors.New("public key is not RSA public key")
	}

	return privateKey, rsaPublicKey, nil
}

func ExtractPublicKey(mesh string) (*rsa.PublicKey, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(mesh)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %v", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}

	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("invalid PEM block type: expected 'PUBLIC KEY', got '%s'", block.Type)
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	rsaPubKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("parsed key is not an RSA public key")
	}

	return rsaPubKey, nil
}

func savePrivateKey(privateKey *rsa.PrivateKey, path string) error {
	privateFile, err := os.Create(path)
	if err != nil {
		return err
	}
	defer privateFile.Close()

	privatePEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	return pem.Encode(privateFile, privatePEM)
}

func savePublicKey(publicKey *rsa.PublicKey, path string) error {
	publicFile, err := os.Create(path)
	if err != nil {
		return err
	}
	defer publicFile.Close()

	publicBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return err
	}

	publicPEM := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicBytes,
	}

	return pem.Encode(publicFile, publicPEM)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
