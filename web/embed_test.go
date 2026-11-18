package web

import (
	"io/fs"
	"regexp"
	"testing"
)

// TestPagesReferenceEmbeddedFiles catches a script or stylesheet that a page
// loads but the embed directive does not include: the desktop app serves
// only embedded files, so such a page breaks there and nowhere else.
func TestPagesReferenceEmbeddedFiles(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="/([A-Za-z0-9._-]+\.(?:js|css|svg))"`)
	pages, err := fs.Glob(FS, "*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		raw, err := FS.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(raw), -1) {
			if _, err := fs.Stat(FS, m[1]); err != nil {
				t.Errorf("%s loads /%s, which is not embedded", page, m[1])
			}
		}
	}
}
