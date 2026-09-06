package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

func isXMP(file string) bool {
	return strings.EqualFold(filepath.Ext(file), ".xmp")
}

func normalizeXMPOffset(offset string) string {
	if offset == "Z" {
		return "+00:00"
	}
	return offset
}

func verifyXMPRepair(before analysisResult, after metadata) error {
	expected, err := parseExifDate(before.TargetLocal)
	if err != nil {
		return err
	}
	for _, value := range []string{after.XMPDateTimeOriginal, after.XMPCreateDate, after.XMPDateCreated} {
		date, err := parseExifDate(value)
		if err != nil || date.Local != expected.Local || normalizeXMPOffset(date.RawOffset) != before.TargetOffset {
			return fmt.Errorf("验证失败：XMP 三个拍摄时间字段未统一为 %s%s", before.TargetLocal, before.TargetOffset)
		}
	}
	if before.Meta.XMPModifyDate != after.XMPModifyDate || before.Meta.MetadataDate != after.MetadataDate || before.Meta.HistoryWhen != after.HistoryWhen || before.Meta.GPSDateTime != after.GPSDateTime {
		return fmt.Errorf("验证失败：XMP 修改/历史时间或 GPS 时间发生变化")
	}
	return nil
}
