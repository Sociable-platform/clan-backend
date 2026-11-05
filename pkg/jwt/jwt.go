// Package jwt signs and verifies the platform's tokens with Ed25519 (alg "EdDSA").
//
// Only the platform service holds the private key and signs tokens.
// Other services (social) hold only the public key and verify tokens themselves,
// so they never need to call platform on each request.
package jwt

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token types. Player endpoints must reject developer tokens and the other way around.
const (
	TypePlayer    = "player"
	TypeDeveloper = "developer"
)

// Claims are the fields inside a token.
type Claims struct {
	Subject   string `json:"sub"`            // player: the game's own player ID; developer: developer ID
	GameID    string `json:"gid,omitempty"`  // player tokens only: the game the player belongs to
	Name      string `json:"name,omitempty"` // display name
	Type      string `json:"typ"`            // TypePlayer or TypeDeveloper
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

var (
	ErrMalformed = errors.New("jwt: malformed token")
	ErrSignature = errors.New("jwt: invalid signature")
	ErrExpired   = errors.New("jwt: token expired")
	ErrType      = errors.New("jwt: wrong token type")
)

var b64 = base64.RawURLEncoding

const header = `{"alg":"EdDSA","typ":"JWT"}`

// Sign returns a signed token for c. IssuedAt and ExpiresAt are set from now and ttl.
func Sign(key ed25519.PrivateKey, c Claims, ttl time.Duration, now time.Time) (string, error) {
	if c.Type != TypePlayer && c.Type != TypeDeveloper {
		return "", ErrType
	}
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(ttl).Unix()
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString(payload)
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// Verify checks the signature and expiry of token and that it has the wanted type.
func Verify(key ed25519.PublicKey, token, wantType string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrMalformed
	}

	hdrJSON, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, ErrMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hdrJSON, &hdr) != nil || hdr.Alg != "EdDSA" {
		return c, ErrMalformed // refuse any other algorithm, including "none"
	}

	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return c, ErrMalformed
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return c, ErrSignature
	}

	payload, err := b64.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return Claims{}, ErrMalformed
	}
	if now.Unix() >= c.ExpiresAt {
		return Claims{}, ErrExpired
	}
	if c.Type != wantType {
		return Claims{}, ErrType
	}
	return c, nil
}

// ParsePrivateKeyPEM reads an Ed25519 private key in PKCS#8 PEM form
// (what `openssl genpkey -algorithm ed25519` writes).
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: no PEM block found in private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse private key: %w", err)
	}
	pk, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("jwt: private key is not Ed25519")
	}
	return pk, nil
}

// ParsePublicKeyPEM reads an Ed25519 public key in PKIX PEM form
// (what `openssl pkey -in private.pem -pubout` writes).
func ParsePublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: no PEM block found in public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse public key: %w", err)
	}
	pk, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("jwt: public key is not Ed25519")
	}
	return pk, nil
}
