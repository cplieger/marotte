package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "n0tify-T0ken-for-tests_0123456789abcdef" // gitleaks:allow

func mustGate(t *testing.T, token string) Gate {
	t.Helper()
	g, err := ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func authorize(t *testing.T, g Gate, header string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/notify", http.NoBody)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	w := httptest.NewRecorder()
	return w, g.Authorize(w, r)
}

func TestGate_Authorize(t *testing.T) {
	on := mustGate(t, testToken)
	t.Run("off answers 401 naming the variable", func(t *testing.T) {
		w, ok := authorize(t, Gate{}, "Bearer "+testToken)
		if ok || w.Code != http.StatusUnauthorized {
			t.Fatalf("ok=%v code=%d, want refused 401", ok, w.Code)
		}
		if !strings.Contains(w.Body.String(), TokenEnv) {
			t.Errorf("body %q does not name %s", w.Body.String(), TokenEnv)
		}
		if strings.Contains(w.Body.String(), testToken) {
			t.Errorf("body echoes the presented token")
		}
	})
	for _, tc := range []struct{ name, header string }{
		{"missing header", ""},
		{"wrong token", "Bearer wrong-" + testToken},
		{"token prefix", "Bearer " + testToken[:10]},
		{"basic scheme", "Basic " + testToken},
		{"no scheme", testToken},
		{"empty bearer", "Bearer "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ok := authorize(t, on, tc.header)
			if ok || w.Code != http.StatusUnauthorized {
				t.Fatalf("ok=%v code=%d, want refused 401", ok, w.Code)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
			if strings.Contains(w.Body.String(), testToken[:10]) {
				t.Errorf("body echoes the presented token: %q", w.Body.String())
			}
		})
	}
	t.Run("right token admits", func(t *testing.T) {
		for _, h := range []string{"Bearer " + testToken, "bearer " + testToken} {
			w, ok := authorize(t, on, h)
			if !ok {
				t.Fatalf("%q refused: %d %s", h[:6], w.Code, w.Body.String())
			}
			if w.Body.Len() != 0 || w.Code != http.StatusOK {
				t.Errorf("an admitted request had a reply written: %d %q", w.Code, w.Body.String())
			}
		}
	})
}

func TestGate_Carries(t *testing.T) {
	on := mustGate(t, testToken)
	for _, tc := range []struct {
		name   string
		gate   Gate
		header string
		want   bool
	}{
		{"valid token", on, "Bearer " + testToken, true},
		{"wrong token", on, "Bearer nope", false},
		{"no header", on, "", false},
		{"gate off", Gate{}, "Bearer " + testToken, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/settings", http.NoBody)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := tc.gate.Carries(r); got != tc.want {
				t.Errorf("Carries = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseToken(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{"hex secret", strings.Repeat("ab", 32), true},
		{"token68 charset", "A-._~+/" + strings.Repeat("z", 30) + "==", true},
		{"too short", "short-token", false},
		{"edge space", " " + testToken, false},
		{"interior space", "abc def" + testToken, false},
		{"control byte", testToken + "\x01", false},
		{"non ascii", testToken + "é", false},
		{"too long", strings.Repeat("a", maxTokenBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseToken(tc.value)
			if (err == nil) != tc.ok {
				t.Fatalf("ParseToken err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil {
				if !strings.Contains(err.Error(), TokenEnv) {
					t.Errorf("error %q does not name %s", err, TokenEnv)
				}
				if len(tc.value) > 4 && strings.Contains(err.Error(), tc.value) {
					t.Errorf("error echoes the value")
				}
			}
		})
	}
}

func TestIsRoute(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/sessions/abc-123/notify":   true,
		"/api/sessions//notify":          false,
		"/api/sessions/a/b/notify":       false,
		"/api/sessions/abc/notify/":      false,
		"/api/sessions/abc/notifyx":      false,
		"/api/settings":                  false,
		"/api/sessions/abc/notify/extra": false,
	} {
		if got := IsRoute(path); got != want {
			t.Errorf("IsRoute(%q) = %v, want %v", path, got, want)
		}
	}
}
