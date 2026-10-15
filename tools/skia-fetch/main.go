// SPDX-License-Identifier: GPL-2.0-only

// Command skia-fetch installs the pinned SkiaSharp native library
// (libSkiaSharp.so) for one Linux architecture.
//
// Usage: skia-fetch [-arch amd64|arm64] [-cache DIR] [-offline] -o PATH
//
// The library comes from the official SkiaSharp.NativeAssets.Linux NuGet
// package, the same package and version that the pinned Jellyfin revision
// uses (Directory.Packages.props). The tool verifies the SHA-256 of the
// .nupkg and of each extracted libSkiaSharp.so against the pins below. It
// keeps the verified libraries in a cache directory, so it downloads the
// package only on a cache miss. With -offline, a cache miss is an error and
// the tool does not use the network.
//
// The default cache directory is $DEADFIN_SKIA_CACHE, else
// <user cache dir>/deadfin/skiasharp.
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	usageText      = "usage: skia-fetch [-arch amd64|arm64] [-cache DIR] [-offline] -o PATH\n"
	cacheEnv       = "DEADFIN_SKIA_CACHE"
	libraryName    = "libSkiaSharp.so"
	maxPackageSize = 256 << 20
	maxLibrarySize = 64 << 20
	fetchTimeout   = 10 * time.Minute
)

// manifest pins one NuGet package and the native libraries taken from it.
type manifest struct {
	Package string
	Version string
	URL     string
	SHA256  string
	// Libraries maps a Go architecture name to the library inside the package.
	Libraries map[string]library
}

// library is one native library inside the package.
type library struct {
	Entry  string
	SHA256 string
}

// pinned returns the SkiaSharp pin. Change the version only together with
// upstream/PIN (Directory.Packages.props) and third_party/skia-sharp/.
func pinned() manifest {
	return manifest{
		Package: "SkiaSharp.NativeAssets.Linux",
		Version: "3.119.4",
		URL:     "https://api.nuget.org/v3-flatcontainer/skiasharp.nativeassets.linux/3.119.4/skiasharp.nativeassets.linux.3.119.4.nupkg",
		SHA256:  "fae0554059b1107ef7888e46c20bdfb548401ef7a7a6f7391ad4fadc7432d50a",
		Libraries: map[string]library{
			"amd64": {Entry: "runtimes/linux-x64/native/libSkiaSharp.so", SHA256: "66c856eaf1a47a00b23204c30c6ee407987bf5086ecc0a1a6b4fd67526b0cd02"},
			"arm64": {Entry: "runtimes/linux-arm64/native/libSkiaSharp.so", SHA256: "87d2d56c49a9b1d1da618dfa20994ee213f752f9ada04e085703115162997aef"},
		},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], pinned(), http.DefaultClient, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "skia-fetch: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, pin manifest, client *http.Client, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("skia-fetch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usageText) }
	arch := flags.String("arch", runtime.GOARCH, "Linux architecture: "+strings.Join(pin.architectures(), " or "))
	cacheDir := flags.String("cache", "", "cache directory for verified libraries")
	offline := flags.Bool("offline", false, "use only the cache; fail on a cache miss")
	output := flags.String("o", "", "install path for libSkiaSharp.so")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		fmt.Fprint(stderr, usageText)
		return errors.New("missing -o or extra arguments")
	}
	lib, ok := pin.Libraries[*arch]
	if !ok {
		return fmt.Errorf("unsupported architecture %q; want %s", *arch, strings.Join(pin.architectures(), " or "))
	}
	if *cacheDir == "" {
		dir, err := defaultCacheDir()
		if err != nil {
			return err
		}
		*cacheDir = dir
	}

	cached := pin.cachePath(*cacheDir, *arch)
	if err := verifyFile(cached, lib.SHA256); err != nil {
		if *offline {
			return fmt.Errorf("%s %s linux-%s is not in the cache (%v); run `go run ./tools/skia-fetch -o PATH` with network access first", pin.Package, pin.Version, *arch, err)
		}
		fmt.Fprintf(stdout, "skia-fetch: downloading %s %s\n", pin.Package, pin.Version)
		if err := pin.fill(ctx, client, *cacheDir); err != nil {
			return err
		}
	}
	if err := installFile(cached, *output, lib.SHA256); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "skia-fetch: %s %s linux-%s sha256 %s -> %s\n", pin.Package, pin.Version, *arch, lib.SHA256, *output)
	return nil
}

func defaultCacheDir() (string, error) {
	if dir := os.Getenv(cacheEnv); dir != "" {
		return dir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory; set %s or pass -cache: %w", cacheEnv, err)
	}
	return filepath.Join(dir, "deadfin", "skiasharp"), nil
}

func (m manifest) architectures() []string {
	names := make([]string, 0, len(m.Libraries))
	for name := range m.Libraries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m manifest) cachePath(cacheDir, arch string) string {
	return filepath.Join(cacheDir, m.Package, m.Version, "linux-"+arch, libraryName)
}

// fill downloads the package, verifies it, and puts every pinned library into
// the cache. The package itself is not kept.
func (m manifest) fill(ctx context.Context, client *http.Client, cacheDir string) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	pkg, err := os.CreateTemp(cacheDir, ".nupkg-*")
	if err != nil {
		return err
	}
	defer os.Remove(pkg.Name())
	defer pkg.Close()

	if err := download(ctx, client, m.URL, pkg, m.SHA256); err != nil {
		return err
	}
	info, err := pkg.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(pkg, info.Size())
	if err != nil {
		return fmt.Errorf("read %s %s: %w", m.Package, m.Version, err)
	}
	for _, arch := range m.architectures() {
		lib := m.Libraries[arch]
		if err := extract(archive, lib, m.cachePath(cacheDir, arch)); err != nil {
			return fmt.Errorf("linux-%s: %w", arch, err)
		}
	}
	return nil
}

func download(ctx context.Context, client *http.Client, url string, dst io.Writer, want string) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(resp.Body, maxPackageSize+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxPackageSize {
		return fmt.Errorf("download %s: larger than %d bytes", url, maxPackageSize)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("download %s: sha256 %s, want %s", url, got, want)
	}
	return nil
}

func extract(archive *zip.Reader, lib library, dst string) error {
	for _, file := range archive.File {
		if file.Name != lib.Entry {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		return writeVerified(io.LimitReader(src, maxLibrarySize+1), dst, lib.SHA256)
	}
	return fmt.Errorf("%s not in package", lib.Entry)
}

func installFile(src, dst, want string) error {
	if verifyFile(dst, want) == nil {
		return nil
	}
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeVerified(file, dst, want)
}

// writeVerified writes src to dst through a temporary file in the same
// directory. It renames the file into place only when the SHA-256 matches.
func writeVerified(src io.Reader, dst, want string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, hash), src)
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n > maxLibrarySize {
		return fmt.Errorf("%s: larger than %d bytes", dst, maxLibrarySize)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("%s: sha256 %s, want %s", dst, got, want)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func verifyFile(path, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("%s: sha256 %s, want %s", path, got, want)
	}
	return nil
}
