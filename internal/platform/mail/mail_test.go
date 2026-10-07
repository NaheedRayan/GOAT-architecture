package mail

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestComposeEscapesEverything(t *testing.T) {
	b := Brand{Name: "Shop <b>", BaseURL: "https://shop.example"}
	m := b.Compose("a@example.com", "Hi", Content{
		Heading:    `Hello <script>alert(1)</script>`,
		Paragraphs: []string{`Thanks, "Eve" & <i>friends</i>`},
		Lines:      []string{`Item <img src=x onerror=alert(1)> × 2`},
		Button:     &Button{Label: "Open <now>", URL: "https://shop.example/reset?token=abc&x=1"},
	})
	for _, bad := range []string{"<script>", "<img", "<i>friends", "<b>"} {
		if strings.Contains(m.HTML, bad) {
			t.Errorf("HTML contains unescaped %q", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp; &lt;i&gt;friends", `href="https://shop.example/reset?token=abc&amp;x=1"`} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
	if !strings.Contains(m.Text, "https://shop.example/reset?token=abc&x=1") || !strings.Contains(m.Text, "Item <img src=x") {
		t.Error("the plain-text part should carry the raw text and the link")
	}
}

func TestComposeRefusesDangerousButtonURLs(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x", "//evil.example", "/relative"} {
		m := Brand{Name: "S"}.Compose("a@b.co", "s", Content{Button: &Button{Label: "Go", URL: u}})
		if strings.Contains(m.HTML, "<a href") {
			t.Errorf("button with URL %q was rendered", u)
		}
	}
}

func TestParseAddressRejectsHeaderInjection(t *testing.T) {
	for _, bad := range []string{"a@b.co\r\nBcc: x@y.z", "a@b.co\nSubject: x", "not an address", ""} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseAddress("Alice <alice@example.com>"); err != nil {
		t.Errorf("valid address rejected: %v", err)
	}
}

// fakeSMTP is a minimal SMTP server that records the message it receives.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	to   []string
	data string
}

func startSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { io.WriteString(c, s+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.TrimSpace(line[10:])
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, strings.TrimSpace(line[8:]))
			f.mu.Unlock()
			w("250 ok")
		case cmd == "DATA":
			w("354 go ahead")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSMTPDeliversAMultipartMessage(t *testing.T) {
	srv := startSMTP(t)
	m, err := NewSMTP(SMTPConfig{Addr: srv.ln.Addr().String(), From: "Shop <no-reply@shop.example>", TLS: "none"})
	if err != nil {
		t.Fatal(err)
	}
	msg := Brand{Name: "Shop", BaseURL: "https://shop.example"}.Compose("buyer@example.com", "Your order café", Content{
		Heading: "Thanks", Paragraphs: []string{"Ünïcode paragraph"}, Button: &Button{Label: "View", URL: "https://shop.example/orders/1"},
	})
	if err := m.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.Contains(srv.from, "no-reply@shop.example") || len(srv.to) != 1 || !strings.Contains(srv.to[0], "buyer@example.com") {
		t.Fatalf("envelope from=%q to=%v", srv.from, srv.to)
	}
	for _, want := range []string{"To: <buyer@example.com>", "multipart/alternative", "text/plain", "text/html", "Subject: =?utf-8?q?Your_order_caf=C3=A9?=", "MIME-Version: 1.0"} {
		if !strings.Contains(srv.data, want) {
			t.Errorf("message lacks %q\n%s", want, srv.data)
		}
	}
}

func TestSMTPRefusesInjectionAndPlaintextByDefault(t *testing.T) {
	srv := startSMTP(t)
	m, _ := NewSMTP(SMTPConfig{Addr: srv.ln.Addr().String(), From: "a@shop.example", TLS: "none"})
	if err := m.Send(context.Background(), Message{To: "x@y.co", Subject: "hi\r\nBcc: z@z.co"}); err == nil {
		t.Error("a subject with a line break must be refused")
	}
	if err := m.Send(context.Background(), Message{To: "x@y.co\r\nBcc: z@z.co", Subject: "s"}); err == nil {
		t.Error("a recipient with a line break must be refused")
	}
	strict, _ := NewSMTP(SMTPConfig{Addr: srv.ln.Addr().String(), From: "a@shop.example"}) // default: starttls
	if err := strict.Send(context.Background(), Message{To: "x@y.co", Subject: "s", Text: "t"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("default mode must refuse a server without STARTTLS, got %v", err)
	}
}

func TestNewSMTPValidatesConfig(t *testing.T) {
	for name, cfg := range map[string]SMTPConfig{
		"bad from": {Addr: "h:25", From: "nope"},
		"bad addr": {Addr: "nohost", From: "a@b.co"},
		"bad tls":  {Addr: "h:25", From: "a@b.co", TLS: "maybe"},
	} {
		if _, err := NewSMTP(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
