// Package shell quotes values for the commands tidewake prints.
package shell

import "strings"

// unsafeChars need quoting; newline and tab would otherwise split a printed command.
const unsafeChars = " \t\n'\"$`\\!*?[]{}()<>|&;#~"

// Quote returns s as one POSIX shell word.
func Quote(s string) string {
	if s != "" && !strings.ContainsAny(s, unsafeChars) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
