// Sprag - a post-quantum-safe end-to-end encrypted file dropbox.
// Copyright (C) 2026 Tobias von Dewitz <tobias@vondewitz.org>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/elcamino/sprag/internal/store"
)

func TestSQLiteStoreCreatesPagesAndAggregatesUploads(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:        "abc123abc123abc1",
		Title:       "Client drop",
		Description: "PDFs only",
		AllowedExt:  "pdf",
		MaxFileSize: ptrInt64(1024),
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}

	submissionID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/abc/file-1/report.pdf",
		OriginalName: "report.pdf",
		SizeBytes:    12,
		ContentType:  "application/pdf",
		UploaderIP:   "203.0.113.7",
		SubmissionID: submissionID,
		ObjectSHA512: "hash-one",
	}); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}
	if _, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/abc/file-2/scan.pdf",
		OriginalName: "scan.pdf",
		SizeBytes:    34,
		ContentType:  "application/pdf",
		UploaderIP:   "203.0.113.7",
		SubmissionID: submissionID,
		ObjectSHA512: "hash-two",
	}); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	pages, err := db.ListPages(ctx)
	if err != nil {
		t.Fatalf("ListPages failed: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected one page, got %d", len(pages))
	}
	if pages[0].UploadCount != 2 || pages[0].TotalBytes != 46 {
		t.Fatalf("unexpected upload aggregate: count=%d bytes=%d", pages[0].UploadCount, pages[0].TotalBytes)
	}

	files, err := db.ListUploads(ctx, page.ID)
	if err != nil {
		t.Fatalf("ListUploads failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected two files, got %d", len(files))
	}
	for _, file := range files {
		if file.SubmissionID != submissionID {
			t.Fatalf("file %q submission id = %q, want %q", file.OriginalName, file.SubmissionID, submissionID)
		}
		if file.SubmissionUploadedAt == nil {
			t.Fatalf("file %q missing submission upload time", file.OriginalName)
		}
		if file.ObjectHashAlgorithm != "SHA-512" || file.ObjectSHA512 == "" {
			t.Fatalf("file %q missing stored object hash: algorithm=%q hash=%q", file.OriginalName, file.ObjectHashAlgorithm, file.ObjectSHA512)
		}
	}
	if !files[0].UploadedAt.After(time.Time{}) {
		t.Fatal("expected uploaded_at to be populated")
	}
}

func TestDeleteSubmissionRemovesEnvelopeAndReturnsBlobKeys(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "delpageslug000",
		Title: "Bulk delete",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}

	target := "88888888-8888-4888-8888-888888888888"
	for _, s3 := range []string{"a/pdf-one", "b/pdf-two"} {
		if _, err := db.CreateUpload(ctx, store.UploadCreate{
			PageID:       page.ID,
			S3Key:        s3,
			OriginalName: s3,
			SizeBytes:    1,
			SubmissionID: target,
		}); err != nil {
			t.Fatalf("CreateUpload failed: %v", err)
		}
	}
	if _, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "c/keep",
		OriginalName: "keep.pdf",
		SizeBytes:    1,
		SubmissionID: "99999999-9999-4999-9999-999999999999",
	}); err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	keys, reportKey, err := db.DeleteSubmission(ctx, page.ID, target)
	if err != nil {
		t.Fatalf("DeleteSubmission failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("DeleteSubmission returned %d keys, want 2", len(keys))
	}
	if reportKey != "" {
		t.Fatalf("DeleteSubmission report key = %q, want empty", reportKey)
	}

	files, err := db.ListUploads(ctx, page.ID)
	if err != nil {
		t.Fatalf("ListUploads failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 remaining upload, got %d", len(files))
	}
	if files[0].S3Key != "c/keep" {
		t.Fatalf("wrong upload survived: %s", files[0].S3Key)
	}

	// The envelope is gone, so a second delete is a clean not-found.
	if _, _, err := db.DeleteSubmission(ctx, page.ID, target); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteSubmission err = %v, want ErrNotFound", err)
	}

	// The backup submission is untouched.
	if keep, err := db.GetSubmissionEnvelope(ctx, page.ID, "99999999-9999-4999-9999-999999999999"); err != nil {
		t.Fatalf("unrelated submission envelope missing: %v", err)
	} else if keep.ID == 0 {
		t.Fatalf("unrelated submission envelope has id 0")
	}
}


