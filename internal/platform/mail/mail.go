// Package mail composes and sends transactional email.
//
// Modules never send directly: they enqueue a message on the outbox, so a slow
// or failing mail server cannot slow down or fail a checkout, and failed sends
// are retried with backoff. Sensitive payloads (reset links) are redacted from
// the queue as soon as they are delivered.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
)

// JobKind is the outbox job that delivers one Message.
const JobKind = "email.send"

type Message struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

// Mailer delivers a message. Implementations must be safe for concurrent use.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// ---- composition ----

// Brand identifies the shop in every email.
type Brand struct {
	Name    string // "GOAT Store"
	BaseURL string // "https://shop.example.com", used to build links
}

// Link returns an absolute URL for a site-relative path.
func (b Brand) Link(path string) string { return strings.TrimRight(b.BaseURL, "/") + path }

type Button struct{ Label, URL string }

// Content is the structured body of an email; Compose turns it into text and HTML.
type Content struct {
	Heading    string
	Paragraphs []string
	Lines      []string // e.g. order lines, shown as a list
	Button     *Button
	Footnote   string
}

// Compose renders a message. All dynamic text is escaped in HTML, and button
// URLs must be http(s), so user-supplied values (names, product titles) cannot
// inject markup or script into an email.
func (b Brand) Compose(to, subject string, c Content) Message {
	var text, h strings.Builder
	esc := html.EscapeString

	h.WriteString(`<!doctype html><html><body style="margin:0;background:#f5f5f4;font-family:-apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1c1917">`)
	h.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:24px">`)
	h.WriteString(`<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;background:#fff;border-radius:12px;padding:32px">`)
	fmt.Fprintf(&h, `<tr><td style="font-size:18px;font-weight:800;color:#ea580c;padding-bottom:16px">%s</td></tr>`, esc(b.Name))
	if c.Heading != "" {
		fmt.Fprintf(&h, `<tr><td style="font-size:22px;font-weight:700;padding-bottom:12px">%s</td></tr>`, esc(c.Heading))
		text.WriteString(c.Heading + "\n\n")
	}
	for _, p := range c.Paragraphs {
		fmt.Fprintf(&h, `<tr><td style="font-size:15px;line-height:1.5;padding-bottom:12px">%s</td></tr>`, esc(p))
		text.WriteString(p + "\n\n")
	}
	if len(c.Lines) > 0 {
		h.WriteString(`<tr><td style="font-size:15px;line-height:1.6;padding-bottom:12px"><ul style="margin:0;padding-left:20px">`)
		for _, l := range c.Lines {
			fmt.Fprintf(&h, `<li>%s</li>`, esc(l))
			text.WriteString("  - " + l + "\n")
		}
		h.WriteString(`</ul></td></tr>`)
		text.WriteString("\n")
	}
	if c.Button != nil && safeURL(c.Button.URL) {
		fmt.Fprintf(&h, `<tr><td style="padding:8px 0 16px"><a href="%s" style="background:#c2410c;color:#fff;text-decoration:none;font-weight:600;padding:12px 20px;border-radius:8px;display:inline-block">%s</a></td></tr>`, esc(c.Button.URL), esc(c.Button.Label))
		fmt.Fprintf(&h, `<tr><td style="font-size:12px;color:#78716c;padding-bottom:12px">If the button does not work, copy this link: %s</td></tr>`, esc(c.Button.URL))
		text.WriteString(c.Button.Label + ": " + c.Button.URL + "\n\n")
	}
	if c.Footnote != "" {
		fmt.Fprintf(&h, `<tr><td style="font-size:12px;color:#78716c;padding-top:8px">%s</td></tr>`, esc(c.Footnote))
		text.WriteString(c.Footnote + "\n\n")
	}
	h.WriteString(`</table></td></tr></table></body></html>`)
	text.WriteString("— " + b.Name + "\n")
	return Message{To: to, Subject: subject, Text: text.String(), HTML: h.String()}
}

func safeURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ---- queueing ----

// Publisher is satisfied by outbox.Publisher.
type Publisher interface {
	Publish(ctx context.Context, kind string, payload any) error
}

