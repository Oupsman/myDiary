// Package syncproto holds what the sync client and server share.
//
// API (all requests need "Authorization: Bearer <device token>"):
//
//	GET    /v1/ping           check the token
//	GET    /v1/diaries        list diaries ([]DiaryInfo)
//	GET    /v1/diaries/{id}   download; ETag is the version. If-None-Match → 304
//	PUT    /v1/diaries/{id}   upload; needs If-Match: "<version>" or If-None-Match: *
//	                          returns DiaryInfo, or 412 if the version moved on
//	DELETE /v1/diaries/{id}   needs If-Match
//
// Bodies are diary files exactly as stored on disk: already encrypted by the
// client. The server never sees a key.
package syncproto

import (
	"strconv"
	"strings"
	"time"
)

// DiaryInfo describes a stored diary.
type DiaryInfo struct {
	ID      string    `json:"id"`
	Version int64     `json:"version"`
	Size    int64     `json:"size"`
	Updated time.Time `json:"updated"`
}

// ShortID formats the start of a diary ID for display, e.g. "3f9a 1c07 b2e4".
func ShortID(id string) string {
	var b strings.Builder
	for i := 0; i < len(id) && i < 12; i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(id[i:min(i+4, len(id))])
	}
	return b.String()
}

// ValidID reports whether id is an acceptable diary ID (lowercase hex).
func ValidID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ETag formats a version as an entity tag.
func ETag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

// ParseETag parses an entity tag produced by ETag.
func ParseETag(s string) (int64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "W/")
	if len(s) < 3 || s[0] != '"' || s[len(s)-1] != '"' {
		return 0, false
	}
	v, err := strconv.ParseInt(s[1:len(s)-1], 10, 64)
	return v, err == nil && v > 0
}
