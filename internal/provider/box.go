package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// Box seals credentials kept on disk with AES-256-GCM. A sealed value is
// base64 of nonce (12 bytes), tag (16 bytes), and ciphertext.
type Box struct{ aead cipher.AEAD }

// NewBox takes a base64 32-byte key, SECRET_KEY.
func NewBox(key string) (*Box, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("SECRET_KEY must be 32 bytes, base64 encoded")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead}, nil
}

// Seal encrypts plaintext.
func (b *Box) Seal(plaintext string) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nil, nonce, []byte(plaintext), nil)
	body, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	out := append(append(nonce, tag...), body...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value.
func (b *Box) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 28 {
		return "", errors.New("not a sealed value")
	}
	nonce, tag, body := raw[:12], raw[12:28], raw[28:]
	plain, err := b.aead.Open(nil, nonce, append(append([]byte{}, body...), tag...), nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
