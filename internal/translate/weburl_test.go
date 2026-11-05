package translate

import "testing"

func TestIsWebURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://auth.example.com/oauth?code=1", true},
		{"http://localhost:8080/callback", true},
		{"HTTPS://Example.com/x", true},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"ftp://example.com", false},
		{"vscode://localhost/open", false},
		{"", false},
		{"not a url", false},
		{"//example.com", false},
		{"https:foo", false},
		{"https:///x", false},
		{"https://exa\x00mple.com", false},
	}
	for _, tt := range tests {
		if got := IsWebURL(tt.url); got != tt.want {
			t.Errorf("IsWebURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
