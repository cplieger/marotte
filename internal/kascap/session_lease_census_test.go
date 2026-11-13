package kascap

import (
	"regexp"
	"strconv"
	"testing"
)

// sessionLeasesPath is the composition file declaring what marotte's boot sweep of KAS's session leases reads.
const sessionLeasesPath = "../composition/session_leases.go"

// leaseStampWindow bounds how far before parseStamp its size cap's assignment may sit, so a reused minified
// name in another module is never read.
const leaseStampWindow = 64 << 10

var (
	leaseDirRe        = regexp.MustCompile(`\.dirname\(this\.sessionsPath\),"([^"]+)"\)`)
	leaseClientNameRe = regexp.MustCompile(`leaseClientName\(\)\{return [\w$]+\(this\.clientInfo\?\.title\)\?\?`)
	leaseSchemaRe     = regexp.MustCompile(`\{v:[\w$]+\.literal\(1\),pid:[\w$]+,parentPid:[\w$]+,instanceId:[\w$]+,` +
		`hostId:[^,{}]+,acquiredAt:[^,{}]+,token:[\w$]+,clientName:`)
	leaseParseStampRe = regexp.MustCompile(`function [\w$]+\(\w+\)\{if\(!\(\w+\.length>([\w$]+)\)\)try\{let \w+=[\w$]+\.safeParse\(JSON\.parse\(\w+\)\)`)
)

// leaseStampCap answers the size cap KAS's parseStamp applies, read from the nearest numeric assignment of the
// variable it compares against; ok is false when either is missing.
func leaseStampCap(src string) (string, bool) {
	at := leaseParseStampRe.FindAllStringSubmatchIndex(src, -1)
	if len(at) != 1 {
		return "", false
	}
	name := src[at[0][2]:at[0][3]]
	lo := max(0, at[0][0]-leaseStampWindow)
	assign := regexp.MustCompile(`[,;\s]` + regexp.QuoteMeta(name) + `=(\d+)[,;]`).FindAllStringSubmatch(src[lo:at[0][0]], -1)
	if len(assign) == 0 {
		return "", false
	}
	return assign[len(assign)-1][1], true
}

// TestSessionLeaseStampMatchesTheBundle pins the bundle facts marotte's boot sweep of session leases rests on: where
// KAS keeps them, that their clientName is the clientInfo.title marotte sends, the v:1 stamp shape, and the size
// past which KAS reads a file as no stamp. A release that moves any of them would silently disable the sweep.
func TestSessionLeaseStampMatchesTheBundle(t *testing.T) {
	dir := declaredConst(t, sessionLeasesPath, "sessionLeasesDir")
	stampMax := declaredConst(t, sessionLeasesPath, "leaseStampMax")
	forEachBundle(t, func(t *testing.T, b bundle) {
		got := leaseDirRe.FindAllStringSubmatch(b.src, -1)
		if len(got) != 1 {
			t.Fatalf("session leases directory: found %d joins beside sessionsPath, want 1", len(got))
		}
		if got[0][1] != dir {
			t.Errorf("bundle keeps session leases in %q, session_leases.go sessionLeasesDir = %q", got[0][1], dir)
		}
		if n := len(leaseClientNameRe.FindAllString(b.src, -1)); n != 1 {
			t.Errorf("leaseClientName: found %d definitions taking clientInfo.title first, want 1", n)
		}
		if n := len(leaseSchemaRe.FindAllString(b.src, -1)); n != 1 {
			t.Errorf("lease stamp schema: found %d v:1 objects with the stamp's members in order, want 1", n)
		}
		capVal, ok := leaseStampCap(b.src)
		if !ok {
			t.Fatal("parseStamp: found no size cap compared before the schema parse")
		}
		if capVal != stampMax {
			t.Errorf("bundle parseStamp cap = %s bytes, session_leases.go leaseStampMax = %s", capVal, stampMax)
		}
	})
}

func TestSessionLeaseStampExtractors(t *testing.T) {
	const src = `this.sessionLeases=new Sen({dir:op.join(op.dirname(this.sessionsPath),"session-leases"),fs:{}});` +
		`leaseClientName(){return $I(this.clientInfo?.title)??$I(this.clientInfo?.name)}` +
		`var ffd=1,q=2;var ffd=4096,hfd=100,bfd=x.object({v:x.literal(1),pid:yfd,parentPid:wfd,instanceId:M3s,` +
		`hostId:x.string().min(1).max(255),acquiredAt:x.string().min(1).max(40),token:M3s,clientName:x.string().optional()});` +
		`function qwe(t){if(!(t.length>ffd))try{let e=bfd.safeParse(JSON.parse(t));return e.success?e.data:void 0}catch{return}}`
	if m := leaseDirRe.FindStringSubmatch(src); m == nil || m[1] != "session-leases" {
		t.Errorf("leaseDirRe = %v, want the planted session-leases", m)
	}
	if n := len(leaseClientNameRe.FindAllString(src, -1)); n != 1 {
		t.Errorf("leaseClientNameRe matched %d times, want 1", n)
	}
	if n := len(leaseSchemaRe.FindAllString(src, -1)); n != 1 {
		t.Errorf("leaseSchemaRe matched %d times, want 1", n)
	}
	if got, ok := leaseStampCap(src); !ok || got != strconv.Itoa(4096) {
		t.Errorf("leaseStampCap = %q, %v; want the nearest assignment 4096", got, ok)
	}
}
