// SPDX-License-Identifier: GPL-2.0-only

package drives

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeProbe struct {
	dirs  map[string]bool
	sizes map[string]uint64
}

func (p fakeProbe) IsDir(path string) bool { return p.dirs[path] }

func (p fakeProbe) TotalSize(path string) (uint64, error) {
	size, ok := p.sizes[path]
	if !ok {
		return 0, errors.New("statfs failed")
	}
	return size, nil
}

func TestParseMountsDecodesEscapes(t *testing.T) {
	table := strings.Join([]string{
		"overlay / overlay rw,relatime 0 0",
		"proc /proc proc rw,nosuid 0 0",
		`nas:/export /media/my\040movies nfs4 rw 0 0`,
		`/dev/sda1 /mnt/back\134slash ext4 rw 0 0`,
		"",
		"short line",
	}, "\n")
	mounts, err := ParseMounts(context.Background(), strings.NewReader(table))
	if err != nil {
		t.Fatalf("ParseMounts: %v", err)
	}
	want := []Mount{
		{Path: "/", FSType: "overlay"},
		{Path: "/proc", FSType: "proc"},
		{Path: "/media/my movies", FSType: "nfs4"},
		{Path: `/mnt/back\slash`, FSType: "ext4"},
	}
	if !reflect.DeepEqual(mounts, want) {
		t.Fatalf("mounts = %#v, want %#v", mounts, want)
	}
}

func TestParseMountsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseMounts(ctx, strings.NewReader("overlay / overlay rw 0 0\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseMounts error = %v, want context.Canceled", err)
	}
}

// TestFilterAppliesUpstreamGetDrives covers ManagedFileSystem.cs:545-560:
// DriveType Fixed, Network, or Removable; IsReady; TotalSize != 0.
func TestFilterAppliesUpstreamGetDrives(t *testing.T) {
	mounts := []Mount{
		{Path: "/", FSType: "overlay"},
		{Path: "/proc", FSType: "proc"},
		{Path: "/dev", FSType: "tmpfs"},
		{Path: "/dev/pts", FSType: "devpts"},
		{Path: "/sys/fs/cgroup", FSType: "cgroup2"},
		{Path: "/config", FSType: "ext4"},
		{Path: "/etc/hosts", FSType: "ext4"},
		{Path: "/media", FSType: "nfs4"},
		{Path: "/pool", FSType: "fuse.mergerfs"},
		{Path: "/empty", FSType: "xfs"},
		{Path: "/unstatable", FSType: "btrfs"},
		{Path: "/cdrom", FSType: "iso9660"},
		{Path: "/weird", FSType: "somethingnew"},
	}
	probe := fakeProbe{
		dirs: map[string]bool{
			"/": true, "/proc": true, "/dev": true, "/dev/pts": true, "/sys/fs/cgroup": true,
			"/config": true, "/media": true, "/pool": true, "/empty": true, "/unstatable": true,
			"/cdrom": true, "/weird": true,
		},
		sizes: map[string]uint64{
			"/": 10, "/proc": 10, "/dev": 10, "/dev/pts": 10, "/sys/fs/cgroup": 10, "/config": 10,
			"/etc/hosts": 10, "/media": 10, "/pool": 10, "/empty": 0, "/cdrom": 10, "/weird": 10,
		},
	}
	got := Filter(mounts, probe)
	want := []string{"/", "/config", "/media", "/pool"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %q, want %q", got, want)
	}
}

func TestClassifyFSType(t *testing.T) {
	for fsType, want := range map[string]Type{
		"ext4": TypeFixed, "XFS": TypeFixed, "zfs": TypeFixed, "overlay": TypeFixed,
		"nfs": TypeNetwork, "cifs": TypeNetwork, "fuse.sshfs": TypeFixed,
		"tmpfs": TypeRAM, "proc": TypeRAM, "udf": TypeCDRom, "": TypeUnknown,
	} {
		if got := ClassifyFSType(fsType); got != want {
			t.Errorf("ClassifyFSType(%q) = %d, want %d", fsType, got, want)
		}
	}
}

func TestSystemDrivesListsRoot(t *testing.T) {
	got, err := System{}.Drives(context.Background())
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	for _, path := range got {
		if path == "" {
			t.Fatalf("Drives returned an empty mount point: %q", got)
		}
	}
}
