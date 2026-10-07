// Package markdown renders a small, safe subset of Markdown to HTML for content
// pages (terms, privacy policy...). Everything is HTML-escaped first and only a
// fixed set of constructs is turned back into tags, so a page body can never
// inject markup or script: headings, paragraphs, bullet and numbered lists,
// **bold**, *italic*, and links to http(s), mailto and site-relative URLs.
package markdown

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// HTML renders src. The result is safe to emit as-is.
func HTML(src string) string {
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	var para []string
	var list []string
	listTag := ""

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		out.WriteString("<p>" + strings.Join(inline(para), "<br>\n") + "</p>\n")
		para = nil
	}
	flushList := func() {
		if len(list) == 0 {
			return
		}
		out.WriteString("<" + listTag + ">\n")
		for _, item := range list {
			out.WriteString("<li>" + inlineOne(item) + "</li>\n")
		}
		out.WriteString("</" + listTag + ">\n")
		list, listTag = nil, ""
	}

	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flushPara()
			flushList()
		case strings.HasPrefix(trimmed, "### "):
			flushPara()
			flushList()
			out.WriteString("<h3>" + inlineOne(trimmed[4:]) + "</h3>\n")
		case strings.HasPrefix(trimmed, "## "):
			flushPara()
			flushList()
			out.WriteString("<h2>" + inlineOne(trimmed[3:]) + "</h2>\n")
		case strings.HasPrefix(trimmed, "# "):
			flushPara()
			flushList()
			out.WriteString("<h1>" + inlineOne(trimmed[2:]) + "</h1>\n")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			flushPara()
			if listTag != "ul" {
				flushList()
				listTag = "ul"
			}
			list = append(list, trimmed[2:])
		case orderedItem.MatchString(trimmed):
			flushPara()
			if listTag != "ol" {
				flushList()
				listTag = "ol"
			}
			list = append(list, orderedItem.ReplaceAllString(trimmed, ""))
		default:
			flushList()
			para = append(para, trimmed)
		}
	}
	flushPara()
	flushList()
	return out.String()
}

var (
	orderedItem = regexp.MustCompile(`^\d{1,3}[.)]\s+`)
	linkRe      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	boldRe      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italicRe    = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
)

func inline(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = inlineOne(l)
	}
	return out
}

// inlineOne escapes a line and then re-introduces bold, italic and safe links.
func inlineOne(s string) string {
	// Links are located on the raw text so the URL can be validated before escaping.
	var b strings.Builder
	last := 0
	for _, m := range linkRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(emphasis(html.EscapeString(s[last:m[0]])))
		text, href := s[m[2]:m[3]], s[m[4]:m[5]]
		if safeHref(href) {
			b.WriteString(`<a href="` + html.EscapeString(href) + `"` + externalAttrs(href) + `>` + emphasis(html.EscapeString(text)) + `</a>`)
		} else {
			b.WriteString(emphasis(html.EscapeString(s[m[0]:m[1]]))) // an unsafe link stays visible as plain text
		}
		last = m[1]
	}
	b.WriteString(emphasis(html.EscapeString(s[last:])))
	return b.String()
}

func emphasis(escaped string) string {
	escaped = boldRe.ReplaceAllString(escaped, "<strong>$1</strong>")
	return italicRe.ReplaceAllString(escaped, "<em>$1</em>")
}

// safeHref allows only http(s), mailto and site-relative links.
func safeHref(h string) bool {
	if strings.ContainsAny(h, "\x00\t\r\n\\") {
		return false
	}
	if strings.HasPrefix(h, "/") {
		return !strings.HasPrefix(h, "//")
	}
	u, err := url.Parse(h)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != "" || u.Path != ""
	}
	return false
}

func externalAttrs(h string) string {
	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		return ` rel="noopener noreferrer" target="_blank"`
	}
	return ""
}
