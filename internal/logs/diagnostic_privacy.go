package logs

import (
	"regexp"
	"strings"
)

var diagnosticSensitiveText = regexp.MustCompile(`(?i)(authorization|bearer\s|cookie|api[\s_-]?key|access[\s_-]?token|refresh[\s_-]?token|password|auth[\s_-]?blob|<html|eyJ[A-Za-z0-9_-]{10,}|sk-[A-Za-z0-9_-]{8,})`)

// Provider errors can echo request JSON or HTML. Keep classification/phase in
// their dedicated fields; do not persist that upstream body as an error string.
// Existing historical records are not rewritten.
func safeDiagnosticMessage(value string) string {
	if len(value) > 1000 || strings.ContainsAny(strings.ReplaceAll(value, "[DONE]", ""), "{}[]") || diagnosticSensitiveText.MatchString(value) {
		return "Sensitive upstream details omitted"
	}
	return redactSecrets(value)
}
