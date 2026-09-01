// Package role defines Relay's canonical managed-program role identities and
// the narrow compatibility rules that let durable records written before the
// tech-lead rename still load. It is the single source of truth: every decode
// chokepoint normalizes through NormalizeIdentity so no other package has to
// know what the retired identity was.
package role

import "regexp"

const (
	// TL is the canonical machine identity of the managed-program tech lead.
	TL = "tl"
	// AutomatedTLPrefix prefixes the identity a bounded automated tech-lead
	// turn records, as in "tl-automated:3f2504e0".
	AutomatedTLPrefix = "tl-automated:"
	// LegacyCTO is the retired identity the tech lead was recorded under
	// before the rename. It is accepted on read and never written.
	LegacyCTO = "cto"
	// LegacyAutomatedCTOPrefix is the retired automated-turn prefix. It is
	// accepted on read and never written.
	LegacyAutomatedCTOPrefix = "cto-automated:"
)

// automatedSession bounds the session prefix an automated identity may carry.
// Both the canonical and legacy forms share it, so a legacy value only
// normalizes when it would have been a valid automated identity.
const automatedSession = `[a-z0-9]{1,32}`

var (
	canonicalAutomated = regexp.MustCompile(`^` + AutomatedTLPrefix + automatedSession + `$`)
	legacyAutomated    = regexp.MustCompile(`^` + LegacyAutomatedCTOPrefix + automatedSession + `$`)
)

// NormalizeIdentity maps a retired identity onto its canonical equivalent.
// Only an exact legacy identity is rewritten: any other value—a username, an
// unrelated actor, or free text that merely mentions the retired role—is
// returned unchanged, so normalization can never edit content.
func NormalizeIdentity(value string) string {
	switch {
	case value == LegacyCTO:
		return TL
	case legacyAutomated.MatchString(value):
		return AutomatedTLPrefix + value[len(LegacyAutomatedCTOPrefix):]
	default:
		return value
	}
}

// IsAutomated reports whether value names a bounded automated tech-lead turn
// in either the canonical or the retired form.
func IsAutomated(value string) bool {
	return canonicalAutomated.MatchString(value) || legacyAutomated.MatchString(value)
}

// IsCanonical reports whether value is free of retired identities. It is the
// guard new durable writes use so a legacy identity can never be persisted.
func IsCanonical(value string) bool {
	return value != LegacyCTO && !legacyAutomated.MatchString(value)
}
