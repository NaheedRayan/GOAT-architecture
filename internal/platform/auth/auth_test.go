package auth

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func newSigner(t *testing.T, ttl time.Duration) *Signer {
	t.Helper()
	s, ephemeral, err := NewSigner("", ttl)
	if err != nil || !ephemeral {
		t.Fatalf("NewSigner: ephemeral=%v err=%v", ephemeral, err)
	}
	return s
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	s := newSigner(t, time.Minute)
	uid := uuid.New()
	tok, err := s.Issue(uid, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Verify(tok)
	if err != nil || c.UserID != uid || c.Role != RoleAdmin {
		t.Fatalf("Verify = %+v, %v", c, err)
	}
}

func TestVerifyRejectsBadTokens(t *testing.T) {
	s := newSigner(t, time.Minute)
	other := newSigner(t, time.Minute)
	expired := newSigner(t, -time.Minute)

	good, _ := s.Issue(uuid.New(), RoleCustomer)
	foreign, _ := other.Issue(uuid.New(), RoleCustomer)
	old, _ := expired.Issue(uuid.New(), RoleCustomer)
	// alg=none must never be accepted.
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{Issuer: issuer, Subject: uuid.NewString(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	for name, tok := range map[string]string{
		"garbage": "not-a-token", "empty": "", "other key": foreign, "tampered": good + "x", "alg none": none,
	} {
		if _, err := s.Verify(tok); err == nil {
			t.Errorf("%s token was accepted", name)
		}
	}
	// Expiry is checked against the verifier's clock, using the shared key.
	s2 := &Signer{priv: expired.priv, pub: expired.pub, ttl: time.Minute}
	if _, err := s2.Verify(old); err == nil {
		t.Error("expired token was accepted")
	}
}

func TestSeedIsDeterministic(t *testing.T) {
	const seed = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 0..31
	a, eph, err := NewSigner(seed, time.Minute)
	if err != nil || eph {
		t.Fatal(err, eph)
	}
	b, _, _ := NewSigner(seed, time.Minute)
	if !a.pub.Equal(b.pub) || len(a.priv) != ed25519.PrivateKeySize {
		t.Fatal("same seed must give the same key")
	}
	if _, _, err := NewSigner("short", time.Minute); err == nil {
		t.Fatal("bad seed accepted")
	}
}

func TestRequireAndRole(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	serve := func(h http.Handler, claims *Claims, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if claims != nil {
			r = r.WithContext(WithClaims(r.Context(), *claims))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := serve(Require(ok), nil, "/cart?x=1"); w.Code != 303 || w.Header().Get("Location") != "/login?next=%2Fcart%3Fx%3D1" {
		t.Errorf("anonymous: %d %s", w.Code, w.Header().Get("Location"))
	}
	cust := &Claims{UserID: uuid.New(), Role: RoleCustomer}
	adm := &Claims{UserID: uuid.New(), Role: RoleAdmin}
	if w := serve(Require(ok), cust, "/"); w.Code != 200 {
		t.Errorf("customer on Require: %d", w.Code)
	}
	if w := serve(RequireRole(RoleAdmin)(ok), cust, "/admin"); w.Code != 403 {
		t.Errorf("customer on admin: %d", w.Code)
	}
	if w := serve(RequireRole(RoleAdmin)(ok), adm, "/admin"); w.Code != 200 {
		t.Errorf("admin on admin: %d", w.Code)
	}
}
