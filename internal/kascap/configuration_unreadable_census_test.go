package kascap

import (
	"regexp"
	"testing"
)

var (
	// configurationUnreadableClassRe is the declaration of KAS's ConfigurationUnreadableError, capturing its binding.
	configurationUnreadableClassRe = regexp.MustCompile(`(` + jsIdent + `)=class extends ` + jsIdent +
		`\{static\{` + jsIdent + `\(this,"ConfigurationUnreadableError"\)\}name="ConfigurationUnreadableError";errorCategory="error";data;` +
		`constructor\(\)\{super\(-32e3,"The configuration cannot be read now"\),this\.data=\{errorType:this\.name\}\}\}`)
	// configurationUnreadableThrowRe is the one site that throws it, capturing the thrown binding.
	configurationUnreadableThrowRe = regexp.MustCompile(`async untilUserSettingsReadable\(\)\{if\(await this\.whenUserSettingsStated\("clientCall"\)===` +
		`"unnamed"\)throw new (` + jsIdent + `)\}`)
)

// TestConfigurationUnreadableIsAnUnnamedUser pins the premise of the getState retry (internal/agent
// configurationGetRaw): KAS's one ConfigurationUnreadableError throw is the unnamed-user check, so a retry never
// repeats a refusal of another cause.
func TestConfigurationUnreadableIsAnUnnamedUser(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		classes := configurationUnreadableClassRe.FindAllStringSubmatch(b.src, -1)
		throws := configurationUnreadableThrowRe.FindAllStringSubmatch(b.src, -1)
		if len(classes) != 1 || len(throws) != 1 || classes[0][1] != throws[0][1] {
			t.Fatalf("found %d ConfigurationUnreadableError declarations and %d untilUserSettingsReadable throws of it, want 1 "+
				"each naming one binding; re-read when KAS answers getState unreadable before keeping internal/agent's retry",
				len(classes), len(throws))
		}
		constructed := regexp.MustCompile(`new ` + regexp.QuoteMeta(classes[0][1]) + `(?:[^\w$]|$)`)
		if n := len(constructed.FindAllStringIndex(b.src, -1)); n != 1 {
			t.Errorf("ConfigurationUnreadableError is constructed at %d sites, want the one untilUserSettingsReadable throw; "+
				"re-read each before keeping internal/agent's retry", n)
		}
	})
}
