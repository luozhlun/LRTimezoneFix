package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindJPEGsSkipsDevelopmentAndBackupDirectories(t *testing.T) {
	root := t.TempDir()
	visible := filepath.Join(root, "photos", "visible.jpg")
	hidden := filepath.Join(root, ".test", "hidden.jpg")
	backup := filepath.Join(root, "photos", "ExifTool_Backup_20260816_120000", "backup.jpg")
	for _, file := range []string{visible, hidden, backup} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := findJPEGs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != visible {
		t.Fatalf("unexpected files: %v", files)
	}
}

func baseTestMetadata() metadata {
	return metadata{
		DateTimeOriginal:       "2025:10:01 13:39:15",
		CreateDate:             "2025:10:01 12:39:15",
		SubSecDateTimeOriginal: "2025:10:01 13:39:15.65+08:00",
		SubSecCreateDate:       "2025:10:01 12:39:15.65+08:00",
		OffsetTimeOriginal:     "+08:00",
		OffsetTimeDigitized:    "+08:00",
		CreatorTool:            "Adobe Photoshop Lightroom Classic 15.4.1 (Windows)",
		IPTCDigest:             "aaaa",
		CurrentIPTCDigest:      "aaaa",
	}
}

func TestAnalyzeInitialResidue(t *testing.T) {
	result := analyzeMetadata("test.jpg", baseTestMetadata())
	if result.State != stateInitialResidue {
		t.Fatalf("state=%v reason=%s", result.State, result.Reason)
	}
	if result.TargetLocal != "2025:10:01 13:39:15.65" || result.TargetOffset != "+09:00" || result.ShiftMinutes != 60 {
		t.Fatalf("unexpected inference: %+v", result)
	}
}

func TestAnalyzeIgnoresSubsecondRoundingDifference(t *testing.T) {
	m := baseTestMetadata()
	m.SubSecDateTimeOriginal = "2025:10:01 13:39:15.809+08:00"
	m.SubSecCreateDate = "2025:10:01 12:39:15.81+08:00"
	result := analyzeMetadata("test.jpg", m)
	if result.State != stateInitialResidue {
		t.Fatalf("state=%v reason=%s", result.State, result.Reason)
	}
	if result.TargetLocal != "2025:10:01 13:39:15.809" {
		t.Fatalf("DateTimeOriginal precision was not preserved: %s", result.TargetLocal)
	}
}

func TestAnalyzeConsistent(t *testing.T) {
	m := baseTestMetadata()
	m.CreateDate = m.DateTimeOriginal
	m.SubSecCreateDate = "2025:10:01 13:39:15.65+09:00"
	m.SubSecDateTimeOriginal = "2025:10:01 13:39:15.65+09:00"
	m.OffsetTimeOriginal = "+09:00"
	m.OffsetTimeDigitized = "+09:00"
	result := analyzeMetadata("test.jpg", m)
	if result.State != stateConsistent {
		t.Fatalf("state=%v reason=%s", result.State, result.Reason)
	}
}

func TestAnalyzeCrossesMidnightFromCentralUSAToParis(t *testing.T) {
	m := baseTestMetadata()
	m.DateTimeOriginal = "2024:12:22 00:07:31"
	m.CreateDate = "2024:12:21 17:07:31"
	m.SubSecDateTimeOriginal = "2024:12:22 00:07:31.42-06:00"
	m.SubSecCreateDate = "2024:12:21 17:07:31.42-06:00"
	m.OffsetTimeOriginal = "-06:00"
	m.OffsetTimeDigitized = "-06:00"

	result := analyzeMetadata("test.jpg", m)
	if result.State != stateInitialResidue {
		t.Fatalf("state=%v reason=%s", result.State, result.Reason)
	}
	if result.TargetLocal != "2024:12:22 00:07:31.42" || result.TargetOffset != "+01:00" || result.ShiftMinutes != 420 {
		t.Fatalf("unexpected inference: %+v", result)
	}
}

func TestAnalyzeNegativeShiftFromChinaToParis(t *testing.T) {
	m := baseTestMetadata()
	m.DateTimeOriginal = "2025:01:14 12:00:42"
	m.CreateDate = "2025:01:14 19:00:42"
	m.SubSecDateTimeOriginal = "2025:01:14 12:00:42.7+08:00"
	m.SubSecCreateDate = "2025:01:14 19:00:42.7+08:00"

	result := analyzeMetadata("test.jpg", m)
	if result.State != stateInitialResidue {
		t.Fatalf("state=%v reason=%s", result.State, result.Reason)
	}
	if result.TargetLocal != "2025:01:14 12:00:42.7" || result.TargetOffset != "+01:00" || result.ShiftMinutes != -420 {
		t.Fatalf("unexpected inference: %+v", result)
	}
}

func TestInconsistentOffsetsAreAmbiguous(t *testing.T) {
	m := baseTestMetadata()
	m.OffsetTimeOriginal = "+09:00"
	m.OffsetTimeDigitized = "+08:00"

	result := analyzeMetadata("test.jpg", m)
	if result.State != stateAmbiguous || result.Repairable() {
		t.Fatalf("inconsistent offsets must be skipped: %+v", result)
	}
}

func TestAuditMarkerPreservesExistingComment(t *testing.T) {
	candidate := analysisResult{SourceOffset: "+08:00", TargetOffset: "+09:00", ShiftMinutes: 60}
	marker := buildAuditMarker(candidate, time.Date(2026, 8, 16, 12, 44, 37, 0, time.FixedZone("CST", 8*3600)))
	if !strings.HasPrefix(marker, auditPrefix) || !strings.Contains(marker, "utc-preserved=yes") {
		t.Fatalf("bad marker: %s", marker)
	}
	combined := appendAuditMarker("existing user note", marker)
	if !strings.HasPrefix(combined, "existing user note\n") {
		t.Fatalf("existing comment was not preserved: %q", combined)
	}
	if appendAuditMarker(combined, marker) != combined {
		t.Fatal("marker was duplicated")
	}
}

func TestParseExifDate(t *testing.T) {
	parsed, err := parseExifDate("2025:10:01 13:39:15.650+09:00")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Local != "2025:10:01 13:39:15.650" || parsed.Time.Nanosecond() != 650000000 {
		t.Fatalf("unexpected parse: %+v", parsed)
	}
}
