package mtproto

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

const (
	docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	pptxMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	xlsxMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	zipMIME  = "application/zip"
)

func verifyZIPPackage(file io.ReaderAt, size int64) error {
	if _, err := zip.NewReader(file, size); err != nil {
		return fmt.Errorf("invalid ZIP archive: %w", err)
	}
	return nil
}

func verifyXLSXPackage(file io.ReaderAt, size int64) error {
	return verifyOfficePackage(file, size, "XLSX", "xl/workbook.xml", xlsxMIME+".main+xml", "workbook")
}

func verifyDOCXPackage(file io.ReaderAt, size int64) error {
	return verifyOfficePackage(file, size, "DOCX", "word/document.xml", docxMIME+".main+xml", "document")
}

func verifyPPTXPackage(file io.ReaderAt, size int64) error {
	return verifyOfficePackage(file, size, "PPTX", "ppt/presentation.xml", pptxMIME+".main+xml", "presentation")
}

func verifyOfficePackage(file io.ReaderAt, size int64, format, partPath, partMIME, partName string) error {
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return fmt.Errorf("invalid %s archive: %w", format, err)
	}
	types, err := archive.Open("[Content_Types].xml")
	if err != nil {
		return fmt.Errorf("missing %s content types: %w", format, err)
	}
	defer types.Close()
	var content struct {
		XMLName xml.Name `xml:"http://schemas.openxmlformats.org/package/2006/content-types Types"`
		Default []struct {
			Extension   string `xml:"Extension,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Default"`
		Override []struct {
			PartName    string `xml:"PartName,attr"`
			ContentType string `xml:"ContentType,attr"`
		} `xml:"Override"`
	}
	if err = xml.NewDecoder(io.LimitReader(types, 1<<20)).Decode(&content); err != nil {
		return fmt.Errorf("invalid %s content types: %w", format, err)
	}
	partOverride := false
	for _, part := range content.Override {
		name, absolute := strings.CutPrefix(part.PartName, "/")
		if absolute {
			partOverride = partOverride || name == partPath
			if part.ContentType == partMIME && nonemptyZipFile(archive, name) {
				return nil
			}
		}
	}
	if !partOverride {
		for _, entry := range content.Default {
			if strings.EqualFold(entry.Extension, "xml") && entry.ContentType == partMIME && nonemptyZipFile(archive, partPath) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s %s part missing", format, partName)
}

func nonemptyZipFile(archive *zip.Reader, name string) bool {
	info, err := fs.Stat(archive, name)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// Stream uses one worker; never permit server bytes beyond the reviewed file size.
type boundedPublicationWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedPublicationWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("server file exceeds reviewed size")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func (s *Session) downloadPublishedDocument(ctx context.Context, document *tg.Document, output io.Writer, operation string) error {
	if s.client == nil {
		return fmt.Errorf("publication file downloader is unavailable")
	}
	// The CLI owns the session/runtime lock and runs no concurrent history.
	coordinator := newDownloadCoordinator(nil, func(ctx context.Context) error {
		return s.beforeRPC(ctx, "download_"+operation+"_file_readback")
	})
	defer coordinator.finish()
	var downloadErr error
	task := &mediaDownloadTask{Slots: 1,
		Transfer: func(ctx context.Context) {
			downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
			defer cancel()
			_, downloadErr = s.client.Download(document.AsInputDocumentFileLocation("")).WithThreads(1).
				WithRetryHandler(func(event downloader.RetryEvent) {
					s.observeDownloadError("download_"+operation+"_file_retry", event.Err, nil, nil)
				}).
				Stream(downloadCtx, output)
		},
		Fail: func(err error) { downloadErr = err }, Cancel: func(err error) { downloadErr = err },
	}
	runErr := coordinator.runBatch(ctx, []*mediaDownloadTask{task}, nil)
	return errors.Join(runErr, downloadErr)
}
