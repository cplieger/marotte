package kiro

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/runesafe/v3"
)

// User text for a refused region; the first %q is the sanitized region.
const (
	textRegionUnknown     = "marotte cannot sign in to Kiro in the region %q: it is not a known AWS region."
	textPartitionUnserved = "marotte cannot sign in to Kiro in the region %q: Kiro is not offered in the AWS partition %s."
)

var (
	// errUnknownRegion is a region no AWS partition holds.
	errUnknownRegion = errors.New("kiro: the region is in no known AWS partition")
	// errPartitionUnserved is a region of a known AWS partition with no Kiro control plane.
	errPartitionUnserved = errors.New("kiro: the region's AWS partition has no Kiro control plane")
)

// endpoints are the only hosts a sign-in in one region contacts.
type endpoints struct {
	oidc       string
	management string
}

// partition is an AWS partition. management maps a region to its Kiro control plane; every other
// region of the partition is served by fallback's. A partition with no management has no Kiro
// control plane, so its regions are known but refused.
type partition struct {
	regions    *regexp.Regexp
	management map[string]string
	name       string
	dnsSuffix  string
	fallback   string
}

// partitions drives the region check, the SSO-OIDC host and the control-plane host. name, regions
// and dnsSuffix are the AWS SDK default partitions (the smithy partitions.json embedded in
// kiro-cli-chat 2.31.1; which table auth/partition.rs reads could not be established), fallback is
// each one's implicitGlobalRegion, and management is kiro-cli 2.31.1's api_client/endpoints.rs.
var partitions = []partition{
	{
		name:      "aws",
		regions:   regexp.MustCompile(`^(us|eu|ap|sa|ca|me|af|il|mx)\-\w+\-\d+$`),
		dnsSuffix: "amazonaws.com",
		fallback:  "us-east-1",
		management: map[string]string{
			"us-east-1":    "https://management.us-east-1.kiro.dev",
			"eu-central-1": "https://management.eu-central-1.kiro.dev",
		},
	},
	{name: "aws-cn", regions: regexp.MustCompile(`^cn\-\w+\-\d+$`)},
	{name: "aws-eusc", regions: regexp.MustCompile(`^eusc\-(de)\-\w+\-\d+$`)},
	{
		name:       "aws-iso",
		regions:    regexp.MustCompile(`^us\-iso\-\w+\-\d+$`),
		dnsSuffix:  "c2s.ic.gov",
		fallback:   "us-iso-east-1",
		management: map[string]string{"us-iso-east-1": "https://kiro-management.us-iso-east-1.c2s.ic.gov"},
	},
	{
		name:       "aws-iso-b",
		regions:    regexp.MustCompile(`^us\-isob\-\w+\-\d+$`),
		dnsSuffix:  "sc2s.sgov.gov",
		fallback:   "us-isob-east-1",
		management: map[string]string{"us-isob-east-1": "https://kiro-management.us-isob-east-1.sc2s.sgov.gov"},
	},
	{name: "aws-iso-e", regions: regexp.MustCompile(`^eu\-isoe\-\w+\-\d+$`)},
	{
		name:      "aws-iso-f",
		regions:   regexp.MustCompile(`^us\-isof\-\w+\-\d+$`),
		dnsSuffix: "csp.hci.ic.gov",
		fallback:  "us-isof-south-1",
		management: map[string]string{
			"us-isof-south-1": "https://kiro-management.us-isof-south-1.csp.hci.ic.gov",
			"us-isof-east-1":  "https://kiro-management.us-isof-east-1.csp.hci.ic.gov",
		},
	},
	{
		name:      "aws-us-gov",
		regions:   regexp.MustCompile(`^us\-gov\-\w+\-\d+$`),
		dnsSuffix: "amazonaws.com",
		fallback:  "us-gov-west-1",
		management: map[string]string{
			"us-gov-east-1": "https://management.us-gov-east-1.kiro.dev",
			"us-gov-west-1": "https://management.us-gov-west-1.kiro.dev",
		},
	},
}

// endpointsFor returns region's hosts. A refusal wraps signin.ErrInvalidParams and either
// errUnknownRegion or errPartitionUnserved, and its *signin.FailureError text names the region.
func endpointsFor(region string) (endpoints, error) {
	if p := partitionOf(region); p != nil {
		if p.management == nil {
			return endpoints{}, regionRefusal(fmt.Sprintf(textPartitionUnserved, safeRegion(region), p.name), errPartitionUnserved)
		}
		mgmt, ok := p.management[region]
		if !ok {
			mgmt = p.management[p.fallback]
		}
		return endpoints{oidc: "https://oidc." + region + "." + p.dnsSuffix, management: mgmt}, nil
	}
	return endpoints{}, regionRefusal(fmt.Sprintf(textRegionUnknown, safeRegion(region)), errUnknownRegion)
}

// partitionOf is the partition holding region, or nil. The SDK patterns use \w, which also takes
// upper case and '_'; no AWS region id has either, so they are refused first.
func partitionOf(region string) *partition {
	if len(region) > maxRegionBytes || strings.ContainsFunc(region, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	}) {
		return nil
	}
	for i := range partitions {
		if partitions[i].regions.MatchString(region) {
			return &partitions[i]
		}
	}
	return nil
}

func safeRegion(region string) string {
	return runesafe.SanitizeSingleLineBounded(region, maxRegionBytes)
}

func regionRefusal(text string, cause error) error {
	return &signin.FailureError{Text: text, Err: fmt.Errorf("%w: %w", signin.ErrInvalidParams, cause)}
}

func validRegion(region string) error {
	_, err := endpointsFor(region)
	return err
}
