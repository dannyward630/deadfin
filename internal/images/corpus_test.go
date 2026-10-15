// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
	_ "golang.org/x/image/webp"
)

const (
	stockCorpusRoot        = "testdata/stock-corpus"
	pendingCapture         = "PENDING_CAPTURE"
	stockCorpusSchema      = 1
	minimumStockCorpusSSIM = 0.98
)

var (
	sha256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ociDigestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	commitPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	stockCorpusIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// testP301StockCorpus is deliberately called by TestNativeProcessor, which owns
// the single process-lifetime libvips Runtime. It accepts only source-bound
// artifacts captured from stock; PENDING_CAPTURE is an intentional red state,
// never a skipped or synthesized reference.
func testP301StockCorpus(t *testing.T, runtimeOwner *images.Runtime) {
	t.Helper()
	corpus := loadStockCorpus(t)
	if corpus.SchemaVersion != stockCorpusSchema {
		t.Errorf("manifest schemaVersion = %d, want %d", corpus.SchemaVersion, stockCorpusSchema)
	}
	for _, issue := range validateStockCorpusCoverage(corpus) {
		t.Error(issue)
	}
	t.Run("resolves TopLeft raw orientation request to source-byte no-op", func(t *testing.T) {
		var topLeft *stockCorpusCase
		for index := range corpus.Cases {
			if corpus.Cases[index].ID == "jpeg-orientation-1" {
				topLeft = &corpus.Cases[index]
				break
			}
		}
		if topLeft == nil {
			t.Fatal("manifest lacks jpeg-orientation-1 TopLeft contract case")
		}
		if !topLeft.Request.AutoOrient {
			t.Error("TopLeft raw request autoOrient = false, want true")
		}
		if effectiveAutoOrient(topLeft.Request) {
			t.Error("TopLeft effective processor auto-orient = true, want false")
		}
		source, err := os.ReadFile(filepath.Join(stockCorpusRoot, topLeft.Source.Path))
		if err != nil {
			t.Fatalf("ReadFile(TopLeft source): %v", err)
		}
		stock, err := os.ReadFile(filepath.Join(stockCorpusRoot, topLeft.Stock.Path))
		if err != nil {
			t.Fatalf("ReadFile(TopLeft stock): %v", err)
		}
		if !bytes.Equal(source, stock) {
			t.Error("TopLeft stock body differs from source bytes, want raw route no-op")
		}
	})

	cacheRoot := t.TempDir()
	processor, err := images.NewProcessor(runtimeOwner, images.ProcessorConfig{
		CacheRoot:     cacheRoot,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatalf("NewProcessor(stock corpus): %v", err)
	}

	for _, testCase := range corpus.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			inputPath, stockPath, ready := requireCapturedStockCase(t, testCase)
			if !ready {
				return
			}
			if testCase.Request.OutputFormat == "webp" && testCase.Request.Quality == 0 && runtime.GOOS != "linux" {
				t.Skip("WebP Q0 stock parity requires the packaged Linux libSkiaSharp 3.119.4 runtime; run the linux/amd64 image corpus")
			}

			stock, stockFormat := decodeStockImage(t, stockPath)
			assertStockMetadata(t, "stock", testCase, stock, stockFormat, stockPath)

			modified, err := time.Parse(time.RFC3339, testCase.Request.SourceModifiedUTC)
			if err != nil {
				t.Fatalf("manifest request.sourceModifiedUTC = %q: %v", testCase.Request.SourceModifiedUTC, err)
			}
			actual, err := processor.Process(context.Background(), images.ProcessOptions{
				Source:       images.Source{Path: inputPath, Modified: modified},
				OutputFormat: manifestFormat(testCase.Request.OutputFormat),
				Quality:      testCase.Request.Quality,
				Resize:       manifestResize(testCase.Request.Resize),
				AutoOrient:   effectiveAutoOrient(testCase.Request),
			})
			if err != nil {
				t.Errorf("Processor.Process() error: %v", err)
				return
			}
			if actual.MIMEType != testCase.Expected.MIMEType {
				t.Errorf("Processor.Process().MIMEType = %q, want %q", actual.MIMEType, testCase.Expected.MIMEType)
			}
			wantDimensions := images.Dimensions{Width: testCase.Expected.Width, Height: testCase.Expected.Height}
			if actual.Dimensions != wantDimensions {
				t.Errorf("Processor.Process().Dimensions = %#v, want %#v", actual.Dimensions, wantDimensions)
			}

			produced, producedFormat := decodeStockImage(t, actual.Path)
			assertStockMetadata(t, "processor output", testCase, produced, producedFormat, actual.Path)
			score, err := images.SSIM(stock, produced)
			if err != nil {
				t.Errorf("SSIM(stock, processor output): %v", err)
				return
			}
			residual := 1 - score
			t.Logf("SSIM = %.6f, residual = %.6f", score, residual)
			if score < minimumStockCorpusSSIM {
				t.Errorf("SSIM = %.6f, residual = %.6f, want >= %.2f", score, residual, minimumStockCorpusSSIM)
			}
		})
	}
}

type stockCorpusManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Cases         []stockCorpusCase `json:"cases"`
}

type stockCorpusCase struct {
	ID         string              `json:"id"`
	Categories []string            `json:"categories"`
	Source     stockCorpusSource   `json:"source"`
	Stock      stockCorpusStock    `json:"stock"`
	Request    stockCorpusRequest  `json:"request"`
	Expected   stockCorpusExpected `json:"expected"`
}

type stockCorpusSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type stockCorpusStock struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	OCIDigest      string `json:"ociDigest"`
	UpstreamCommit string `json:"upstreamCommit"`
	CapturedAt     string `json:"capturedAt"`
	ToolVersion    string `json:"toolVersion"`
}

type stockCorpusRequest struct {
	ItemKey           string            `json:"itemKey"`
	ImageType         string            `json:"imageType"`
	SourceModifiedUTC string            `json:"sourceModifiedUTC"`
	OutputFormat      string            `json:"outputFormat"`
	Quality           int               `json:"quality"`
	Resize            stockCorpusResize `json:"resize"`
	AutoOrient        bool              `json:"autoOrient"`
	// ProcessorAutoOrient is the item-orientation-resolved option. AutoOrient
	// remains the raw route request for source provenance.
	ProcessorAutoOrient *bool              `json:"processorAutoOrient"`
	Capture             stockCorpusCapture `json:"capture"`
}

type stockCorpusResize struct {
	Width      *int `json:"width"`
	Height     *int `json:"height"`
	MaxWidth   *int `json:"maxWidth"`
	MaxHeight  *int `json:"maxHeight"`
	FillWidth  *int `json:"fillWidth"`
	FillHeight *int `json:"fillHeight"`
}

type stockCorpusExpected struct {
	MIMEType    string `json:"mimeType"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Orientation int    `json:"orientation"`
}

// Capture is route provenance recorded beside the request whose stock response
// it produced. The processor test remains independent of Jellyfin HTTP routing.
type stockCorpusCapture struct {
	ContentType  string `json:"contentType"`
	HTTPStatus   int    `json:"httpStatus"`
	ImageIndex   *int   `json:"imageIndex"`
	ImageType    string `json:"imageType"`
	ItemIdentity string `json:"itemIdentity"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Transport    string `json:"transport"`
	URL          string `json:"url"`
}

func loadStockCorpus(t *testing.T) stockCorpusManifest {
	t.Helper()
	file, err := os.Open(filepath.Join(stockCorpusRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("Open(stock corpus manifest): %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close stock corpus manifest: %v", err)
		}
	})

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var corpus stockCorpusManifest
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatalf("Decode(stock corpus manifest): %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("Decode(stock corpus manifest trailing data): %v", err)
	}
	return corpus
}

