package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeJPEGSelection(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "A.JPG")
	second := filepath.Join(root, "中文 B.jpeg")
	for _, file := range []string{first, second} {
		if err := os.WriteFile(file, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := normalizeJPEGSelection([]string{second, first, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != first || files[1] != second {
		t.Fatalf("unexpected normalised files: %v", files)
	}
}

func TestValidateRepairIndices(t *testing.T) {
	results := []analysisResult{{State: stateConsistent}, {State: stateInitialResidue}, {State: stateAmbiguous}}
	indices, err := validateRepairIndices(results, []int{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(indices) != 1 || indices[0] != 1 {
		t.Fatalf("unexpected indices: %v", indices)
	}
	if _, err := validateRepairIndices(results, []int{2}); err == nil {
		t.Fatal("ambiguous file must not be accepted for repair")
	}
}

func TestGUIAppCancelScan(t *testing.T) {
	app := newGUIApp()
	ctx, err := app.beginScanOperation()
	if err != nil {
		t.Fatal(err)
	}
	if !app.CancelScan() {
		t.Fatal("active scan was not cancelled")
	}
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("unexpected context error: %v", ctx.Err())
		}
	default:
		t.Fatal("scan context is still active")
	}
	app.endOperation()
	if app.CancelScan() {
		t.Fatal("inactive scan reported as cancelled")
	}
}

func TestGUIProgressGateThrottlesIntermediateEvents(t *testing.T) {
	var gate guiProgressGate
	start := time.Unix(100, 0)
	indeterminate := GUIProgress{Phase: "scan", Done: 0, Total: 0}
	if !gate.allow(indeterminate, start) {
		t.Fatal("first progress event must be allowed")
	}
	if gate.allow(indeterminate, start.Add(50*time.Millisecond)) {
		t.Fatal("indeterminate progress should be throttled")
	}
	if !gate.allow(indeterminate, start.Add(guiProgressInterval)) {
		t.Fatal("progress should be allowed after the throttle interval")
	}

	working := GUIProgress{Phase: "scan", Done: 1, Total: 10}
	if !gate.allow(working, start.Add(guiProgressInterval+time.Millisecond)) {
		t.Fatal("a new total must be reported immediately")
	}
	if gate.allow(GUIProgress{Phase: "scan", Done: 2, Total: 10}, start.Add(guiProgressInterval+50*time.Millisecond)) {
		t.Fatal("intermediate progress should be throttled")
	}
	if !gate.allow(GUIProgress{Phase: "scan", Done: 10, Total: 10}, start.Add(guiProgressInterval+51*time.Millisecond)) {
		t.Fatal("the terminal progress event must not be throttled")
	}
}

func TestGUIAppShutdownCancelsScanAndDropsSession(t *testing.T) {
	app := newGUIApp()
	app.sessions["old"] = &guiSession{Results: []analysisResult{{File: "old.jpg"}}}
	ctx, err := app.beginScanOperation()
	if err != nil {
		t.Fatal(err)
	}

	app.shutdown(context.Background())
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("shutdown did not cancel scan: %v", ctx.Err())
	}
	if len(app.sessions) != 0 {
		t.Fatalf("shutdown retained scan sessions: %d", len(app.sessions))
	}
	if app.CancelScan() {
		t.Fatal("shutdown left an active scan cancellation handle")
	}
}

func TestGUIAppClosedDoesNotStartThumbnailSession(t *testing.T) {
	app := newGUIApp()
	app.shutdown(context.Background())

	if _, err := app.getThumbnailSession("missing-exiftool-after-shutdown"); err == nil || err.Error() != "应用已经关闭" {
		t.Fatalf("closed app attempted to create thumbnail session: %v", err)
	}
}

func TestBuildGUIScanReportPreallocatesFiles(t *testing.T) {
	results := []analysisResult{
		{File: "consistent.jpg", State: stateConsistent},
		{File: "candidate.jpg", State: stateInitialResidue},
		{File: "ambiguous.jpg", State: stateAmbiguous},
		{File: "unreadable.jpg", State: stateUnreadable},
	}
	report := buildGUIScanReport("session", ".", "exiftool", results)
	if len(report.Files) != len(results) || cap(report.Files) != len(results) {
		t.Fatalf("report files were not preallocated: len=%d cap=%d", len(report.Files), cap(report.Files))
	}
	if report.Summary != (GUISummary{Total: 4, Candidates: 1, Consistent: 1, Ambiguous: 1, Unreadable: 1}) {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
}

func TestGUIAppScanIntegration(t *testing.T) {
	root := os.Getenv("LRTIMEZONEFIX_GUI_TEST_ROOT")
	if root == "" {
		t.Skip("set LRTIMEZONEFIX_GUI_TEST_ROOT to run the ExifTool GUI integration test")
	}
	app := newGUIApp()
	defer app.shutdown(context.Background())
	report, err := app.Scan(GUISelection{Mode: "folder", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total != 1 || report.Summary.Candidates != 1 {
		t.Fatalf("unexpected report summary: %+v", report.Summary)
	}
	if got := report.Files[0].TargetOffset; got != "+09:00" {
		t.Fatalf("target offset=%s", got)
	}
}

func TestGetThumbnailIntegration(t *testing.T) {
	file := os.Getenv("LRTIMEZONEFIX_THUMBNAIL_TEST_FILE")
	if file == "" {
		t.Skip("set LRTIMEZONEFIX_THUMBNAIL_TEST_FILE to run the embedded thumbnail integration test")
	}
	app := newGUIApp()
	defer app.shutdown(context.Background())
	app.sessions["thumbnail-test"] = &guiSession{Results: []analysisResult{{File: file}}}
	thumbnail, err := app.GetThumbnail("thumbnail-test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(thumbnail, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected embedded thumbnail: %.40q", thumbnail)
	}
}
