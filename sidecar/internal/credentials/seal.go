package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Stream keys must be shown again on their stream's page, which an HMAC cannot do. Their secrets
// are also kept sealed with AES-256-GCM under a key derived from the credential key, bound to what they belong to
// (the additional data), so a sealed value copied to another row does not open.

func (s *Service) aead() (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, s.key, nil, "mtxui sealed secrets v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts secret for the context ad (for example "stream 3 publish").
func (s *Service) Seal(secret, ad string) (string, error) {
	g, err := s.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(secret), []byte(ad))), nil
}

// Unseal decrypts what Seal made for the same ad.
func (s *Service) Unseal(sealed, ad string) (string, error) {
	g, err := s.aead()
	if err != nil {
		return "", err
	}
	b, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil || len(b) < g.NonceSize() {
		return "", errors.New("sealed secret is malformed")
	}
	out, err := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte(ad))
	if err != nil {
		return "", errors.New("sealed secret does not open")
	}
	return string(out), nil
}
