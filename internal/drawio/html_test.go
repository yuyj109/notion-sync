package drawio

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

const model = `<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" parent="1" value="日本語 + 100%" style="html=1;rounded=1;" vertex="1"><mxGeometry x="10" y="20" width="180" height="60" as="geometry"/></mxCell></root></mxGraphModel>`

func wrapSVG(content string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" content="` + html.EscapeString(content) + `"><rect width="10" height="10"/></svg>`)
}

func diagramJSON(t *testing.T, output []byte) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<script id="diagram-data" type="application/json">(.*?)</script>`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatal("diagram data script not found")
	}
	var data struct {
		XML string `json:"xml"`
	}
	if err := json.Unmarshal(match[1], &data); err != nil {
		t.Fatal(err)
	}
	return data.XML
}

func TestRenderPreservesSVGAndDiagram(t *testing.T) {
	svg, err := os.ReadFile("testdata/notion-sample.drawio.svg")
	if err != nil {
		t.Fatal(err)
	}
	xml, err := extractDiagram(svg)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Render(svg, "構成 & network.drawio.svg")
	if err != nil {
		t.Fatal(err)
	}
	if got := diagramJSON(t, output); got != xml {
		t.Fatal("diagram XML was rewritten")
	}
	if strings.Count(xml, "<diagram ") != 2 || !strings.Contains(xml, `id="notes"`) {
		t.Fatal("fixture must include multiple pages and layers")
	}
	match := regexp.MustCompile(`src="data:image/svg\+xml;base64,([^"]+)"`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatal("SVG fallback not found")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(match[1]))
	if err != nil || !bytes.Equal(decoded, svg) {
		t.Fatal("original SVG must be embedded byte-for-byte", err)
	}
	if !bytes.Contains(output, []byte("構成 &amp; network.drawio.svg")) || bytes.Contains(output, []byte("{{.DiagramJSON}}")) {
		t.Fatal("template values not applied")
	}
	if !bytes.Contains(output, []byte("https://cdn.jsdelivr.net/gh/jgraph/drawio@v32.6.10/")) || !bytes.Contains(output, []byte(`integrity="sha384-`)) {
		t.Fatal("pinned CDN and integrity attribute missing")
	}
}

func TestRenderPreservesCompressedAndUncompressedModels(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte(url.PathEscape(model))); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	mxfile := `<mxfile><diagram name="圧縮ページ" id="stable-page-id">` + base64.StdEncoding.EncodeToString(compressed.Bytes()) + `</diagram><diagram name="非圧縮">` + model + `</diagram></mxfile>`
	for _, content := range []string{model, mxfile} {
		output, err := Render(wrapSVG(content), "input.svg")
		if err != nil {
			t.Fatal(err)
		}
		if got := diagramJSON(t, output); got != content {
			t.Fatal("model, page IDs, styles or compressed content changed")
		}
	}
}

func TestURIEncodedContent(t *testing.T) {
	got, err := extractDiagram(wrapSVG(url.PathEscape(model)))
	if err != nil || got != model {
		t.Fatal("percent encoding or literal plus sign changed", err)
	}
}

func TestScriptAndFilenameEscaping(t *testing.T) {
	attack := `</script><script>window.injected=true</script>&` + "\u2028\u2029"
	xml := strings.Replace(model, "日本語 + 100%", html.EscapeString(attack), 1)
	output, err := Render(wrapSVG(xml), `</title><script>window.injected=true</script>.svg`)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte(`<script>window.injected=true</script>`)) {
		t.Fatal("input escaped the title or JSON script")
	}
	if got := diagramJSON(t, output); got != xml {
		t.Fatal("escaped JSON must round-trip the original XML")
	}
}

func TestRenderWithSVGDoctype(t *testing.T) {
	for _, declaration := range []string{
		`<!DOCTYPE svg>`,
		`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">`,
		"<!DOCTYPE svg\n PUBLIC '-//W3C//DTD SVG 1.1//EN'\n 'http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd'>",
		`<!DOCTYPE svg SYSTEM "https://invalid.example/svg.dtd">`,
		`<!DOCTYPE svg SYSTEM "file:///nonexistent/svg.dtd">`,
	} {
		svg := append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+declaration+"\n"), wrapSVG(model)...)
		output, err := Render(svg, "export.drawio.svg")
		if err != nil {
			t.Fatalf("DOCTYPE rejected: %s: %v", declaration, err)
		}
		if got := diagramJSON(t, output); got != model {
			t.Fatal("DOCTYPE changed the diagram XML")
		}
		match := regexp.MustCompile(`src="data:image/svg\+xml;base64,([^"]+)"`).FindSubmatch(output)
		if len(match) != 2 {
			t.Fatal("SVG fallback not found")
		}
		decoded, err := base64.StdEncoding.DecodeString(string(match[1]))
		if err != nil || !bytes.Equal(decoded, svg) {
			t.Fatal("DOCTYPE or original SVG changed", err)
		}
	}
}

func TestRejectInvalidSVGOrDiagram(t *testing.T) {
	cases := map[string][]byte{
		"not XML":           []byte("not an SVG"),
		"not SVG":           []byte(`<html/>`),
		"ordinary SVG":      []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"wrong namespace":   []byte(`<svg xmlns="urn:other" content="test"/>`),
		"broken SVG":        []byte(`<svg content="&lt;mxfile/&gt;">`),
		"bad graph XML":     wrapSVG(`<mxfile>`),
		"not a graph":       wrapSVG(`<html/>`),
		"no pages":          wrapSVG(`<mxfile/>`),
		"extra graph root":  wrapSVG(model + model),
		"extra SVG root":    append(wrapSVG(model), []byte(`<svg/>`)...),
		"duplicate content": []byte(`<svg content="x" content="y"/>`),
		"internal DTD":      append([]byte(`<!DOCTYPE svg [<!ENTITY x "hidden">]>`), wrapSVG(model)...),
		"duplicate DTD":     append([]byte(`<!DOCTYPE svg><!DOCTYPE svg>`), wrapSVG(model)...),
		"DTD after root":    append(wrapSVG(model), []byte(`<!DOCTYPE svg>`)...),
		"wrong DTD root":    append([]byte(`<!DOCTYPE html>`), wrapSVG(model)...),
		"external entity":   []byte(`<!DOCTYPE svg SYSTEM "file:///nonexistent/svg.dtd"><svg content="&secret;"/>`),
		"entity directive":  append([]byte(`<!ENTITY x "hidden">`), wrapSVG(model)...),
		"diagram DTD":       wrapSVG(`<!DOCTYPE mxGraphModel>` + model),
		"mismatched DTD":    wrapSVG(`<!DOCTYPE svg>` + model),
		"too deep":          wrapSVG(strings.Repeat(`<x>`, 129) + strings.Repeat(`</x>`, 129)),
		"too large":         bytes.Repeat([]byte("x"), maxSVGSize+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Render(input, "input.svg"); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
