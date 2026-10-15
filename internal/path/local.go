// SPDX-License-Identifier: GPL-2.0-only

package path

import "path/filepath"

// JoinLocal joins root and the relative path rel. It reports false when rel
// is not local to root: empty, absolute, or escaping root through "..".
// Use it for every file-system path that takes a request-supplied name.
func JoinLocal(root, rel string) (string, bool) {
	if !filepath.IsLocal(rel) {
		return "", false
	}
	// IsLocal already rejects a rel that escapes root; cleaning it under
	// "/" keeps the result inside root on its own as well.
	return filepath.Join(root, filepath.Clean("/"+rel)), true
}