func validateStockCorpusCoverage(corpus stockCorpusManifest) []string {
	issues := make([]string, 0)
	if len(corpus.Cases) != 50 {
		issues = append(issues, fmt.Sprintf("stock corpus has %d cases, want exactly 50", len(corpus.Cases)))
	}

	seenIDs := make(map[string]struct{}, len(corpus.Cases))
	outputFormats := make(map[string]int)
	sourceExtensions := make(map[string]int)
	fixed, maximum, fill, noResize, autoOrient, alphaInput := 0, 0, 0, 0, 0, 0
	qualities := make(map[int]int)
	for _, testCase := range corpus.Cases {
		if !stockCorpusIDPattern.MatchString(testCase.ID) {
			issues = append(issues, fmt.Sprintf("case %q has an invalid stable ID", testCase.ID))
		}
		if _, duplicate := seenIDs[testCase.ID]; duplicate {
			issues = append(issues, fmt.Sprintf("case %q is duplicated", testCase.ID))
		}
		seenIDs[testCase.ID] = struct{}{}
		if testCase.Source.Kind != "local-fixture" {
			issues = append(issues, fmt.Sprintf("case %s source.kind = %q, want local-fixture", testCase.ID, testCase.Source.Kind))
		}
		if !corpusRelativePath(testCase.Source.Path, "inputs/") {
			issues = append(issues, fmt.Sprintf("case %s source.path = %q, want a relative inputs/ path", testCase.ID, testCase.Source.Path))
		}
		if !corpusRelativePath(testCase.Stock.Path, "stock/") {
			issues = append(issues, fmt.Sprintf("case %s stock.path = %q, want a relative stock/ path", testCase.ID, testCase.Stock.Path))
		}
		outputFormats[testCase.Request.OutputFormat]++
		if !hasExactStockCategories(testCase.Categories, testCase.Request.OutputFormat) {
			issues = append(issues, fmt.Sprintf("case %s categories = %q, want stock-http, local-fixture, and %s", testCase.ID, testCase.Categories, testCase.Request.OutputFormat))
		}
		sourceExtension := strings.ToLower(filepath.Ext(testCase.Source.Path))
		sourceExtensions[sourceExtension]++
		if sourceExtension == ".png" {
			alphaInput++
		}
		if testCase.Request.Resize.Width != nil || testCase.Request.Resize.Height != nil {
			fixed++
		}
		if testCase.Request.Resize.MaxWidth != nil || testCase.Request.Resize.MaxHeight != nil {
			maximum++
		}
		if testCase.Request.Resize.FillWidth != nil || testCase.Request.Resize.FillHeight != nil {
			fill++
		}
		if testCase.Request.Resize == (stockCorpusResize{}) && !testCase.Request.AutoOrient {
			noResize++
		}
		if testCase.Request.AutoOrient {
			autoOrient++
		}
		qualities[testCase.Request.Quality]++
		if extensionForManifestFormat(testCase.Request.OutputFormat) != filepath.Ext(testCase.Stock.Path) {
			issues = append(issues, fmt.Sprintf("case %s stock path %q does not match output format %q", testCase.ID, testCase.Stock.Path, testCase.Request.OutputFormat))
		}
	}
	for _, requirement := range []struct {
		name string
		got  int
		want int
	}{
		{"JPEG outputs", outputFormats["jpeg"], 18},
		{"PNG outputs", outputFormats["png"], 16},
		{"WebP outputs", outputFormats["webp"], 16},
		{"JPEG inputs", sourceExtensions[".jpg"], 29},
		{"PNG inputs", sourceExtensions[".png"], 14},
		{"WebP inputs", sourceExtensions[".webp"], 7},
		{"fixed-dimension requests", fixed, 12},
		{"maximum-bound requests", maximum, 10},
		{"fill-box requests", fill, 8},
		{"no-resize non-orientation requests", noResize, 12},
		{"auto-orientation requests", autoOrient, 8},
		{"transparent PNG input requests", alphaInput, 14},
	} {
		if requirement.got != requirement.want {
			issues = append(issues, fmt.Sprintf("stock corpus %s = %d, want %d", requirement.name, requirement.got, requirement.want))
		}
	}
	for _, quality := range []int{0, 30, 50, 100} {
		if qualities[quality] == 0 {
			issues = append(issues, fmt.Sprintf("stock corpus has no explicit quality %d request", quality))
		}
	}
	return issues
}

