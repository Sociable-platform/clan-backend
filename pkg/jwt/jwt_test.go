package jwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignAndVerify(t *testing.T) {
	pub, priv := keys(t)
	now := time.Unix(1_700_000_000, 0)
	tok, err := Sign(priv, Claims{Subject: "u_42", GameID: "g1", Name: "Rashad", Type: TypePlayer}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(pub, tok, TypePlayer, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Subject != "u_42" || c.GameID != "g1" || c.Name != "Rashad" {
		t.Fatalf("unexpected claims: %+v", c)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := keys(t)
	otherPub, _ := keys(t)
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(priv, Claims{Subject: "u_42", GameID: "g1", Type: TypePlayer}, time.Hour, now)

	cases := map[string]struct {
		key   ed25519.PublicKey
		token string
		typ   string
		at    time.Time
		want  error
	}{
		"expired":       {pub, tok, TypePlayer, now.Add(2 * time.Hour), ErrExpired},
		"wrong key":     {otherPub, tok, TypePlayer, now, ErrSignature},
		"wrong type":    {pub, tok, TypeDeveloper, now, ErrType},
		"garbage":       {pub, "not-a-token", TypePlayer, now, ErrMalformed},
		"tampered body": {pub, tamper(tok), TypePlayer, now, ErrSignature},
		"alg none":      {pub, "eyJhbGciOiJub25lIn0." + strings.Split(tok, ".")[1] + ".", TypePlayer, now, ErrMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(tc.key, tc.token, tc.typ, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// An admin token verifies only as TypeAdmin, and the other types never verify as admin.
func TestAdminTokenType(t *testing.T) {
	pub, priv := keys(t)
	now := time.Unix(1_700_000_000, 0)

	adminTok, err := Sign(priv, Claims{Subject: "a1", Name: "Ops", Type: TypeAdmin}, time.Hour, now)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}
	if c, err := Verify(pub, adminTok, TypeAdmin, now); err != nil || c.Subject != "a1" || c.Type != TypeAdmin {
		t.Fatalf("verify as admin: claims %+v, err %v", c, err)
	}
	for _, typ := range []string{TypePlayer, TypeDeveloper} {
		if _, err := Verify(pub, adminTok, typ, now); !errors.Is(err, ErrType) {
			t.Errorf("admin token verified as %s: err %v", typ, err)
		}
		tok, _ := Sign(priv, Claims{Subject: "x", Type: typ}, time.Hour, now)
		if _, err := Verify(pub, tok, TypeAdmin, now); !errors.Is(err, ErrType) {
			t.Errorf("%s token verified as admin: err %v", typ, err)
		}
	}
	if _, err := Sign(priv, Claims{Subject: "a1", Type: "superuser"}, time.Hour, now); !errors.Is(err, ErrType) {
		t.Errorf("sign unknown type: err %v", err)
	}
}

// tamper swaps the payload for one with a different subject, keeping the old signature.
func tamper(tok string) string {
	p := strings.Split(tok, ".")
	fake := b64.EncodeToString([]byte(`{"sub":"admin","gid":"g1","typ":"player","iat":1,"exp":9999999999}`))
	return p[0] + "." + fake + "." + p[2]
}

func TestParsePEMKeys(t *testing.T) {
	pub, priv := keys(t)
	privDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	pubDER, _ := x509.MarshalPKIXPublicKey(pub)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	gotPriv, err := ParsePrivateKeyPEM(privPEM)
	if err != nil {
		t.Fatal(err)
	}
	gotPub, err := ParsePublicKeyPEM(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, _ := Sign(gotPriv, Claims{Subject: "d1", Type: TypeDeveloper}, time.Minute, now)
	if _, err := Verify(gotPub, tok, TypeDeveloper, now); err != nil {
		t.Fatalf("round trip with PEM keys failed: %v", err)
	}
}
