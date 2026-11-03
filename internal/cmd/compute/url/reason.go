package url

import (
	"strings"
	"unicode"
)

// HumanBlock renders what the server says is holding something up, in words a
// developer can act on.
//
// The platform reports a blocked state as a camelCase reason plus a human
// message. Product principle 4 forbids showing the reason: "NoMatchingInterfaces"
// is internal state, and a developer who has never read the API types cannot do
// anything with it. The message is what was written for them, so it wins
// whenever there is one.
//
// When a condition carries no message there is still something worth saying, so
// the reason is spaced out and lowercased rather than dropped: "no matching
// interfaces" tells a developer more than silence and still never shows them an
// identifier. This is formatting, not interpretation — nothing here branches on
// which reason it was given, per the house rule in util/conditions.go.
func HumanBlock(reason, message string) string {
	if message != "" {
		return message
	}
	return humanizeReason(reason)
}

// humanizeReason turns a camelCase condition reason into a lowercase phrase.
// Runs of capitals are kept together so "CertificateCARequired" reads as
// "certificate CA required" rather than "certificate c a required".
func humanizeReason(reason string) string {
	if reason == "" {
		return statusPending
	}

	runes := []rune(reason)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			// A capital after a lowercase always starts a word; a capital that
			// ends a run of capitals starts one only if a lowercase follows it.
			startsWord := !unicode.IsUpper(prev) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1]))
			if startsWord {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(r)
	}

	words := strings.Fields(b.String())
	for i, w := range words {
		// Leave acronyms as the server wrote them; lowercase ordinary words.
		if !isAcronym(w) {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

func isAcronym(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, r := range w {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
