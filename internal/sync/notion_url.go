package sync

import (
	"net/url"
	"strings"
)

// canonicalNotionS3URL unifies only path-style and virtual-hosted URLs for
// Notion's known buckets in the same S3 region. It preserves object escaping.
func canonicalNotionS3URL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	base, ok := strings.CutSuffix(host, ".amazonaws.com")
	if !ok {
		return false
	}
	var bucket, endpoint string
	for _, name := range []string{"secure.notion-static.com", "prod-files-secure"} {
		if rest, found := strings.CutPrefix(base, name+"."); found {
			bucket, endpoint = name, rest
			break
		}
	}
	objectPath, escapedObject := u.Path, u.EscapedPath()
	if bucket == "" {
		endpoint = base
		for _, name := range []string{"secure.notion-static.com", "prod-files-secure"} {
			prefix := "/" + name + "/"
			if strings.HasPrefix(u.Path, prefix) && strings.HasPrefix(u.EscapedPath(), prefix) {
				bucket = name
				objectPath = strings.TrimPrefix(u.Path, "/"+name)
				escapedObject = strings.TrimPrefix(u.EscapedPath(), "/"+name)
				break
			}
		}
	}
	if bucket == "" || !strings.HasPrefix(objectPath, "/") {
		return false
	}
	region := ""
	switch {
	case endpoint == "s3":
	case strings.HasPrefix(endpoint, "s3."):
		region = strings.TrimPrefix(endpoint, "s3.")
	case strings.HasPrefix(endpoint, "s3-"):
		region = strings.TrimPrefix(endpoint, "s3-")
	default:
		return false
	}
	for _, r := range region {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	if endpoint != "s3" && region == "" {
		return false
	}
	u.Host = "s3.amazonaws.com"
	if region != "" {
		u.Host = "s3." + region + ".amazonaws.com"
	}
	u.Path = "/" + bucket + objectPath
	u.RawPath = "/" + bucket + escapedObject
	return true
}

func notionImageProxySource(u *url.URL) (string, bool, error) {
	if u.Scheme != "https" || u.User != nil {
		return "", false, nil
	}
	switch strings.ToLower(u.Hostname()) {
	case "notion.so", "www.notion.so", "notion.com", "www.notion.com":
		if encoded, ok := strings.CutPrefix(u.EscapedPath(), "/image/"); ok {
			source, err := url.PathUnescape(encoded)
			return source, true, err
		}
	}
	return "", false, nil
}