// CreateUpload performs three writes (submission envelope, upload row, custody
// event). A failure partway through must leave no partial state behind: an
// upload row without its upload.accepted custody event would silently break the
// custody-chain guarantee, and an orphaned envelope would surface as a phantom
// submission. The custody_events table is dropped out from under the store to
// force the third write to fail deterministically.
func TestCreateUploadIsAtomicWhenCustodyEventInsertFails(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sprag.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "atomicslug000001",
		Title: "Atomicity check",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "DROP TABLE custody_events"); err != nil {
		t.Fatalf("drop custody_events: %v", err)
	}

	submissionID := "33333333-3333-4333-8333-333333333333"
	if _, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/atomic/file-1/report.pdf",
		OriginalName: "report.pdf",
		SizeBytes:    12,
		SubmissionID: submissionID,
	}); err == nil {
		t.Fatal("CreateUpload succeeded despite missing custody_events table")
	}

	uploads, err := db.ListUploads(ctx, page.ID)
	if err != nil {
		t.Fatalf("ListUploads failed: %v", err)
	}
	if len(uploads) != 0 {
		t.Fatalf("upload row survived failed CreateUpload: %#v", uploads)
	}
	if _, err := db.GetSubmissionEnvelope(ctx, page.ID, submissionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSubmissionEnvelope after failed CreateUpload = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreCreatesReceiptForSubmissionEnvelope(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "receipt-page-1",
		Title: "Receipt intake",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}

	submissionID := "33333333-3333-4333-8333-333333333333"
	first, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/receipt/file-1/one.pdf",
		OriginalName: "one.pdf",
		SizeBytes:    12,
		SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("CreateUpload first failed: %v", err)
	}
	second, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/receipt/file-2/two.pdf",
		OriginalName: "two.pdf",
		SizeBytes:    34,
		SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("CreateUpload second failed: %v", err)
	}
	if first.ReceiptToken == "" {
		t.Fatal("first upload missing receipt token")
	}
	if first.ReceiptToken != second.ReceiptToken {
		t.Fatalf("uploads in one submission got different receipt tokens: %q vs %q", first.ReceiptToken, second.ReceiptToken)
	}
	if first.ReceiptStatus != "received" || second.ReceiptStatus != "received" {
		t.Fatalf("receipt statuses = %q/%q, want received", first.ReceiptStatus, second.ReceiptStatus)
	}

	receipt, err := db.GetReceipt(ctx, first.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Status != "received" {
		t.Fatalf("receipt status = %q, want received", receipt.Status)
	}
	if receipt.FileCount != 2 || receipt.TotalBytes != 46 {
		t.Fatalf("receipt aggregate = %d files/%d bytes, want 2 files/46 bytes", receipt.FileCount, receipt.TotalBytes)
	}
	if receipt.SubmittedAt.IsZero() || receipt.UpdatedAt.IsZero() {
		t.Fatalf("receipt times were not populated: submitted=%v updated=%v", receipt.SubmittedAt, receipt.UpdatedAt)
	}
}

func TestSQLiteStoreUpdatesReceiptStatus(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "receipt-status-1",
		Title: "Receipt status",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}
	created, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/receipt/file/report.pdf",
		OriginalName: "report.pdf",
		SizeBytes:    12,
		SubmissionID: "44444444-4444-4444-8444-444444444444",
	})
	if err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	updated, err := db.UpdateReceiptStatus(ctx, page.ID, created.SubmissionID, "reviewed")
	if err != nil {
		t.Fatalf("UpdateReceiptStatus failed: %v", err)
	}
	if updated.ReceiptStatus != "reviewed" {
		t.Fatalf("updated receipt status = %q, want reviewed", updated.ReceiptStatus)
	}
	receipt, err := db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Status != "reviewed" {
		t.Fatalf("public receipt status = %q, want reviewed", receipt.Status)
	}

	if _, err := db.UpdateReceiptStatus(ctx, page.ID, created.SubmissionID, "chatting"); err == nil {
		t.Fatal("expected invalid receipt status to fail")
	}
}

func TestSQLiteStoreSealsPageOnce(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "seal-page-1",
		Title: "Seal page",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}
	if page.SealedAt != nil {
		t.Fatalf("new page sealed_at = %v, want nil", page.SealedAt)
	}

	sealed, err := db.SealPage(ctx, page.ID)
	if err != nil {
		t.Fatalf("SealPage failed: %v", err)
	}
	if sealed.SealedAt == nil || sealed.IsActive {
		t.Fatalf("sealed page = %#v, want sealed_at and inactive", sealed)
	}
	firstSealedAt := *sealed.SealedAt

	again, err := db.SealPage(ctx, page.ID)
	if err != nil {
		t.Fatalf("second SealPage failed: %v", err)
	}
	if again.SealedAt == nil || !again.SealedAt.Equal(firstSealedAt) {
		t.Fatalf("second SealPage changed sealed_at from %v to %v", firstSealedAt, again.SealedAt)
	}

	pages, err := db.ListPages(ctx)
	if err != nil {
		t.Fatalf("ListPages failed: %v", err)
	}
	if len(pages) != 1 || pages[0].SealedAt == nil {
		t.Fatalf("ListPages missing sealed_at: %#v", pages)
	}
}

