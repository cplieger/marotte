package agent

import "testing"

func TestWithGoogleOfflineAccess(t *testing.T) {
	const redirect = "redirect_uri=http%3A%2F%2F127.0.0.1%3A41234%2Fcallback"
	cases := map[string]struct{ in, want string }{
		"the endpoint gains offline access and keeps every other segment in order": {
			in:   "https://accounts.google.com/o/oauth2/v2/auth?client_id=c&" + redirect + "&state=s%2B1&code_challenge=x_y",
			want: "https://accounts.google.com/o/oauth2/v2/auth?client_id=c&" + redirect + "&state=s%2B1&code_challenge=x_y&access_type=offline&prompt=consent",
		},
		"an existing access type and prompt are replaced": {
			in:   "https://accounts.google.com/o/oauth2/v2/auth?access_type=online&state=s&prompt=select_account",
			want: "https://accounts.google.com/o/oauth2/v2/auth?state=s&access_type=offline&prompt=consent",
		},
		"an encoded key is recognised": {
			in:   "https://accounts.google.com/o/oauth2/v2/auth?access%5Ftype=online&state=s",
			want: "https://accounts.google.com/o/oauth2/v2/auth?state=s&access_type=offline&prompt=consent",
		},
		"a URL with no query gains both": {
			in:   "https://accounts.google.com/o/oauth2/v2/auth",
			want: "https://accounts.google.com/o/oauth2/v2/auth?access_type=offline&prompt=consent",
		},
	}
	for name, unchanged := range map[string]string{
		"a lookalike host":      "https://accounts.google.com.evil.example/o/oauth2/v2/auth?state=s",
		"an explicit port":      "https://accounts.google.com:443/o/oauth2/v2/auth?state=s",
		"userinfo":              "https://user@accounts.google.com/o/oauth2/v2/auth?state=s",
		"plain http":            "http://accounts.google.com/o/oauth2/v2/auth?state=s",
		"the v1 endpoint":       "https://accounts.google.com/o/oauth2/auth?state=s",
		"another provider":      "https://linear.app/oauth/authorize?state=s",
		"an unparsable address": "https://accounts.google.com/o/oauth2/v2/auth?state=s\x7f",
	} {
		cases["unchanged for "+name] = struct{ in, want string }{unchanged, unchanged}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := withGoogleOfflineAccess(tc.in); got != tc.want {
				t.Errorf("withGoogleOfflineAccess(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
