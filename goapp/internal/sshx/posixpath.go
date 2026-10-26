package sshx

import "strings"

// NormalizePOSIXPath resolves "." and ".." without touching the filesystem.
func NormalizePOSIXPath(p string) string {
	var parts []string
	absolute := strings.HasPrefix(p, "/")
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	joined := strings.Join(parts, "/")
	if absolute {
		if joined == "" {
			return "/"
		}
		return "/" + joined
	}
	if joined == "" {
		return "."
	}
	return joined
}
