package crypt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

const (
	privateKeyFile = "ecdsa_private.pem"
	publicKeyFile  = "ecdsa_public.pem"
)

// generateKeys генерирует новую пару ECDSA ключей.
func generateKeys() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// savePrivateKey сохраняет приватный ключ в PEM-файл.
func savePrivateKey(key *ecdsa.PrivateKey, filename string) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("ошибка маршалинга приватного ключа: %w", err)
	}

	pemBlock := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}
	return os.WriteFile(filename, pem.EncodeToMemory(pemBlock), 0600)
}

// savePublicKey сохраняет публичный ключ в PEM-файл.
func savePublicKey(key *ecdsa.PublicKey, filename string) error {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return fmt.Errorf("ошибка маршалинга публичного ключа: %w", err)
	}

	pemBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	}
	return os.WriteFile(filename, pem.EncodeToMemory(pemBlock), 0644)
}

// loadPrivateKey загружает приватный ключ из PEM-файла и десериализует его.
func loadPrivateKey(filename string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("не удалось декодировать PEM-блок из файла %s", filename)
	}
	privKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ошибка парсинга приватного ключа: %w", err)
	}
	return privKey, nil
}

// loadPublicKey загружает публичный ключ из PEM-файла и десериализует его.
func loadPublicKey(filename string) (*ecdsa.PublicKey, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("не удалось декодировать PEM-блок из файла %s", filename)
	}
	pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ошибка парсинга публичного ключа: %w", err)
	}
	pubKey, ok := pubInterface.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("публичный ключ не является ECDSA")
	}
	return pubKey, nil
}

// loadOrGenerateKeys проверяет наличие файлов с ключами, если их нет — генерирует и сохраняет, иначе загружает.
func LoadOrGenerateKeys() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	_, privErr := os.Stat(privateKeyFile)
	_, pubErr := os.Stat(publicKeyFile)

	// Если хотя бы одного файла нет, генерируем новые ключи
	if os.IsNotExist(privErr) || os.IsNotExist(pubErr) {
		fmt.Println("Ключи не найдены, генерируем новую пару ключей...")
		privKey, err := generateKeys()
		if err != nil {
			return nil, nil, fmt.Errorf("ошибка генерации ключей: %w", err)
		}
		pubKey := &privKey.PublicKey

		if err := savePrivateKey(privKey, privateKeyFile); err != nil {
			return nil, nil, fmt.Errorf("ошибка сохранения приватного ключа: %w", err)
		}
		if err := savePublicKey(pubKey, publicKeyFile); err != nil {
			return nil, nil, fmt.Errorf("ошибка сохранения публичного ключа: %w", err)
		}
		fmt.Println("Ключи успешно сгенерированы и сохранены.")
		return privKey, pubKey, nil
	}

	// Файлы существуют, загружаем ключи
	fmt.Println("Загружаем ключи из файлов...")
	privKey, err := loadPrivateKey(privateKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка загрузки приватного ключа: %w", err)
	}
	pubKey, err := loadPublicKey(publicKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка загрузки публичного ключа: %w", err)
	}
	fmt.Println("Ключи успешно загружены.")
	return privKey, pubKey, nil
}
