package storage

import (
	"mime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentDisposition(t *testing.T) {
	cases := []struct {
		name         string
		filename     string
		wantFilename string
	}{
		{"plain name", "report.pdf", "report.pdf"},
		{"quotes are neutralised", `evil".html`, `evil".html`},
		{"path is stripped", "../../etc/passwd", "passwd"},
		{"windows path is stripped", `C:\Windows\system32\evil.exe`, "evil.exe"},
		{"non-ascii survives", "réport.pdf", "réport.pdf"},
		{"empty name falls back", "", "download"},
		{"dotdot alone falls back", "..", "download"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := contentDisposition(tc.filename)

			// Whatever we emit must parse back to an attachment with the expected name,
			// which is the property that matters for the browser.
			mediaType, params, err := mime.ParseMediaType(header)
			require.NoError(t, err, "header must be parseable: %q", header)
			assert.Equal(t, "attachment", mediaType)
			assert.Equal(t, tc.wantFilename, params["filename"])
		})
	}
}

func TestContentDisposition_StripsControlCharacters(t *testing.T) {
	// A CR/LF in the filename must never reach the header value. The remaining text is
	// harmless once the line breaks are gone: it stays inside the quoted filename
	// parameter and cannot start a new header.
	header := contentDisposition("evil\r\nX-Injected: yes.txt")

	assert.NotContains(t, header, "\r")
	assert.NotContains(t, header, "\n")

	mediaType, params, err := mime.ParseMediaType(header)
	require.NoError(t, err)
	assert.Equal(t, "attachment", mediaType)
	assert.Equal(t, "evilX-Injected: yes.txt", params["filename"])
}

func TestContentDisposition_AlwaysAttachment(t *testing.T) {
	// Files whose detected type renders inline are exactly the XSS risk, so they must
	// still come back as attachments.
	for _, name := range []string{"payload.html", "payload.svg", "payload.xml"} {
		header := contentDisposition(name)
		assert.True(t, strings.HasPrefix(header, "attachment"), "got %q", header)
	}
}
