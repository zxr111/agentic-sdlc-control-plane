package knowledge

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

type ParsedDocument struct {
	Content       string
	ParserVersion string
	Warnings      []string
}

// ParseDocument converts supported source formats into deterministic Markdown-
// like text. It never executes source content and preserves headings, lists,
// code, and table cell boundaries where the input format exposes them.
func ParseDocument(sourceType, contentType string, raw []byte) (ParsedDocument, error) {
	if len(raw) == 0 {
		return ParsedDocument{}, nil
	}
	kind := strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(kind, "html") || strings.EqualFold(sourceType, "CONFLUENCE_STORAGE") {
		parsed, err := parseHTML(raw)
		if err != nil {
			return ParsedDocument{}, fmt.Errorf("parse HTML knowledge source: %w", err)
		}
		return ParsedDocument{Content: parsed, ParserVersion: "html-structural-v1"}, nil
	}
	return ParsedDocument{Content: string(raw), ParserVersion: ParserVersion}, nil
}

func parseHTML(raw []byte) (string, error) {
	root, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "nav", "noscript":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				out.WriteString("\n" + strings.Repeat("#", int(node.Data[1]-'0')) + " ")
			case "p", "div", "section", "article", "tr", "pre", "blockquote":
				out.WriteString("\n")
			case "li":
				out.WriteString("\n- ")
			case "th", "td":
				out.WriteString(" | ")
			case "br":
				out.WriteString("\n")
			}
		}
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return NormalizeText(out.String()), nil
}
