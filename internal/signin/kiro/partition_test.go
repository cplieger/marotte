package kiro

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/signin"
)

func TestEndpointsFor_EachPartitionsHosts(t *testing.T) {
	for _, tc := range []struct {
		region     string
		oidc       string
		management string
	}{
		{region: "us-east-1", oidc: "https://oidc.us-east-1.amazonaws.com", management: "https://management.us-east-1.kiro.dev"},
		{region: "eu-central-1", oidc: "https://oidc.eu-central-1.amazonaws.com", management: "https://management.eu-central-1.kiro.dev"},
		{region: "eu-west-1", oidc: "https://oidc.eu-west-1.amazonaws.com", management: "https://management.us-east-1.kiro.dev"},
		{region: "us-gov-east-1", oidc: "https://oidc.us-gov-east-1.amazonaws.com", management: "https://management.us-gov-east-1.kiro.dev"},
		{region: "us-gov-west-1", oidc: "https://oidc.us-gov-west-1.amazonaws.com", management: "https://management.us-gov-west-1.kiro.dev"},
		{region: "us-iso-east-1", oidc: "https://oidc.us-iso-east-1.c2s.ic.gov", management: "https://kiro-management.us-iso-east-1.c2s.ic.gov"},
		{region: "us-iso-west-1", oidc: "https://oidc.us-iso-west-1.c2s.ic.gov", management: "https://kiro-management.us-iso-east-1.c2s.ic.gov"},
		{region: "us-isob-east-1", oidc: "https://oidc.us-isob-east-1.sc2s.sgov.gov", management: "https://kiro-management.us-isob-east-1.sc2s.sgov.gov"},
		{region: "us-isof-east-1", oidc: "https://oidc.us-isof-east-1.csp.hci.ic.gov", management: "https://kiro-management.us-isof-east-1.csp.hci.ic.gov"},
		{region: "us-isof-west-1", oidc: "https://oidc.us-isof-west-1.csp.hci.ic.gov", management: "https://kiro-management.us-isof-south-1.csp.hci.ic.gov"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			ep, err := endpointsFor(tc.region)
			if err != nil {
				t.Fatalf("endpointsFor = %v", err)
			}
			if ep.oidc != tc.oidc {
				t.Errorf("oidc = %q, want %q", ep.oidc, tc.oidc)
			}
			if ep.management != tc.management {
				t.Errorf("management = %q, want %q", ep.management, tc.management)
			}
		})
	}
}

func TestPartitions_EveryAWSPartitionIsKnown(t *testing.T) {
	var names []string
	for _, p := range partitions {
		names = append(names, p.name)
	}
	want := []string{"aws", "aws-cn", "aws-eusc", "aws-iso", "aws-iso-b", "aws-iso-e", "aws-iso-f", "aws-us-gov"}
	if !slices.Equal(names, want) {
		t.Errorf("partitions = %v, want the SDK's %v", names, want)
	}
}

func TestPartitions_EveryServedPartitionHasItsHosts(t *testing.T) {
	for _, p := range partitions {
		if p.management == nil {
			continue
		}
		if p.dnsSuffix == "" {
			t.Errorf("the %s partition has no DNS suffix", p.name)
		}
		if p.management[p.fallback] == "" {
			t.Errorf("the %s partition falls back to %q, which has no control plane", p.name, p.fallback)
		}
	}
}

func TestEndpointsFor_RefusesARegionOfAPartitionKiroIsNotOfferedIn(t *testing.T) {
	for _, tc := range []struct{ region, partition string }{
		{region: "cn-north-1", partition: "aws-cn"},
		{region: "cn-northwest-1", partition: "aws-cn"},
		{region: "eusc-de-east-1", partition: "aws-eusc"},
		{region: "eu-isoe-west-1", partition: "aws-iso-e"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			_, err := endpointsFor(tc.region)
			if !errors.Is(err, signin.ErrInvalidParams) || !errors.Is(err, errPartitionUnserved) {
				t.Fatalf("endpointsFor = %v, want ErrInvalidParams and errPartitionUnserved", err)
			}
			want := `marotte cannot sign in to Kiro in the region "` + tc.region + `": Kiro is not offered in the AWS partition ` + tc.partition + "."
			if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != want {
				t.Errorf("error = %v, want the text %q", err, want)
			}
		})
	}
}