// Outbox composes emails for a brand and queues them for delivery.
type Outbox struct {
	Brand Brand
	Pub   Publisher
}

// Send queues one email. The recipient is validated here so bad addresses fail early.
func (o Outbox) Send(ctx context.Context, to, subject string, c Content) error {
	if _, err := ParseAddress(to); err != nil {
		return fmt.Errorf("mail: invalid recipient: %w", err)
	}
	return o.Pub.Publish(ctx, JobKind, o.Brand.Compose(to, subject, c))
}

// Handler delivers queued messages; register it with outbox.Worker.Handle(JobKind, ...).
func Handler(m Mailer) outbox.Handler {
	return func(ctx context.Context, j outbox.Job) error {
		var msg Message
		if err := json.Unmarshal(j.Payload, &msg); err != nil {
			return fmt.Errorf("mail: bad payload: %w", err)
		}
		return m.Send(ctx, msg)
	}
}

// ParseAddress validates an address and rejects header-injection characters.
func ParseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("address contains a line break")
	}
	return mail.ParseAddress(s)
}

// ---- adapters ----

// LogMailer prints emails to the log (development default); links are visible there.
type LogMailer struct{ Log *slog.Logger }

func (l LogMailer) Send(_ context.Context, m Message) error {
	l.Log.Info("email (not sent: no SMTP configured)", "to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}

// MemoryMailer records messages; for tests.
type MemoryMailer struct {
	mu   sync.Mutex
	sent []Message
}

func (m *MemoryMailer) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

// Sent returns a copy of everything sent so far.
func (m *MemoryMailer) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

// To returns messages addressed to the given recipient.
func (m *MemoryMailer) To(addr string) []Message {
	var out []Message
	for _, s := range m.Sent() {
		if strings.EqualFold(s.To, addr) {
			out = append(out, s)
		}
	}
	return out
}

// SMTPConfig configures the SMTP mailer.
type SMTPConfig struct {
	Addr string // host:port
	User string
	Pass string
	From string // "Shop <no-reply@example.com>"
	TLS  string // "starttls" (default), "implicit" (port 465) or "none" (local testing)
}

type SMTPMailer struct{ cfg SMTPConfig }

func NewSMTP(cfg SMTPConfig) (*SMTPMailer, error) {
	if _, err := ParseAddress(cfg.From); err != nil {
		return nil, fmt.Errorf("MAIL_FROM: %w", err)
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return nil, fmt.Errorf("SMTP_ADDR must be host:port: %w", err)
	}
	switch cfg.TLS {
	case "", "starttls", "implicit", "none":
	default:
		return nil, fmt.Errorf("SMTP_TLS must be starttls, implicit or none (got %q)", cfg.TLS)
	}
	return &SMTPMailer{cfg: cfg}, nil
}

func (s *SMTPMailer) Send(ctx context.Context, m Message) error {
	to, err := ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mail: recipient: %w", err)
	}
	from, _ := ParseAddress(s.cfg.From)
	if strings.ContainsAny(m.Subject, "\r\n") {
		return errors.New("mail: subject contains a line break")
	}
	body, err := buildMIME(from, to, m)
	if err != nil {
		return err
	}

	host, _, _ := net.SplitHostPort(s.cfg.Addr)
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if s.cfg.TLS == "implicit" {
		conn, err = tls.DialWithDialer(&d, "tcp", s.cfg.Addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", s.cfg.Addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.cfg.TLS == "" || s.cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail: server does not offer STARTTLS (set SMTP_TLS=none only for local testing)")
		}
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if s.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMIME renders a multipart/alternative message (plain text + HTML).
func buildMIME(from, to *mail.Address, m Message) ([]byte, error) {
	var idb [12]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	boundary := "b-" + hex.EncodeToString(idb[:])
	domain := "localhost"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	var b bytes.Buffer
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", to.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(idb[:])+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, ctype)
		b.WriteString(qpEncode(body))
		b.WriteString("\r\n")
	}
	part("text/plain", m.Text)
	part("text/html", m.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}
