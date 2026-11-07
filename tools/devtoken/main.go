// Command devtoken prints a signed test token for local development.
//
// Platform does not issue player tokens yet, so this tool signs one with the same
// private key platform uses. Social cannot tell the difference, which makes it
// possible to test social's token checks today.
//
//	go run ./tools/devtoken -player u_42 -name Ali   // player token, valid 1 hour
//	go run ./tools/devtoken -developer               // developer token (social must reject it)
//	go run ./tools/devtoken -ttl -1m                 // already expired (social must reject it)
//
// For local development only: it needs the private key file, which never leaves your computer.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
)

func main() {
	keyFile := flag.String("key", envOr("PLATFORM_JWT_PRIVATE_KEY_FILE", "secrets/platform_jwt_private.pem"), "platform private key (PEM file)")
	player := flag.String("player", "u_1", "the game's own player ID (becomes the token's sub)")
	game := flag.String("game", "00000000-0000-0000-0000-000000000001", "game ID, a UUID (becomes the token's gid)")
	name := flag.String("name", "Test Player", "display name")
	ttl := flag.Duration("ttl", time.Hour, "how long the token is valid; negative makes an expired token")
	developer := flag.Bool("developer", false, "make a developer token instead of a player token")
	flag.Parse()

	keyPEM, err := os.ReadFile(*keyFile)
	if err != nil {
		fail("read private key: %v (see README: JWT signing key)", err)
	}
	key, err := jwt.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		fail("%v", err)
	}

	claims := jwt.Claims{Subject: *player, GameID: *game, Name: *name, Type: jwt.TypePlayer}
	if *developer {
		claims = jwt.Claims{Subject: *player, Name: *name, Type: jwt.TypeDeveloper}
	}

	token, err := jwt.Sign(key, claims, *ttl, time.Now())
	if err != nil {
		fail("%v", err)
	}
	fmt.Println(token)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "devtoken: "+format+"\n", a...)
	os.Exit(1)
}
