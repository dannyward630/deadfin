// SPDX-License-Identifier: GPL-2.0-only

package path

import "strings"

// ValidFilename ports ManagedFileSystem.GetValidFilename: it replaces each
// character that is invalid in a file name with a space.
func ValidFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("\"<>|:*?\\/", r) {
			return ' '
		}
		return r
	}, name)
}
