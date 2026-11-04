package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// StaticJSON must be indistinguishable from WriteJSON on the wire: same
// body, encoding, validator and revalidation.
func TestStaticJSONAnswersAsWriteJSONDoes(t *testing.T) {
	values := map[string]any{
		"small": map[string]string{"status": "ready"},
		"large": map[string]any{"object": "list", "data": strings.Repeat("model ", 400)},
	}
	for name, value := range values {
		static, err := httpx.NewStaticJSON(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		etag := ""
		for _, header := range []map[string]string{
			{},
			{"Accept-Encoding": "gzip, br"},
			{"Accept-Encoding": "gzip;q=0"},
			{"If-None-Match": "<etag>"},
		} {
			request := func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				for k, v := range header {
					if v == "<etag>" {
						v = etag
					}
					r.Header.Set(k, v)
				}
				return r
			}
			want := httptest.NewRecorder()
			httpx.WriteJSON(want, request(), http.StatusOK, value)
			got := httptest.NewRecorder()
			static.ServeHTTP(got, request())

			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("%s %v: got %d %q, want %d %q", name, header, got.Code, got.Body.String(), want.Code, want.Body.String())
			}
			for _, h := range []string{"Content-Type", "Content-Encoding", "Content-Length", "ETag", "Vary"} {
				if g, w := got.Header().Values(h), want.Header().Values(h); strings.Join(g, ",") != strings.Join(w, ",") {
					t.Errorf("%s %v: %s = %q, want %q", name, header, h, g, w)
				}
			}
			etag = want.Header().Get("ETag")
		}
	}
}

func TestNewStaticJSONRejectsWhatCannotBeEncoded(t *testing.T) {
	if _, err := httpx.NewStaticJSON(func() {}); err == nil {
		t.Fatal("want an error for a value JSON cannot encode")
	}
}
