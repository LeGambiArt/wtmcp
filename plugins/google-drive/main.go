// google-drive handler is a persistent plugin for Google Drive.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
)

var (
	driveSvc *drive.Service
	plug     *handler.Plugin
)

func main() {
	p := handler.New()
	plug = p

	p.OnInit(func(_ json.RawMessage) error {
		client := handler.NewProxyTransport(p).Client()
		svc, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
		if err != nil {
			return fmt.Errorf("drive service: %w", err)
		}
		driveSvc = svc

		return nil
	})

	p.Handle("drive_get_file_by_id", toolGetFileByID)
	p.Handle("drive_get_file_by_url", toolGetFileByURL)
	p.Handle("drive_extract_and_get_from_text", toolExtractAndGet)
	p.Handle("drive_export_google_doc_text", toolExportDocText)
	p.Handle("drive_export_google_sheet_csv", toolExportSheetCSV)
	p.Handle("drive_export_slides_pdf", toolExportSlidesPDF)
	p.Handle("drive_export_google_doc_markdown", toolExportDocMarkdown)
	p.Handle("drive_search_files", toolSearchFiles)
	p.Handle("drive_search_text", toolSearchText)
	p.Handle("drive_create_folder", toolCreateFolder)
	p.Handle("drive_upload_file", toolUploadFile)
	p.Handle("drive_rename_file", toolRenameFile)
	p.Handle("drive_copy_file", toolCopyFile)
	p.Handle("drive_delete_file", toolDeleteFile)

	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "handler: %v\n", err)
		os.Exit(1)
	}
}
