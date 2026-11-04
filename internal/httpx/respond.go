package httpx

import (
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ErrorBody is the error envelope every service returns, matching the shape
// OpenAI clients already know how to read.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one failure.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

// WriteError sends a JSON error. Internals are never included: the message is
// what a caller is allowed to see, and the log holds the rest.
func WriteError(w http.ResponseWriter, status int, message, kind string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: ErrorDetail{Message: message, Type: kind}})
}

// WriteJSON sends a value as JSON, negotiating compression and serving a 304
// when the caller already holds this representation.
//
// Compression and validation live together because they must agree: the ETag
// is computed over the uncompressed bytes so that the same logical response
// keeps one identity across encodings.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Internal server error.", "server_error")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")

	// Only a successful, cacheable response gets a validator; an error has no
	// stable identity worth revalidating.
	if status == http.StatusOK {
		etag := ETagFor(body)
		h.Set("ETag", etag)
		if matchesETag(r, etag) {
			// A 304 carries no body, and must not carry a stale length.
			h.Del("Content-Type")
			h.Del("Content-Length")
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	encoded, encoding := Compress(r, body)
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
		h.Add("Vary", "Accept-Encoding")
	}
	h.Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// StaticJSON is a response that never changes, such as a catalogue compiled
// into the binary. WriteJSON would marshal, hash and compress it again on
// every request; StaticJSON does that once and then only copies bytes.
type StaticJSON struct {
	body    []byte
	gzipped []byte
	etag    string
}

// NewStaticJSON prepares value as a 200 response.
func NewStaticJSON(value any) (*StaticJSON, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	s := &StaticJSON{body: body, etag: ETagFor(body)}
	if len(body) >= minCompressBytes {
		if gzipped, ok := gzipBytes(body); ok && len(gzipped) < len(body) {
			s.gzipped = gzipped
		}
	}
	return s, nil
}

// ServeHTTP answers exactly as WriteJSON(w, r, http.StatusOK, value) would.
func (s *StaticJSON) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("ETag", s.etag)
	if matchesETag(r, s.etag) {
		h.Del("Content-Type")
		h.Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := s.body
	if s.gzipped != nil && accepts(r, "gzip") {
		body = s.gzipped
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

/* -------------------------------------------------------- compression -- */

// minCompressBytes is the size below which compressing loses.
//
// Measured against this service's own responses: a small JSON body shrinks by
// a handful of bytes while costing a compressor setup, and the header added to
// describe the encoding can exceed the saving outright.
const minCompressBytes = 512

var gzipWriters = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
		return w
	},
}

// Compress encodes body if the caller accepts an encoding and the body is
// large enough to benefit.
//
// gzip is the only encoding offered for dynamic responses. Brotli would win a
// few points of ratio, but it is not in the standard library, and the whole
// service is deliberately dependency-free; static assets are pre-compressed at
// build time where the much slower maximum-quality setting is affordable.
func Compress(r *http.Request, body []byte) ([]byte, string) {
	if len(body) < minCompressBytes || !accepts(r, "gzip") {
		return body, ""
	}

	compressed, ok := gzipBytes(body)
	// Refuse a "compression" that made things bigger.
	if !ok || len(compressed) >= len(body) {
		return body, ""
	}
	return compressed, "gzip"
}

func gzipBytes(body []byte) ([]byte, bool) {
	writer := gzipWriters.Get().(*gzip.Writer)
	defer gzipWriters.Put(writer)

	var buf bytes.Buffer
	writer.Reset(&buf)
	if _, err := writer.Write(body); err != nil {
		return nil, false
	}
	if err := writer.Close(); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// accepts reports whether the caller accepts an encoding, honouring q=0 as an
// explicit refusal rather than a preference.
func accepts(r *http.Request, encoding string) bool {
	header := r.Header.Get("Accept-Encoding")
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), encoding) {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if value, err := strconv.ParseFloat(q, 64); err == nil && value == 0 {
				return false
			}
		}
		return true
	}
	return false
}

/* ---------------------------------------------------------- validation -- */

// ETagFor builds a weak validator over a response body.
//
// Weak is correct here: the same logical representation may be sent as
// identity or gzip, whose bytes differ. A strong tag shared across encodings
// would be a lie, and one tag per encoding would defeat the purpose.
func ETagFor(body []byte) string {
	sum := sha1.Sum(body)
	return `W/"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`
}

func matchesETag(r *http.Request, etag string) bool {
	header := r.Header.Get("If-None-Match")
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == want {
			return true
		}
	}
	return false
}
