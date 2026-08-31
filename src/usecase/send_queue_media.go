package usecase

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// rehydrateMultipartFile rebuilds a *multipart.FileHeader from a file on disk, so
// a queued media send can be replayed through the existing Send* methods with no
// change to their media handling at all.
//
// Why this shape:
//   - multipart.FileHeader cannot be constructed directly (its backing fields are
//     unexported), and it cannot be JSON-persisted, so the only supported way to
//     obtain a working one is to parse a multipart body.
//   - ReadForm(0) keeps roughly 10MB in memory and spills anything larger to a
//     temp file, so a 100MB video does not land in RAM.
//   - The body is streamed through an io.Pipe rather than buffered, so the source
//     file is never read whole into memory either.
//   - The durable queue file is deliberately NOT handed to the send path directly:
//     fasthttp.SaveMultipartFile renames a disk-backed FileHeader, which would
//     move the queue file away before we know whether the send succeeded. Going
//     through ReadForm means the send path only ever moves a throwaway temp copy.
//
// mimeType is the Content-Type the upload originally arrived with. It matters:
// the image, sticker, video and audio validators reject a part whose Content-Type
// is not an allowed mime, and multipart.CreateFormFile would hardcode
// application/octet-stream. When it is empty (a row queued before it was
// recorded) it is recovered from the extension, then by sniffing the content.
//
// The returned cleanup releases any temp file ReadForm created and must always be
// called; it is safe to call after a send has already consumed the file.
func rehydrateMultipartFile(path, field, mimeType string) (*multipart.FileHeader, func(), error) {
	noop := func() {}

	source, err := os.Open(path)
	if err != nil {
		return nil, noop, fmt.Errorf("failed to open queued media %s: %w", path, err)
	}

	if strings.TrimSpace(mimeType) == "" {
		mimeType = detectQueuedMediaMIME(path, source)
	}

	reader, writer := io.Pipe()
	formWriter := multipart.NewWriter(writer)

	go func() {
		// Own the source file here: ReadForm only returns once this goroutine has
		// finished writing, so closing it anywhere else would race the pipe.
		defer source.Close()

		var writeErr error
		defer func() {
			// Propagate the failure to ReadForm instead of letting it see a clean EOF.
			_ = writer.CloseWithError(writeErr)
		}()

		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(
			`form-data; name="%s"; filename="%s"`,
			escapeMultipartQuotes(field), escapeMultipartQuotes(filepath.Base(path))))
		header.Set("Content-Type", mimeType)

		part, err := formWriter.CreatePart(header)
		if err != nil {
			writeErr = err
			return
		}
		if _, err := io.Copy(part, source); err != nil {
			writeErr = err
			return
		}
		writeErr = formWriter.Close()
	}()

	form, err := multipart.NewReader(reader, formWriter.Boundary()).ReadForm(0)
	if err != nil {
		_ = reader.CloseWithError(err)
		return nil, noop, fmt.Errorf("failed to rebuild upload for queued media %s: %w", path, err)
	}
	cleanup := func() { _ = form.RemoveAll() }

	headers := form.File[field]
	if len(headers) == 0 || headers[0] == nil {
		cleanup()
		return nil, noop, fmt.Errorf("rebuilt upload for queued media %s has no %s part", path, field)
	}

	// Restore the name the caller originally uploaded, minus the UUID prefix
	// persistQueuedMedia added, so captions and document filenames look right.
	headers[0].Filename = originalQueuedFilename(headers[0].Filename)
	return headers[0], cleanup, nil
}

// detectQueuedMediaMIME recovers a Content-Type for a row that has none stored.
// The extension is tried first, then the leading bytes; the reader is rewound so
// the caller can still stream the whole file.
func detectQueuedMediaMIME(path string, source *os.File) string {
	if byExt := mime.TypeByExtension(filepath.Ext(path)); byExt != "" {
		// Drop any "; charset=..." suffix: the validators compare exact mimes.
		if parsed, _, err := mime.ParseMediaType(byExt); err == nil {
			return parsed
		}
		return byExt
	}

	sniff := make([]byte, 512)
	n, err := source.Read(sniff)
	if _, seekErr := source.Seek(0, io.SeekStart); seekErr != nil || (err != nil && n == 0) {
		return "application/octet-stream"
	}
	return http.DetectContentType(sniff[:n])
}

// escapeMultipartQuotes mirrors what mime/multipart does internally for
// Content-Disposition values, which is unexported.
func escapeMultipartQuotes(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, `"`, `\"`)
}

// originalQueuedFilename strips the "<uuid>-" prefix persistQueuedMedia adds.
// The prefix is a 36-character UUIDv4 followed by a hyphen; anything else is
// returned untouched.
func originalQueuedFilename(name string) string {
	const uuidLength = 36
	if len(name) > uuidLength+1 && name[uuidLength] == '-' {
		if candidate := strings.TrimSpace(name[uuidLength+1:]); candidate != "" {
			return candidate
		}
	}
	return name
}
