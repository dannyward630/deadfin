// SPDX-License-Identifier: GPL-2.0-only

// Package drives lists the mount points that GET /Environment/Drives reports.
//
// Upstream ManagedFileSystem.GetDrives (ManagedFileSystem.cs:545-560) keeps a
// .NET DriveInfo when its DriveType is Fixed, Network, or Removable, when it
// IsReady, and when its TotalSize is not zero. On Unix, .NET reads every mount
// point from the system mount table, IsReady is Directory.Exists(mount point),
// and TotalSize comes from statfs. This package keeps that filter pure (Filter)
// and puts the operating-system reads behind Probe and System.
package drives

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// Mount is one mount-table entry.
type Mount struct {
	// Path is the mount point.
	Path string
	// FSType is the filesystem type name from the mount table.
	FSType string
}

// Type is the .NET System.IO.DriveType of a mount.
type Type int

// The DriveType values that the filter uses.
const (
	TypeUnknown Type = iota
	TypeRemovable
	TypeFixed
	TypeNetwork
	TypeCDRom
	TypeRAM
)

// Probe reads the filesystem facts the filter needs for one mount point.
type Probe interface {
	// IsDir reports Directory.Exists(path): path exists and is a directory
	// after symlink resolution.
	IsDir(path string) bool
	// TotalSize returns the total size of the filesystem at path in bytes.
	TotalSize(path string) (uint64, error)
}

// Filter applies the upstream GetDrives filter to mounts, in mount-table
// order. It returns the mount points that pass.
func Filter(mounts []Mount, probe Probe) []string {
	var paths []string
	for _, mount := range mounts {
		switch ClassifyFSType(mount.FSType) {
		case TypeFixed, TypeNetwork, TypeRemovable:
		default:
			continue
		}
		if !probe.IsDir(mount.Path) {
			continue
		}
		size, err := probe.TotalSize(mount.Path)
		if err != nil || size == 0 {
			continue
		}
		paths = append(paths, mount.Path)
	}
	return paths
}

// ClassifyFSType maps a filesystem type name to a DriveType. The table follows
// the .NET runtime's Unix drive-type mapping for the types a media server
// meets. A type that is not in the table is TypeUnknown, which the filter
// drops (divergence D-58).
func ClassifyFSType(fsType string) Type {
	name := strings.ToLower(fsType)
	if strings.HasPrefix(name, "fuse.") {
		// FUSE mounts report "fuse.<driver>" in the mount table and the
		// FUSE magic in statfs; .NET sees the magic.
		name = "fuse"
	}
	switch name {
	case "adfs", "affs", "apfs", "aufs", "befs", "bfs", "btrfs", "drvfs", "ecryptfs", "efs",
		"exfat", "ext", "ext2", "ext2_old", "ext3", "ext4", "ext4dev", "f2fs", "fat", "fuse",
		"fuseblk", "fuseext2", "fusefat", "hfs", "hfs+", "hfsplus", "hfsx", "hostfs", "hpfs",
		"jffs", "jffs2", "jfs", "lofs", "lustre", "minix", "msdos", "nilfs", "nilfs2", "ntfs",
		"ntfs3", "ocfs2", "omfs", "overlay", "overlayfs", "qnx4", "qnx6", "reiserfs", "squashfs",
		"sysv", "ubifs", "ufs", "umsdos", "vboxsf", "vfat", "virtiofs", "xenix", "xfs", "xiafs",
		"zfs", "bcachefs":
		return TypeFixed
	case "9p", "acfs", "afp", "afpfs", "afs", "ceph", "cifs", "coda", "gfs", "gfs2", "glusterfs",
		"ncp", "ncpfs", "nfs", "nfs4", "nfsd", "novell", "smb", "smb2", "smb3", "smbfs", "snfs",
		"sshfs", "vxfs", "webdav":
		return TypeNetwork
	case "cddafs", "cd9660", "iso", "isofs", "iso9660", "fuseiso", "fuseiso9660", "udf":
		return TypeCDRom
	case "ramfs", "tmpfs", "shmfs", "devtmpfs", "proc", "sysfs", "cgroup", "cgroup2", "devpts",
		"debugfs", "securityfs", "tracefs", "mqueue", "configfs", "bpf", "pstore", "hugetlbfs",
		"fusectl", "binfmt_misc", "autofs", "nsfs", "rpc_pipefs", "selinuxfs", "efivarfs",
		"devfs", "rootfs":
		return TypeRAM
	default:
		return TypeUnknown
	}
}

// ParseMounts reads a Linux mounts table (/proc/self/mounts, fstab format) the
// way getmntent does: whitespace-separated fields, with the octal escapes
// \040, \011, \012, and \134 decoded in the mount point.
func ParseMounts(ctx context.Context, r io.Reader) ([]Mount, error) {
	var mounts []Mount
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mounts = append(mounts, Mount{Path: unescapeMountField(fields[1]), FSType: unescapeMountField(fields[2])})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

func unescapeMountField(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}
	var builder strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] == '\\' && i+4 <= len(field) {
			if decoded, ok := mountEscape(field[i+1 : i+4]); ok {
				builder.WriteByte(decoded)
				i += 3
				continue
			}
		}
		builder.WriteByte(field[i])
	}
	return builder.String()
}

// mountEscape decodes the octal escapes that getmntent decodes.
func mountEscape(code string) (byte, bool) {
	switch code {
	case "040":
		return ' ', true
	case "011":
		return '\t', true
	case "012":
		return '\n', true
	case "134":
		return '\\', true
	default:
		return 0, false
	}
}
