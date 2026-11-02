package apicaller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIs(t *testing.T) {
	cases := []struct {
		headers map[string]string
		name    string
		want    bool
	}{
		{name: "curl sends neither header", want: true},
		{name: "an https page sends fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}},
		{name: "a plain-HTTP page sends Origin on a POST", headers: map[string]string{"Origin": "http://nas.lan:9847"}},
		{
			name:    "a page sending both",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://marotte.example"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/command", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := Is(r); got != tc.want {
				t.Errorf("Is() = %v, want %v", got, tc.want)
			}
		})
	}
}
