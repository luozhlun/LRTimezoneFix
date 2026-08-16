package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupLogRecordsHeaderEntriesAndResult(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "ExifTool_Backup_20260816_180000")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := analysisResult{
		SourceOffset: "+08:00",
		TargetOffset: "+09:00",
		ShiftMinutes: 60,
		TargetLocal:  "2025:10:01 13:39:15",
	}
	batchTime := time.Date(2026, 8, 16, 18, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for _, name := range []string{"first.jpg", "second.jpg"} {
		source := filepath.Join(root, name)
		backup := filepath.Join(backupDir, name)
		hash := sha256.Sum256([]byte(name))
		if err := appendBackupLogEntry(backupDir, source, backup, hash, candidate, batchTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendBackupLogResult(backupDir, filepath.Join(root, "first.jpg"), "REPAIR_VERIFIED", "验证通过", batchTime); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(backupDir, "LRTimezoneFix_Backup.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "LRTimezoneFix Backup Log") != 1 || strings.Count(text, "[Backup]") != 2 {
		t.Fatalf("unexpected log structure:\n%s", text)
	}
	for _, expected := range []string{"ToolVersion=" + version, "SourceOffset=+08:00", "TargetOffset=+09:00", "Status=REPAIR_VERIFIED"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("backup log is missing %q:\n%s", expected, text)
		}
	}
}