func TestSQLiteStoreRejectsDuplicateSlugs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	create := store.PageCreate{Slug: "same-slug-same-1", Title: "One"}
	if _, err := db.CreatePage(ctx, create); err != nil {
		t.Fatalf("first CreatePage failed: %v", err)
	}
	_, err = db.CreatePage(ctx, create)
	if !errors.Is(err, store.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestSQLiteStoreMigratesLegacyUploadsIntoSubmissionEnvelopes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sprag.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite failed: %v", err)
	}
	_, err = raw.Exec(`
CREATE TABLE pages (
  id INTEGER PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  description TEXT,
  pin_hash TEXT,
  max_file_size INTEGER,
  allowed_ext TEXT,
  expires_at TEXT,
  is_active INTEGER NOT NULL DEFAULT 1,
  e2e_enabled INTEGER NOT NULL DEFAULT 0,
  e2e_algorithm TEXT,
  e2e_public_key TEXT,
  e2e_public_key_fingerprint TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE uploads (
  id INTEGER PRIMARY KEY,
  page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  s3_key TEXT NOT NULL,
  original_name TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  content_type TEXT,
  uploader_ip TEXT,
  encryption_mode TEXT,
  encryption_algorithm TEXT,
  encryption_envelope TEXT,
  uploaded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO pages (id, slug, title) VALUES (1, 'legacy-legacy-1', 'Legacy');
INSERT INTO uploads (id, page_id, s3_key, original_name, size_bytes, uploader_ip, uploaded_at)
VALUES (9, 1, 'pages/legacy/file/report.pdf', 'report.pdf', 12, '203.0.113.9', '2026-06-17T10:00:00Z');
`)
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw sqlite failed: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("seed legacy schema failed: %v", err)
	}

	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	files, err := db.ListUploads(ctx, 1)
	if err != nil {
		t.Fatalf("ListUploads failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	if files[0].SubmissionID != "legacy-9" {
		t.Fatalf("submission id = %q, want legacy-9", files[0].SubmissionID)
	}
	if files[0].SubmissionUploadedAt == nil || !files[0].SubmissionUploadedAt.Equal(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected submission upload time: %v", files[0].SubmissionUploadedAt)
	}
	if files[0].ReceiptToken == "" {
		t.Fatal("legacy upload missing backfilled receipt token")
	}
	receipt, err := db.GetReceipt(ctx, files[0].ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt for backfilled legacy token failed: %v", err)
	}
	if receipt.Status != "received" || receipt.FileCount != 1 || receipt.TotalBytes != 12 {
		t.Fatalf("legacy receipt = %#v, want received/1 file/12 bytes", receipt)
	}
}

func TestSQLiteStoreReportLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "report-page-1",
		Title: "Report intake",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}
	submissionID := "66666666-6666-4666-8666-666666666666"
	created, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/report/file/one.pdf",
		OriginalName: "one.pdf",
		SizeBytes:    12,
		SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	// No report yet: receipt has none, upload list carries none.
	receipt, err := db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Report != nil {
		t.Fatalf("receipt report = %#v, want nil", receipt.Report)
	}
	uploads, err := db.ListUploads(ctx, page.ID)
	if err != nil {
		t.Fatalf("ListUploads failed: %v", err)
	}
	if len(uploads) != 1 || uploads[0].Report != nil {
		t.Fatalf("upload report = %#v, want nil", uploads[0].Report)
	}

	// Attach a report; the receipt and upload list now carry it.
	report, oldKey, err := db.PutReport(ctx, page.ID, submissionID, store.ReportCreate{
		S3Key:        "pages/report/report-1/result.pdf",
		OriginalName: "result.pdf",
		SizeBytes:    99,
		ContentType:  "application/pdf",
	})
	if err != nil {
		t.Fatalf("PutReport failed: %v", err)
	}
	if oldKey != "" {
		t.Fatalf("first PutReport old key = %q, want empty", oldKey)
	}
	if report.OriginalName != "result.pdf" || report.SizeBytes != 99 {
		t.Fatalf("unexpected report: %#v", report)
	}

	receipt, err = db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Report == nil || receipt.Report.OriginalName != "result.pdf" || receipt.Report.SizeBytes != 99 {
		t.Fatalf("receipt report = %#v, want result.pdf/99 bytes", receipt.Report)
	}
	if receipt.FileCount != 1 || receipt.TotalBytes != 12 {
		t.Fatalf("receipt aggregate changed with report: %d files/%d bytes", receipt.FileCount, receipt.TotalBytes)
	}

	byToken, err := db.GetReportByToken(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReportByToken failed: %v", err)
	}
	if byToken.S3Key != "pages/report/report-1/result.pdf" || byToken.PageID != page.ID {
		t.Fatalf("unexpected report by token: %#v", byToken)
	}
	byID, err := db.GetReport(ctx, page.ID, submissionID)
	if err != nil {
		t.Fatalf("GetReport failed: %v", err)
	}
	if byID.ID != byToken.ID {
		t.Fatalf("GetReport id = %d, GetReportByToken id = %d", byID.ID, byToken.ID)
	}

	// Replace: the old key comes back.
	replaced, oldKey, err := db.PutReport(ctx, page.ID, submissionID, store.ReportCreate{
		S3Key:        "pages/report/report-2/result-v2.pdf",
		OriginalName: "result-v2.pdf",
		SizeBytes:    120,
	})
	if err != nil {
		t.Fatalf("PutReport replace failed: %v", err)
	}
	if oldKey != "pages/report/report-1/result.pdf" {
		t.Fatalf("replace old key = %q, want the first key", oldKey)
	}
	if replaced.ID != report.ID {
		t.Fatalf("replace changed report id from %d to %d", report.ID, replaced.ID)
	}

	// Delete: the key comes back and the receipt loses the report.
	deletedKey, err := db.DeleteReport(ctx, page.ID, submissionID)
	if err != nil {
		t.Fatalf("DeleteReport failed: %v", err)
	}
	if deletedKey != "pages/report/report-2/result-v2.pdf" {
		t.Fatalf("DeleteReport key = %q, want the second key", deletedKey)
	}
	if _, err := db.DeleteReport(ctx, page.ID, submissionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteReport err = %v, want ErrNotFound", err)
	}
	receipt, err = db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Report != nil {
		t.Fatalf("receipt report after delete = %#v, want nil", receipt.Report)
	}
	if _, err := db.GetReportByToken(ctx, created.ReceiptToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetReportByToken after delete err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreReportCascadesWithSubmission(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "report-cascade-1",
		Title: "Report cascade",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}
	submissionID := "77777777-7777-4777-8777-777777777777"
	created, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/cascade/file/one.pdf",
		OriginalName: "one.pdf",
		SizeBytes:    12,
		SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}
	if _, _, err := db.PutReport(ctx, page.ID, submissionID, store.ReportCreate{
		S3Key:        "pages/cascade/report/result.pdf",
		OriginalName: "result.pdf",
		SizeBytes:    99,
	}); err != nil {
		t.Fatalf("PutReport failed: %v", err)
	}

	keys, reportKey, err := db.DeleteSubmission(ctx, page.ID, submissionID)
	if err != nil {
		t.Fatalf("DeleteSubmission failed: %v", err)
	}
	if len(keys) != 1 || reportKey != "pages/cascade/report/result.pdf" {
		t.Fatalf("DeleteSubmission keys = %v report = %q", keys, reportKey)
	}
	if _, err := db.GetReportByToken(ctx, created.ReceiptToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("report survived submission delete: %v", err)
	}
	if _, err := db.GetReceipt(ctx, created.ReceiptToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("receipt survived submission delete: %v", err)
	}
}

func TestSQLiteStoreReportStatusFlipsWithAttachment(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sprag.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	page, err := db.CreatePage(ctx, store.PageCreate{
		Slug:  "report-status-1",
		Title: "Report status",
	})
	if err != nil {
		t.Fatalf("CreatePage failed: %v", err)
	}
	submissionID := "88888888-8888-4888-8888-888888888888"
	created, err := db.CreateUpload(ctx, store.UploadCreate{
		PageID:       page.ID,
		S3Key:        "pages/status/file/one.pdf",
		OriginalName: "one.pdf",
		SizeBytes:    12,
		SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("CreateUpload failed: %v", err)
	}

	if _, err := db.UpdateReceiptStatus(ctx, page.ID, submissionID, store.ReceiptStatusCompleted); err != nil {
		t.Fatalf("UpdateReceiptStatus completed failed: %v", err)
	}
	receipt, err := db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Status != store.ReceiptStatusCompleted {
		t.Fatalf("receipt status = %q, want completed", receipt.Status)
	}

	if _, err := db.UpdateReceiptStatus(ctx, page.ID, submissionID, store.ReceiptStatusReceived); err != nil {
		t.Fatalf("UpdateReceiptStatus received failed: %v", err)
	}
	receipt, err = db.GetReceipt(ctx, created.ReceiptToken)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if receipt.Status != store.ReceiptStatusReceived {
		t.Fatalf("receipt status = %q, want received", receipt.Status)
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}
