// SPDX-License-Identifier: GPL-2.0-only

//go:build !darwin && !linux

package library

import "os"

func entryFileIdentity(_ os.FileInfo) FileIdentity { return FileIdentity{} }
