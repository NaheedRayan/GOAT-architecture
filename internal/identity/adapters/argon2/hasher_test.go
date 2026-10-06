package argon2

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h := Hasher{}
	enc, err := h.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$") {
		t.Fatalf("unexpected format %q", enc)
	}
	if !h.Verify("correct horse", enc) || h.Verify("wrong", enc) {
		t.Fatal("verify gave the wrong answer")
	}
	again, _ := h.Hash("correct horse")
	if again == enc {
		t.Fatal("hashes must be salted")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "$bcrypt$a$b$c$d"} {
		if h.Verify("correct horse", bad) {
			t.Errorf("Verify accepted malformed hash %q", bad)
		}
	}
}
