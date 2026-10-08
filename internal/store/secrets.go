package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const encryptedSecretPrefix = "hankenc:v1:"

type secretBox struct {
	aead cipher.AEAD
}

func (s *Store) encryptJSONSecret(value []byte) (string, error) {
	encrypted, err := s.encryptSecret(string(value))
	if err != nil {
		return "", err
	}
	if encrypted == string(value) {
		return encrypted, nil
	}
	encoded, err := json.Marshal(encrypted)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *Store) decryptJSONSecret(value string) (string, error) {
	var encoded string
	if err := json.Unmarshal([]byte(value), &encoded); err == nil && strings.HasPrefix(encoded, encryptedSecretPrefix) {
		return s.decryptSecret(encoded)
	}
	return s.decryptSecret(value)
}

func newSecretBox(key string) (*secretBox, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

func (s *Store) encryptSecret(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.HasPrefix(value, encryptedSecretPrefix) || s.secretBox == nil {
		return value, nil
	}
	nonce := make([]byte, s.secretBox.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := s.secretBox.aead.Seal(nil, nonce, []byte(value), nil)
	payload := append(nonce, ciphertext...)
	return encryptedSecretPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (s *Store) decryptSecret(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedSecretPrefix) {
		return value, nil
	}
	if s.secretBox == nil {
		return "", errors.New("secret encryption key is required")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, encryptedSecretPrefix))
	if err != nil {
		return "", err
	}
	nonceSize := s.secretBox.aead.NonceSize()
	if len(payload) <= nonceSize {
		return "", errors.New("encrypted secret payload is invalid")
	}
	plaintext, err := s.secretBox.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
