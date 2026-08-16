package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

func TestGUIAppScanIntegration(t *testing.T) {
	root := os.Getenv("LRTIMEZONEFIX_GUI_TEST_ROOT")
	if root == "" {
		t.Skip("set LRTIMEZONEFIX_GUI_TEST_ROOT to run the ExifTool GUI integration test")
	}
	app := newGUIApp()
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