func TestEndpointsFor_RefusesAnUnknownRegion(t *testing.T) {
	for _, region := range []string{
		"xx-east-1", "eusc-fr-east-1", "eu-CENTRAL-1", "us-west_2-1", "us-east-1.example.com", "--region", "",
		"us-east-1\nforged", "us-" + strings.Repeat("a", maxRegionBytes) + "-1",
	} {
		t.Run(region, func(t *testing.T) {
			_, err := endpointsFor(region)
			if !errors.Is(err, signin.ErrInvalidParams) || !errors.Is(err, errUnknownRegion) {
				t.Fatalf("endpointsFor = %v, want ErrInvalidParams and errUnknownRegion", err)
			}
			fe, ok := errors.AsType[*signin.FailureError](err)
			if !ok || !strings.HasSuffix(fe.Text, ": it is not a known AWS region.") {
				t.Fatalf("error = %v, want the user text", err)
			}
			if strings.ContainsAny(fe.Text, "\r\n") || len(fe.Text) > len(textRegionUnknown)+maxRegionBytes+len("...") {
				t.Errorf("text = %q, want the region sanitized to one bounded line", fe.Text)
			}
		})
	}
}

func TestBegin_ARefusedRegionSendsNoRequest(t *testing.T) {
	for _, tc := range []struct{ region, text string }{
		{region: "xx-east-1", text: `marotte cannot sign in to Kiro in the region "xx-east-1": it is not a known AWS region.`},
		{region: "cn-north-1", text: `marotte cannot sign in to Kiro in the region "cn-north-1": Kiro is not offered in the AWS partition aws-cn.`},
	} {
		t.Run(tc.region, func(t *testing.T) {
			aws := newFakeAWS(t, "alice")
			_, err := backendFor(aws).Begin(t.Context(), signin.FlowParams{Flow: signin.FlowDevice, StartURL: "https://d-1.awsapps.com/start", Region: tc.region})
			if fe, ok := errors.AsType[*signin.FailureError](err); !ok || fe.Text != tc.text {
				t.Errorf("Begin = %v, want the refusal naming the region", err)
			}
			if n := len(aws.served()); n != 0 {
				t.Errorf("calls = %d, want none for a refused region", n)
			}
		})
	}
}

func TestDecodeRecord_RefusesARefusedRegion(t *testing.T) {
	r := testRecord(providerBuilderID, "")
	r.Region = "cn-north-1"
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRecord(signin.Credential{Data: data}); !errors.Is(err, signin.ErrInvalidParams) {
		t.Errorf("decodeRecord = %v, want the region refused", err)
	}
}

// TestBackend_TheSignInAndItsRenewalUseTheRecordsRegion: every host comes from the region the
// portal named, for the device flow, the profile and email lookups and a later renewal alike.
func TestBackend_TheSignInAndItsRenewalUseTheRecordsRegion(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	served := b.resolve
	var asked []string
	b.resolve = func(region string) (endpoints, error) {
		asked = append(asked, region)
		return served(region)
	}
	r, err := signInDirect(t, b, idcParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Refresh(t.Context(), credOf(t, r)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eu-west-1", "eu-west-1"}; !slices.Equal(asked, want) {
		t.Errorf("regions resolved = %v, want %v", asked, want)
	}
}

// TestBackend_EachRequestGoesToItsOwnHost: the device and refresh grants, which carry the client
// secret and the refresh token, reach only the SSO-OIDC host, and the profile and email lookups
// only the control plane.
func TestBackend_EachRequestGoesToItsOwnHost(t *testing.T) {
	aws := newFakeAWS(t, "alice")
	b := backendFor(aws)
	r, err := signInDirect(t, b, idcParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Refresh(t.Context(), credOf(t, r)); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/client/register": hostOIDC, "/device_authorization": hostOIDC, "/token": hostOIDC,
		"/": hostManagement, "/Get-Usage-Limits": hostManagement,
	}
	seen := map[string]bool{}
	for _, c := range aws.served() {
		if c.host != want[c.path] {
			t.Errorf("%s went to the %q host, want %q", c.path, c.host, want[c.path])
		}
		seen[c.path] = true
	}
	for path := range want {
		if !seen[path] {
			t.Errorf("no request to %s", path)
		}
	}
}
