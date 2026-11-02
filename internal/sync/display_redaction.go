package sync

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// Redact URLs in reference definitions and HTML attributes as well as ordinary
// URLs. Display normalization must never affect the stored sync baseline.
var displayReferenceURL = regexp.MustCompile(`(?m)^[ \t>]*\[[^\]\n]+\]:[ \t]*(?:\r?\n[ \t>]*)?(<[^>\n]+>|[^ \t\r\n]+)`)
var displayHTMLSource = regexp.MustCompile(`(?i)\b(?:src|srcset)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
var displayAbsoluteURL = regexp.MustCompile("(?i)(?:https?://|//)[^\\s<>\"'`]+")
var displaySourceToken = regexp.MustCompile(`[^\s,]+`)

func redactDisplayURL(raw string) string {
	decoded := html.UnescapeString(raw)
	u, err := url.Parse(decoded)
	if err != nil {
		return "redacted-invalid-url"
	}
	normalizeImageURL(u, true, 0)
	result := u.String()
	if decoded != raw {
		result = strings.ReplaceAll(result, "&", "&amp;")
	}
	return result
}

func redactDisplayURLs(body string) string {
	body = displayReferenceURL.ReplaceAllStringFunc(body, func(reference string) string {
		loc := displayReferenceURL.FindStringSubmatchIndex(reference)
		value := reference[loc[2]:loc[3]]
		if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
			value = "<" + redactDisplayURL(value[1:len(value)-1]) + ">"
		} else {
			value = redactDisplayURL(value)
		}
		return reference[:loc[2]] + value + reference[loc[3]:]
	})
	body = displayHTMLSource.ReplaceAllStringFunc(body, func(attribute string) string {
		i := strings.IndexByte(attribute, '=') + 1
		prefix, value := attribute[:i], attribute[i:]
		value = displaySourceToken.ReplaceAllStringFunc(value, func(token string) string {
			left := len(token) - len(strings.TrimLeft(token, " \t\r\n\"'"))
			right := len(strings.TrimRight(token, " \t\r\n\"'"))
			if left >= right {
				return token
			}
			return token[:left] + redactDisplayURL(token[left:right]) + token[right:]
		})
		return prefix + value
	})
	return displayAbsoluteURL.ReplaceAllStringFunc(body, func(value string) string {
		trimmed := strings.TrimRight(value, ")]},.;")
		return redactDisplayURL(trimmed) + value[len(trimmed):]
	})
}
