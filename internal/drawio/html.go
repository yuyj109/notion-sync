package drawio

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"text/template"
)

const maxSVGSize = 20 << 20

//go:embed templates/viewer.html.tmpl
var viewerHTML string

var viewerTemplate = template.Must(template.New("viewer").Parse(viewerHTML))

// Render builds a Notion HTML block from a draw.io SVG without network or file I/O.
func Render(svg []byte, filename string) ([]byte, error) {
	if len(svg) > maxSVGSize {
		return nil, errors.New("SVG exceeds the 20 MiB input limit")
	}
	diagram, err := extractDiagram(svg)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(struct {
		XML string `json:"xml"`
	}{XML: diagram})
	if err != nil {
		return nil, fmt.Errorf("encode diagram: %w", err)
	}
	// Only escaped title text, a base64 image, and encoding/json output enter
	// the template. Marshal's HTML escaping prevents closing the data script.
	values := struct {
		Title       string
		SVGBase64   string
		DiagramJSON string
	}{
		Title:       html.EscapeString(filename),
		SVGBase64:   base64.StdEncoding.EncodeToString(svg),
		DiagramJSON: string(data),
	}
	var output bytes.Buffer
	if err = viewerTemplate.Execute(&output, values); err != nil {
		return nil, fmt.Errorf("render HTML: %w", err)
	}
	return output.Bytes(), nil
}
