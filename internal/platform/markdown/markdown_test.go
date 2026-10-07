package markdown

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

func TestRendersTheSupportedSubset(t *testing.T) {
	src := "# Terms\n\nWelcome to **our shop**, we *care*.\nSecond line.\n\n## Delivery\n\n- fast\n- cheap\n\n1. one\n2. two\n\nSee [returns](/pages/returns) or [site](https://example.com) or [mail](mailto:hi@example.com).\n"
	got := HTML(src)
	for _, want := range []string{
		"<h1>Terms</h1>", "<h2>Delivery</h2>", "<strong>our shop</strong>", "<em>care</em>", "we *care*" /* absent */, "<br>",
		"<ul>", "<li>fast</li>", "<ol>", "<li>two</li>", `<a href="/pages/returns">returns</a>`,
		`<a href="https://example.com" rel="noopener noreferrer" target="_blank">site</a>`, `<a href="mailto:hi@example.com">mail</a>`,
	} {
		if want == "we *care*" {
			if strings.Contains(got, want) {
				t.Errorf("emphasis markers should have been consumed: %s", got)
			}
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q\n%s", want, got)
		}
	}
}

var (
	plainTag  = regexp.MustCompile(`</?(p|h1|h2|h3|ul|ol|li|strong|em)>|<br>`)
	closeA    = regexp.MustCompile(`</a>`)
	anchorTag = regexp.MustCompile(`<a href="([^"<>]*)"( rel="noopener noreferrer" target="_blank")?>`)
)

// assertSafe fails if the output could execute anything: after removing the exact
// tags the renderer is allowed to emit, no angle bracket may remain, and every link
// must point at http(s), mailto or a site-relative path.
func assertSafe(t *testing.T, input, out string) {
	t.Helper()
	rest := plainTag.ReplaceAllString(out, "")
	for _, m := range anchorTag.FindAllStringSubmatch(rest, -1) {
		href := strings.ToLower(html.UnescapeString(m[1]))
		ok := strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:") ||
			(strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"))
		if !ok {
			t.Errorf("input %q: link to %q is not allowed", input, href)
		}
	}
	rest = anchorTag.ReplaceAllString(rest, "")
	rest = closeA.ReplaceAllString(rest, "")
	if strings.ContainsAny(rest, "<>") {
		t.Errorf("input %q produced markup beyond the allowed tags: %q (leftover %q)", input, out, rest)
	}
}

func TestNothingCanInjectMarkupOrScript(t *testing.T) {
	hostile := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"[click](javascript:alert(1))",
		"[click](JaVaScRiPt:alert(1))",
		"[click](data:text/html;base64,PHNjcmlwdD4=)",
		"[click](//evil.example/x)",
		"[click](/\\evil.example)",
		"[click](https:///nohost)",
		"[click](vbscript:msgbox(1))",
		"**<b onclick=x>bold</b>**",
		"# <iframe src=//evil></iframe>",
		"- <svg onload=alert(1)>",
		"[a](https://ok.example\" onmouseover=\"alert(1))",
		"[](x)\n[x]()\n[[nested](/a)](/b)",
		"[x](https://a.example)<script>",
		"*<script>*",
		"[<img src=x onerror=alert(1)>](https://ok.example)",
		"[x](/ok\"><script>alert(1)</script>)",
	}
	for _, h := range hostile {
		assertSafe(t, h, HTML(h))
	}
}

func TestEscapesInsideLinksAndAttributes(t *testing.T) {
	out := HTML(`[a "quoted" <b>](https://example.com/?a=1&b="2")`)
	if strings.Contains(out, "<b>") || strings.Contains(out, `"2"`) {
		t.Fatalf("link text/URL not escaped: %s", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Fatalf("ampersand not escaped: %s", out)
	}
}

func TestEmptyAndOddInput(t *testing.T) {
	for _, in := range []string{"", "\n\n\n", "   ", "\r\n\r\n", "#", "# ", "- ", "**", "[", "](", strings.Repeat("*", 1000), strings.Repeat("[a](/b)", 500)} {
		_ = HTML(in) // must not panic or hang
	}
	if HTML("") != "" {
		t.Fatal("empty input should render nothing")
	}
}
