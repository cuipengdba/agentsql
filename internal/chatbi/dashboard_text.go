package chatbi

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

var dashboardTextTags = map[string]bool{
	"p": true, "div": true, "span": true, "strong": true, "b": true,
	"em": true, "i": true, "u": true, "s": true, "br": true,
	"ul": true, "ol": true, "li": true, "blockquote": true, "a": true,
}

// sanitizeDashboardText retains basic rich text without active content,
// event handlers, CSS, images, or URL schemes that can execute in a browser.
func sanitizeDashboardText(raw string) (string, error) {
	if len(raw) > 16000 {
		return "", ErrInvalidDashboard
	}
	reader := html.NewTokenizer(strings.NewReader(raw))
	var output strings.Builder
	stack := make([]string, 0, 8)
	skip := 0
	for {
		kind := reader.Next()
		if kind == html.ErrorToken {
			if reader.Err() != io.EOF {
				return "", ErrInvalidDashboard
			}
			break
		}
		token := reader.Token()
		name := strings.ToLower(token.Data)
		if name == "script" || name == "style" || name == "iframe" || name == "svg" {
			if kind == html.StartTagToken {
				skip++
			} else if kind == html.EndTagToken && skip > 0 {
				skip--
			}
			continue
		}
		if skip > 0 {
			continue
		}
		switch kind {
		case html.TextToken:
			output.WriteString(html.EscapeString(token.Data))
		case html.StartTagToken, html.SelfClosingTagToken:
			if !dashboardTextTags[name] || len(stack) >= 32 {
				continue
			}
			if name == "br" {
				output.WriteString("<br>")
				continue
			}
			output.WriteByte('<')
			output.WriteString(name)
			if name == "a" {
				for _, attr := range token.Attr {
					if strings.ToLower(attr.Key) != "href" {
						continue
					}
					link, err := url.Parse(strings.TrimSpace(attr.Val))
					if err == nil && (link.Scheme == "https" || link.Scheme == "http" || link.Scheme == "mailto") {
						output.WriteString(` href="`)
						output.WriteString(html.EscapeString(link.String()))
						output.WriteString(`" rel="noopener noreferrer"`)
					}
					break
				}
			}
			output.WriteByte('>')
			if kind == html.StartTagToken {
				stack = append(stack, name)
			} else {
				output.WriteString("</" + name + ">")
			}
		case html.EndTagToken:
			if len(stack) > 0 && stack[len(stack)-1] == name {
				stack = stack[:len(stack)-1]
				output.WriteString("</" + name + ">")
			}
		}
		if output.Len() > 8000 {
			return "", ErrInvalidDashboard
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		output.WriteString("</" + stack[i] + ">")
	}
	if output.Len() > 8000 {
		return "", ErrInvalidDashboard
	}
	return output.String(), nil
}
