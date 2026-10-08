package cloud

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	notePreviewAllowedElements = map[string]bool{
		"html": true, "head": true, "body": true, "title": true, "style": true,
		"article": true, "aside": true, "main": true, "nav": true, "section": true, "header": true, "footer": true,
		"div": true, "span": true, "p": true, "br": true, "hr": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"blockquote": true, "pre": true, "code": true, "kbd": true, "samp": true,
		"b": true, "strong": true, "i": true, "em": true, "u": true, "s": true, "small": true, "mark": true, "sub": true, "sup": true,
		"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
		"table": true, "caption": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "th": true, "td": true, "colgroup": true, "col": true,
		"figure": true, "figcaption": true, "img": true, "a": true, "details": true, "summary": true,
	}
	notePreviewBlockedElements = map[string]bool{
		"script": true, "noscript": true, "iframe": true, "frame": true, "frameset": true,
		"object": true, "embed": true, "applet": true, "portal": true, "form": true,
		"input": true, "button": true, "select": true, "option": true, "textarea": true,
		"meta": true, "base": true, "link": true, "template": true, "svg": true, "math": true,
		"audio": true, "video": true, "source": true, "track": true, "canvas": true,
	}
	notePreviewCSSURL       = regexp.MustCompile(`(?is)url\s*\([^)]*\)`)
	notePreviewCSSImport    = regexp.MustCompile(`(?is)@import[^;]*(?:;|$)`)
	notePreviewCSSExecution = regexp.MustCompile(`(?is)(?:expression\s*\([^)]*\)|(?:behavior|-moz-binding)\s*:[^;]*(?:;|$))`)
)

func sanitizeNoteAttachmentHTML(source io.Reader) ([]byte, error) {
	document, err := html.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse HTML preview: %w", err)
	}
	var body bytes.Buffer
	body.WriteString("<!DOCTYPE html>")
	for child := document.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.DoctypeNode {
			continue
		}
		if err := renderSanitizedHTMLNode(&body, child); err != nil {
			return nil, err
		}
	}
	return body.Bytes(), nil
}

func renderSanitizedHTMLNode(target io.Writer, node *html.Node) error {
	switch node.Type {
	case html.TextNode:
		return html.Render(target, &html.Node{Type: html.TextNode, Data: node.Data})
	case html.CommentNode:
		return nil
	case html.ElementNode:
		tag := strings.ToLower(node.Data)
		if notePreviewBlockedElements[tag] {
			return nil
		}
		if !notePreviewAllowedElements[tag] {
			return renderSanitizedHTMLChildren(target, node)
		}
		clean := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: node.DataAtom, Attr: sanitizeNotePreviewAttributes(tag, node.Attr)}
		if tag == "style" {
			var css strings.Builder
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.TextNode {
					css.WriteString(child.Data)
				}
			}
			clean.AppendChild(&html.Node{Type: html.TextNode, Data: sanitizeNotePreviewCSS(css.String())})
		} else {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				var rendered bytes.Buffer
				if err := renderSanitizedHTMLNode(&rendered, child); err != nil {
					return err
				}
				fragment, err := html.ParseFragment(strings.NewReader(rendered.String()), clean)
				if err != nil {
					return fmt.Errorf("parse sanitized HTML fragment: %w", err)
				}
				for _, fragmentNode := range fragment {
					clean.AppendChild(fragmentNode)
				}
			}
		}
		return html.Render(target, clean)
	default:
		return renderSanitizedHTMLChildren(target, node)
	}
}

func renderSanitizedHTMLChildren(target io.Writer, node *html.Node) error {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := renderSanitizedHTMLNode(target, child); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeNotePreviewAttributes(tag string, attrs []html.Attribute) []html.Attribute {
	clean := make([]html.Attribute, 0, len(attrs)+2)
	for _, attr := range attrs {
		key := strings.ToLower(attr.Key)
		if strings.HasPrefix(key, "on") || key == "srcdoc" || key == "nonce" || key == "integrity" || key == "formaction" {
			continue
		}
		value := strings.TrimSpace(attr.Val)
		switch {
		case key == "style":
			if value = strings.TrimSpace(sanitizeNotePreviewCSS(value)); value != "" {
				clean = append(clean, html.Attribute{Key: key, Val: value})
			}
		case key == "class" || key == "id" || key == "title" || key == "lang" || key == "dir" || key == "role" || strings.HasPrefix(key, "aria-") || strings.HasPrefix(key, "data-"):
			clean = append(clean, html.Attribute{Key: key, Val: value})
		case tag == "img" && key == "src" && safeNotePreviewDataImage(value):
			clean = append(clean, html.Attribute{Key: key, Val: value})
		case tag == "img" && (key == "alt" || key == "width" || key == "height"):
			clean = append(clean, html.Attribute{Key: key, Val: value})
		case tag == "a" && key == "href" && strings.HasPrefix(value, "#"):
			clean = append(clean, html.Attribute{Key: key, Val: value})
		case (tag == "td" || tag == "th") && (key == "colspan" || key == "rowspan"):
			clean = append(clean, html.Attribute{Key: key, Val: value})
		case tag == "col" && key == "span":
			clean = append(clean, html.Attribute{Key: key, Val: value})
		}
	}
	if tag == "a" {
		clean = append(clean, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
	}
	return clean
}

func sanitizeNotePreviewCSS(value string) string {
	value = notePreviewCSSURL.ReplaceAllString(value, "")
	value = notePreviewCSSImport.ReplaceAllString(value, "")
	return notePreviewCSSExecution.ReplaceAllString(value, "")
}

func safeNotePreviewDataImage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/gif;base64,", "data:image/webp;base64,"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
