package sync

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var svgAttribute = regexp.MustCompile(`\s+([a-zA-Z_:][a-zA-Z0-9_:.-]*)\s*=\s*("[^"]*"|'[^']*')`)

// stripDrawioMetadata removes the embedded editable diagram from the root SVG
// element, while leaving the rendered SVG and ordinary content attributes intact.
func stripDrawioMetadata(data []byte) []byte {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if err != nil {
			return data
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if element.Name.Local != "svg" || element.Name.Space != "http://www.w3.org/2000/svg" {
			return data
		}
		isDrawio := false
		for _, attr := range element.Attr {
			value := strings.TrimSpace(attr.Value)
			if attr.Name.Space == "" && attr.Name.Local == "content" && (strings.HasPrefix(value, "<mxfile>") || strings.HasPrefix(value, "<mxfile ") || strings.HasPrefix(value, "<mxfile\n") || strings.HasPrefix(value, "<mxfile\t") || strings.HasPrefix(value, "<mxfile\r") || strings.HasPrefix(value, "<mxfile/>")) {
				isDrawio = true
			}
		}
		if !isDrawio {
			return data
		}
		end := decoder.InputOffset()
		tag := data[start:end]
		for _, match := range svgAttribute.FindAllSubmatchIndex(tag, -1) {
			if string(tag[match[2]:match[3]]) == "content" {
				result := make([]byte, 0, len(data)-(match[1]-match[0]))
				result = append(result, data[:int(start)+match[0]]...)
				return append(result, data[int(start)+match[1]:]...)
			}
		}
		return data
	}
}

func uploadImage(ctx context.Context, uploader MultipartClient, image Image, filename string) (string, error) {
	if image.ContentType != "image/svg+xml" {
		return uploader.Upload(ctx, image.Name, image.ContentType, filename)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if hash(string(data)) != image.Hash {
		return "", fmt.Errorf("image changed during sync; run diff again before retrying")
	}
	cleaned := stripDrawioMetadata(data)
	if len(cleaned) == len(data) {
		return uploader.Upload(ctx, image.Name, image.ContentType, filename)
	}
	dir, err := os.MkdirTemp("", "notion-sync-svg-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	prepared := filepath.Join(dir, image.Name)
	if err := os.WriteFile(prepared, cleaned, 0600); err != nil {
		return "", err
	}
	return uploader.Upload(ctx, image.Name, image.ContentType, prepared)
}
