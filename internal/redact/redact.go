// Package redact removes credential-shaped substrings from tool output before
// it is sent back to Hermes: seven regexps (case-insensitive on the first
// three), applied in order — the output of each feeds the next. Guillemets in
// the replacements are U+00AB / U+00BB.
package redact

import "regexp"

var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// 1  bearer <token>
	{regexp.MustCompile(`(?i)(bearer[[:space:]]+)[A-Za-z0-9._~+/-]{16,}=*`), `${1}«redacted»`},
	// 2  authorization: <value>  /  authorization=<value>
	{regexp.MustCompile(`(?i)(authorization[[:space:]]*[:=][[:space:]]*)[^[:space:]"']+`), `${1}«redacted»`},
	// 3  api_key / api-key / apikey / secret / token / passwd / password  : <value>
	{regexp.MustCompile(`(?i)((api[_-]?key|secret|token|passwd|password)[[:space:]]*[:=][[:space:]]*)[^[:space:]"']{6,}`), `${1}«redacted»`},
	// 4  AWS access key id
	{regexp.MustCompile(`(AKIA|ASIA)[A-Z0-9]{16}`), `«redacted-aws-key»`},
	// 5  OpenAI-style secret key
	{regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`), `«redacted»`},
	// 6  GitHub token (ghp_ / gho_ / ghu_ / ghs_ / ghr_)
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), `«redacted»`},
	// 7  PEM private-key header
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), `«redacted-private-key»`},
}

// Scrub applies the seven redaction rules in order and returns the result.
func Scrub(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
