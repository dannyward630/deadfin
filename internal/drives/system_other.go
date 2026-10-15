// SPDX-License-Identifier: GPL-2.0-only

//go:build !linux && !darwin

package drives

import (
	"context"
	"errors"
)

// systemMounts reports no mounts where Deadfin has no mount-table reader.
func systemMounts(context.Context) ([]Mount, error) {
	return nil, nil
}

func totalSize(string) (uint64, error) {
	return 0, errors.New("drives: filesystem size is not supported on this platform")
}