func requireCapturedStockCase(t *testing.T, testCase stockCorpusCase) (string, string, bool) {
	t.Helper()
	missing := make([]string, 0)
	if testCase.Source.Kind != "local-fixture" {
		missing = append(missing, "source.kind must be local-fixture")
	}
	if !corpusRelativePath(testCase.Source.Path, "inputs/") {
		missing = append(missing, "source.path must remain below inputs/")
	}
	if !corpusRelativePath(testCase.Stock.Path, "stock/") {
		missing = append(missing, "stock.path must remain below stock/")
	}
	inputPath := filepath.Join(stockCorpusRoot, testCase.Source.Path)
	stockPath := filepath.Join(stockCorpusRoot, testCase.Stock.Path)
	if _, err := os.Stat(inputPath); err != nil {
		missing = append(missing, fmt.Sprintf("source artifact %q: %v", testCase.Source.Path, err))
	} else if !validSHA256(testCase.Source.SHA256) {
		missing = append(missing, "source.sha256 must be a captured lowercase SHA-256")
	} else if err := assertFileSHA256(inputPath, testCase.Source.SHA256); err != nil {
		missing = append(missing, fmt.Sprintf("source artifact: %v", err))
	}
	if _, err := os.Stat(stockPath); err != nil {
		missing = append(missing, fmt.Sprintf("stock artifact %q: %v", testCase.Stock.Path, err))
	} else if !validSHA256(testCase.Stock.SHA256) {
		missing = append(missing, "stock.sha256 must be a captured lowercase SHA-256")
	} else if err := assertFileSHA256(stockPath, testCase.Stock.SHA256); err != nil {
		missing = append(missing, fmt.Sprintf("stock artifact: %v", err))
	}
	if !ociDigestPattern.MatchString(testCase.Stock.OCIDigest) {
		missing = append(missing, "stock.ociDigest must be a captured sha256 OCI digest")
	}
	if !commitPattern.MatchString(testCase.Stock.UpstreamCommit) {
		missing = append(missing, "stock.upstreamCommit must be a captured 40-character commit")
	}
	if testCase.Stock.ToolVersion == "" || testCase.Stock.ToolVersion == pendingCapture {
		missing = append(missing, "stock.toolVersion must identify the capture recorder")
	}
	if _, err := time.Parse(time.RFC3339, testCase.Stock.CapturedAt); err != nil {
		missing = append(missing, "stock.capturedAt must be RFC3339")
	}
	capture := testCase.Request.Capture
	if capture.ItemIdentity == "" || capture.ItemIdentity == pendingCapture {
		missing = append(missing, "request.capture.itemIdentity is required")
	}
	if capture.ImageType == "" || capture.ImageType == pendingCapture {
		missing = append(missing, "request.capture.imageType is required")
	}
	if capture.ImageIndex != nil && *capture.ImageIndex < 0 {
		missing = append(missing, "request.capture.imageIndex must be non-negative when present")
	}
	if capture.ContentType != testCase.Expected.MIMEType {
		missing = append(missing, "request.capture.contentType must match expected MIME")
	}
	if capture.HTTPStatus != 200 {
		missing = append(missing, "request.capture.httpStatus must be 200")
	}
	if capture.Method != "GET" {
		missing = append(missing, "request.capture.method must be GET")
	}
	if !strings.HasPrefix(capture.Path, "/") {
		missing = append(missing, "request.capture.path must be an absolute request path")
	}
	if capture.Transport == "" || capture.Transport == pendingCapture {
		missing = append(missing, "request.capture.transport is required")
	}
	if !strings.HasPrefix(capture.URL, "http://") && !strings.HasPrefix(capture.URL, "https://") {
		missing = append(missing, "request.capture.url must be an HTTP URL")
	}
	if testCase.Request.ItemKey == "" || testCase.Request.ImageType == "" {
		missing = append(missing, "request item identity is required")
	}
	if _, err := time.Parse(time.RFC3339, testCase.Request.SourceModifiedUTC); err != nil {
		missing = append(missing, "request.sourceModifiedUTC must be RFC3339")
	}
	if testCase.Request.AutoOrient && testCase.Request.ProcessorAutoOrient == nil {
		missing = append(missing, "request.processorAutoOrient must resolve every raw autoOrient request")
	}
	if !testCase.Request.AutoOrient && testCase.Request.ProcessorAutoOrient != nil {
		missing = append(missing, "request.processorAutoOrient is only valid for a raw autoOrient request")
	}
	// A TopLeft photo is the no-op boundary: Jellyfin receives the raw request
	// but resolves it away before it reaches image processing, so stock returns
	// source bytes rather than a transformed artifact.
	if testCase.ID == "jpeg-orientation-1" {
		if !testCase.Request.AutoOrient || testCase.Request.ProcessorAutoOrient == nil || *testCase.Request.ProcessorAutoOrient {
			missing = append(missing, "TopLeft orientation must preserve raw autoOrient=true but resolve processorAutoOrient=false")
		}
		if testCase.Source.SHA256 != testCase.Stock.SHA256 {
			missing = append(missing, "TopLeft orientation stock body must equal the source bytes")
		}
	}
	if !isManifestFormat(testCase.Request.OutputFormat) {
		missing = append(missing, "request.outputFormat must be jpeg, png, or webp")
	}
	if testCase.Request.Quality < 0 || testCase.Request.Quality > 100 {
		missing = append(missing, "request.quality must be between 0 and 100")
	}
	if testCase.Expected.MIMEType != mimeForManifestFormat(testCase.Request.OutputFormat) {
		missing = append(missing, "expected.mimeType must match request.outputFormat")
	}
	if testCase.Expected.Width <= 0 || testCase.Expected.Height <= 0 {
		missing = append(missing, "expected dimensions must be captured positive values")
	}
	if testCase.Expected.Orientation < 1 || testCase.Expected.Orientation > 8 {
		missing = append(missing, "expected.orientation must be an EXIF orientation 1..8")
	}
	if len(missing) != 0 {
		t.Errorf("capture incomplete: %s", strings.Join(missing, "; "))
		return "", "", false
	}
	return inputPath, stockPath, true
}

