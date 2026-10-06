package auth

import "testing"

// FuzzBuildLoginArgs pins that buildLoginArgs starts with "login" and "--use-device-flow" and that newline or NUL in
// provider or region cannot inject arguments.
func FuzzBuildLoginArgs(f *testing.F) {
	f.Add("", "")
	f.Add("https://x.com", "us-east-1")
	f.Add("--evil", "")
	f.Add("", "\ninjected")
	f.Add("https://provider.example.com/auth", "eu-west-1")
	f.Add("a\x00b", "c\x00d")

	f.Fuzz(func(t *testing.T, provider, region string) {
		args := buildLoginArgs(provider, region)

		if len(args) < 2 || args[0] != "login" || args[1] != flagDeviceFlow {
			t.Fatalf("buildLoginArgs(%q, %q) = %v; missing required prefix", provider, region, args)
		}

		// Provider appears only after --identity-provider.
		if provider != "" {
			found := false
			for i, a := range args {
				if a == "--identity-provider" && i+1 < len(args) && args[i+1] == provider {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("buildLoginArgs(%q, %q): provider not after --identity-provider", provider, region)
			}
		}

		// Region appears only after --region.
		if region != "" {
			found := false
			for i, a := range args {
				if a == "--region" && i+1 < len(args) && args[i+1] == region {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("buildLoginArgs(%q, %q): region not after --region", provider, region)
			}
		}

		// Length is determined by the inputs.
		expected := 2
		if provider != "" {
			expected += 2
		}
		if region != "" {
			expected += 2
		}
		if len(args) != expected {
			t.Fatalf("buildLoginArgs(%q, %q): len=%d, want %d", provider, region, len(args), expected)
		}
	})
}
