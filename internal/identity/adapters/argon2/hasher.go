// Package argon2 implements password hashing with argon2id.
package argon2

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the OWASP minimum for argon2id (19 MiB, t=2, p=1).
const (
	memory  = 19 * 1024
	time    = 2
	threads = 1
	keyLen  = 32
)

// sem bounds concurrent hashes: each one allocates ~19 MiB, so an unbounded burst
// of logins could exhaust memory long before the CPU saturates.
var sem = make(chan struct{}, max(2, runtime.NumCPU()))

type Hasher struct{}

func (Hasher) Hash(password string) (string, error) {
	sem <- struct{}{}
	defer func() { <-sem }()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func (Hasher) Verify(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	sem <- struct{}{}
	defer func() { <-sem }()
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