func assertFileSHA256(path, want string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	got := sha256.Sum256(contents)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("SHA-256 for %q = %x, want %s", path, got, want)
	}
	return nil
}

func decodeStockImage(t *testing.T, path string) (image.Image, string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Decode(%q): %v", path, err)
	}
	return decoded, format
}

func assertStockMetadata(t *testing.T, artifact string, testCase stockCorpusCase, decoded image.Image, format, path string) {
	t.Helper()
	wantFormat := testCase.Request.OutputFormat
	if format != wantFormat {
		t.Errorf("%s decoded format = %q, want %q", artifact, format, wantFormat)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != testCase.Expected.Width || bounds.Dy() != testCase.Expected.Height {
		t.Errorf("%s dimensions = %dx%d, want %dx%d", artifact, bounds.Dx(), bounds.Dy(), testCase.Expected.Width, testCase.Expected.Height)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("ReadFile(%s %q): %v", artifact, path, err)
		return
	}
	orientation, err := encodedOrientation(contents, format)
	if err != nil {
		t.Errorf("%s orientation: %v", artifact, err)
		return
	}
	if orientation != testCase.Expected.Orientation {
		t.Errorf("%s EXIF orientation = %d, want %d", artifact, orientation, testCase.Expected.Orientation)
	}
}

func encodedOrientation(contents []byte, format string) (int, error) {
	switch format {
	case "jpeg":
		return jpegOrientation(contents)
	case "png":
		return pngOrientation(contents)
	case "webp":
		return webpOrientation(contents)
	default:
		return 0, fmt.Errorf("unsupported decoded format %q", format)
	}
}

func jpegOrientation(contents []byte) (int, error) {
	if len(contents) < 4 || contents[0] != 0xff || contents[1] != 0xd8 {
		return 0, fmt.Errorf("missing JPEG SOI")
	}
	for offset := 2; offset+4 <= len(contents); {
		if contents[offset] != 0xff {
			return 0, fmt.Errorf("invalid JPEG marker at byte %d", offset)
		}
		for offset < len(contents) && contents[offset] == 0xff {
			offset++
		}
		if offset >= len(contents) || contents[offset] == 0xda || contents[offset] == 0xd9 {
			return 1, nil
		}
		offset++
		if offset+2 > len(contents) {
			return 0, fmt.Errorf("truncated JPEG segment length")
		}
		length := int(binary.BigEndian.Uint16(contents[offset : offset+2]))
		if length < 2 || offset+length > len(contents) {
			return 0, fmt.Errorf("invalid JPEG segment length %d", length)
		}
		segment := contents[offset+2 : offset+length]
		if bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		offset += length
	}
	return 1, nil
}

func pngOrientation(contents []byte) (int, error) {
	const signature = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(contents, []byte(signature)) {
		return 0, fmt.Errorf("missing PNG signature")
	}
	for offset := len(signature); offset+12 <= len(contents); {
		length := int(binary.BigEndian.Uint32(contents[offset : offset+4]))
		if length < 0 || offset+12+length > len(contents) {
			return 0, fmt.Errorf("invalid PNG chunk length %d", length)
		}
		chunkType := string(contents[offset+4 : offset+8])
		if chunkType == "eXIf" {
			return tiffOrientation(contents[offset+8 : offset+8+length])
		}
		if chunkType == "IEND" {
			return 1, nil
		}
		offset += 12 + length
	}
	return 0, fmt.Errorf("PNG lacks IEND")
}

func webpOrientation(contents []byte) (int, error) {
	if len(contents) < 12 || string(contents[:4]) != "RIFF" || string(contents[8:12]) != "WEBP" {
		return 0, fmt.Errorf("missing WebP RIFF signature")
	}
	for offset := 12; offset+8 <= len(contents); {
		chunkType := string(contents[offset : offset+4])
		length := int(binary.LittleEndian.Uint32(contents[offset+4 : offset+8]))
		dataStart := offset + 8
		dataEnd := dataStart + length
		if length < 0 || dataEnd > len(contents) {
			return 0, fmt.Errorf("invalid WebP chunk length %d", length)
		}
		if chunkType == "EXIF" {
			return tiffOrientation(contents[dataStart:dataEnd])
		}
		offset = dataEnd
		if length%2 != 0 {
			offset++
		}
	}
	return 1, nil
}

func tiffOrientation(contents []byte) (int, error) {
	// JPEG APP1 requires this prefix, while WebP EXIF payloads are usually raw
	// TIFF but libvips may retain the prefix. Both encode the same metadata.
	contents = bytes.TrimPrefix(contents, []byte("Exif\x00\x00"))
	if len(contents) < 8 {
		return 0, fmt.Errorf("truncated TIFF header")
	}
	var order binary.ByteOrder
	switch string(contents[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, fmt.Errorf("unknown TIFF byte order %q", contents[:2])
	}
	if order.Uint16(contents[2:4]) != 42 {
		return 0, fmt.Errorf("invalid TIFF magic")
	}
	ifdOffset := int(order.Uint32(contents[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(contents) {
		return 0, fmt.Errorf("invalid TIFF IFD offset %d", ifdOffset)
	}
	entryCount := int(order.Uint16(contents[ifdOffset : ifdOffset+2]))
	entriesEnd := ifdOffset + 2 + entryCount*12
	if entriesEnd > len(contents) {
		return 0, fmt.Errorf("truncated TIFF IFD")
	}
	for offset := ifdOffset + 2; offset < entriesEnd; offset += 12 {
		if order.Uint16(contents[offset:offset+2]) != 0x0112 {
			continue
		}
		if order.Uint16(contents[offset+2:offset+4]) != 3 || order.Uint32(contents[offset+4:offset+8]) != 1 {
			return 0, fmt.Errorf("unsupported TIFF orientation field")
		}
		orientation := int(order.Uint16(contents[offset+8 : offset+10]))
		if orientation < 1 || orientation > 8 {
			return 0, fmt.Errorf("invalid TIFF orientation %d", orientation)
		}
		return orientation, nil
	}
	return 1, nil
}

func hasExactStockCategories(categories []string, format string) bool {
	if len(categories) != 3 {
		return false
	}
	wanted := map[string]bool{"stock-http": false, "local-fixture": false, format: false}
	for _, category := range categories {
		if _, known := wanted[category]; !known || wanted[category] {
			return false
		}
		wanted[category] = true
	}
	return wanted["stock-http"] && wanted["local-fixture"] && wanted[format]
}

func corpusRelativePath(path, prefix string) bool {
	return strings.HasPrefix(path, prefix) && filepath.Clean(path) == path && !strings.Contains(path, "..")
}

func validSHA256(value string) bool {
	return value != pendingCapture && sha256Pattern.MatchString(value)
}

func manifestFormat(value string) images.Format {
	switch value {
	case "jpeg":
		return images.FormatJPEG
	case "png":
		return images.FormatPNG
	case "webp":
		return images.FormatWebP
	default:
		return images.Format("")
	}
}

func manifestResize(resize stockCorpusResize) images.ResizeOptions {
	return images.ResizeOptions{
		Width:      resize.Width,
		Height:     resize.Height,
		MaxWidth:   resize.MaxWidth,
		MaxHeight:  resize.MaxHeight,
		FillWidth:  resize.FillWidth,
		FillHeight: resize.FillHeight,
	}
}

func effectiveAutoOrient(request stockCorpusRequest) bool {
	if request.ProcessorAutoOrient != nil {
		return *request.ProcessorAutoOrient
	}
	return request.AutoOrient
}

func isManifestFormat(value string) bool {
	return value == "jpeg" || value == "png" || value == "webp"
}

func extensionForManifestFormat(value string) string {
	switch value {
	case "jpeg":
		return ".jpg"
	case "png":
		return ".png"
	case "webp":
		return ".webp"
	default:
		return ""
	}
}

func mimeForManifestFormat(value string) string {
	switch value {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}
