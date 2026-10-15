// SPDX-License-Identifier: GPL-2.0-only

// Source-pinned cleaner error-seam regressions.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// CleanDateTimeParser.cs:16-52 and CleanStringParser.cs:19-53 execute configured .NET regular expressions.
func TestUpstreamVideoConfiguredCleanersExecuteValidDotNetLookaheads(t *testing.T) {
	t.Run("date", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.CleanDateTimeRegexes = []string{`^(Title)(?=ZZ)ZZ(2024)()()$`}
		got, err := naming.CleanDateTimeWithError("TitleZZ2024", opts)
		if err != nil || got.Name != "Title" || !reflect.DeepEqual(got.Year, intp(2024)) {
			t.Fatalf("CleanDateTimeWithError dotnet lookahead = (%#v, %v), want Title/2024 without error", got, err)
		}
	})

	t.Run("string", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.CleanStringRegexes = []string{`^(?<cleaned>Title)(?=ZZ)ZZ$`}
		got, cleaned, err := naming.CleanStringWithError("TitleZZ", opts)
		if err != nil || !cleaned || got != "Title" {
			t.Fatalf("CleanStringWithError dotnet lookahead = (%q, %t, %v), want (Title, true, nil)", got, cleaned, err)
		}
	})
}

// VideoResolver.cs:70-112 and GenericVideoResolver.cs:36-75 invoke both cleaners; a reached malformed rule is observable as an error.
func TestUpstreamVideoCleanerErrorsPropagateThroughDirectAndResolverSeams(t *testing.T) {
	for _, tt := range []struct {
		name        string
		configure   func(*naming.NamingOptions)
		directError func(naming.NamingOptions) error
	}{
		{
			name:      "date",
			configure: func(opts *naming.NamingOptions) { opts.CleanDateTimeRegexes = []string{`[`} },
			directError: func(opts naming.NamingOptions) error {
				_, err := naming.CleanDateTimeWithError("Movie", opts)
				return err
			},
		},
		{
			name:      "string",
			configure: func(opts *naming.NamingOptions) { opts.CleanStringRegexes = []string{`[`} },
			directError: func(opts naming.NamingOptions) error {
				_, _, err := naming.CleanStringWithError("Movie", opts)
				return err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := naming.DefaultOptions()
			tt.configure(&opts)
			if err := tt.directError(opts); err == nil {
				t.Fatal("direct configured cleaner returned nil error for reached malformed expression")
			}
			if got, err := naming.ResolveVideoWithError("Movie.mkv", opts); err == nil || got != nil {
				t.Fatalf("ResolveVideoWithError malformed %s cleaner = (%#v, %v), want (nil, error)", tt.name, got, err)
			}
			if got, err := naming.ResolveVideoDirectoryWithError("Movie", opts, true, ""); err == nil || got != nil {
				t.Fatalf("ResolveVideoDirectoryWithError malformed %s cleaner = (%#v, %v), want (nil, error)", tt.name, got, err)
			}
		})
	}
}
