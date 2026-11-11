package drawio

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// Accept SVG document declarations without internal entity/DTD definitions.
var svgDoctype = regexp.MustCompile(`^DOCTYPE\s+svg(?:\s+(?:PUBLIC\s+(?:"[^"]*"|'[^']*')\s+(?:"[^"]*"|'[^']*')|SYSTEM\s+(?:"[^"]*"|'[^']*')))?\s*$`)

func extractDiagram(svg []byte) (string, error) {
	var content string
	_, err := inspectXML(svg, func(element xml.StartElement, depth int) error {
		if depth != 1 {
			return nil
		}
		if element.Name.Local != "svg" || (element.Name.Space != "" && element.Name.Space != "http://www.w3.org/2000/svg") {
			return errors.New("input is not an SVG document")
		}
		for _, attribute := range element.Attr {
			if attribute.Name.Space == "" && attribute.Name.Local == "content" {
				content = attribute.Value
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid SVG: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("SVG has no draw.io data; export SVG with 'Include a copy of my diagram' enabled")
	}
	// Some exporters URI-encode the entire content attribute. Never unescape
	// ordinary XML: labels may legitimately contain percent signs or plus signs.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(content)), "%3c") {
		content, err = url.PathUnescape(strings.TrimSpace(content))
		if err != nil {
			return "", errors.New("invalid URI-encoded draw.io data")
		}
	}
	pages := 0
	root, err := inspectXML([]byte(content), func(element xml.StartElement, depth int) error {
		if depth == 2 && element.Name.Local == "diagram" {
			pages++
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid draw.io XML: %w", err)
	}
	if root.Space != "" || (root.Local != "mxfile" && root.Local != "mxGraphModel") || (root.Local == "mxfile" && pages == 0) {
		return "", errors.New("SVG content must contain an mxfile with diagram pages or an mxGraphModel")
	}
	// Preserve the original model, compressed pages, styles, page IDs and layers.
	return content, nil
}

// Stream XML to validate its structure without constructing or rewriting a DOM.
// encoding/xml does not fetch external DTDs or resolve their entities.
func inspectXML(data []byte, visit func(xml.StartElement, int) error) (xml.Name, error) {
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	var root xml.Name
	depth := 0
	seenRoot := false
	seenDoctype := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return root, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 128 {
				return root, errors.New("XML nesting exceeds 128 levels")
			}
			if depth == 1 {
				if seenRoot {
					return root, errors.New("multiple XML roots")
				}
				root = token.Name
				seenRoot = true
				if seenDoctype && root.Local != "svg" {
					return root, errors.New("SVG DOCTYPE must precede an SVG root")
				}
			}
			attributes := make(map[xml.Name]bool, len(token.Attr))
			for _, attribute := range token.Attr {
				if attributes[attribute.Name] {
					return root, errors.New("duplicate XML attribute")
				}
				attributes[attribute.Name] = true
			}
			if err = visit(token, depth); err != nil {
				return root, err
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			if seenRoot || seenDoctype || !svgDoctype.Match(token) {
				return root, errors.New("only an SVG DOCTYPE without an internal subset is supported")
			}
			seenDoctype = true
		case xml.ProcInst:
			if token.Target != "xml" || seenRoot {
				return root, errors.New("unsupported XML processing instruction")
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(token)) != "" {
				return root, errors.New("unexpected text outside XML root")
			}
		}
	}
	if !seenRoot || depth != 0 {
		return root, errors.New("empty or incomplete XML document")
	}
	return root, nil
}
