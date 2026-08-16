package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func repairFile(exifTool string, candidate *analysisResult, stamp string, repairedAt time.Time) (err error) {
	file := candidate.File
	backupDir := filepath.Join(filepath.Dir(file), backupPrefix+stamp)
	backupPath := filepath.Join(backupDir, filepath.Base(file))

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("无法创建备份目录：%w", err)
	}
	if _, statErr := os.Stat(backupPath); statErr == nil {
		return fmt.Errorf("备份文件已存在，拒绝覆盖：%s", backupPath)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("无法检查备份路径：%w", statErr)
	}

	sourceHash, err := fileSHA256(file)
	if err != nil {
		return fmt.Errorf("无法计算原文件 SHA-256：%w", err)
	}
	if err := copyFileExact(file, backupPath); err != nil {
		return fmt.Errorf("备份失败：%w", err)
	}
	backupHash, err := fileSHA256(backupPath)
	if err != nil || sourceHash != backupHash {
		return fmt.Errorf("备份 SHA-256 验证失败")
	}

	restoreNeeded := true
	defer func() {
		if !restoreNeeded || err == nil {
			return
		}
		if restoreErr := copyFileExact(backupPath, file); restoreErr != nil {
			err = fmt.Errorf("%v；自动恢复也失败：%w；请手动使用备份 %s", err, restoreErr, backupPath)
			return
		}
		restoredHash, hashErr := fileSHA256(file)
		if hashErr != nil || restoredHash != sourceHash {
			err = fmt.Errorf("%v；自动恢复后的 SHA-256 验证失败；请手动使用备份 %s", err, backupPath)
		}
	}()

	marker := buildAuditMarker(*candidate, repairedAt)
	comment := appendAuditMarker(candidate.Meta.UserComment, marker)
	target := candidate.TargetLocal + candidate.TargetOffset
	args := []string{
		"-overwrite_original_in_place",
		"-P",
		"-use", "MWG",
		"-MWG:DateTimeOriginal=" + target,
		"-MWG:CreateDate=" + target,
		"-EXIF:UserComment=" + comment,
		"-Photoshop:IPTCDigest=new",
	}
	_, stderr, writeErr := runExifToolForFiles(exifTool, filepath.Dir(file), []string{filepath.Base(file)}, args...)
	if writeErr != nil {
		return fmt.Errorf("ExifTool 写入失败：%v；%s", writeErr, strings.TrimSpace(stderr))
	}

	after, err := readMetadata(exifTool, file)
	if err != nil {
		return fmt.Errorf("写后读取失败：%w", err)
	}
	if err := verifyRepair(*candidate, after, marker); err != nil {
		return err
	}

	restoreNeeded = false
	return nil
}

func buildAuditMarker(candidate analysisResult, repairedAt time.Time) string {
	return fmt.Sprintf(
		"%s action=%s; from=%s; to=%s; wall-shift=%s; utc-preserved=yes; normalized=DateTimeOriginal,CreateDate; version=%s; repaired-at=%s",
		auditPrefix,
		defaultMarker,
		candidate.SourceOffset,
		candidate.TargetOffset,
		formatSignedMinutes(candidate.ShiftMinutes),
		version,
		repairedAt.Format("2006-01-02T15:04:05Z07:00"),
	)
}

func appendAuditMarker(existing, marker string) string {
	if strings.Contains(existing, auditPrefix) {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return marker
	}
	return strings.TrimRight(existing, "\r\n") + "\n" + marker
}

func formatSignedMinutes(minutes int) string {
	sign := "+"
	if minutes < 0 {
		sign = "-"
		minutes = -minutes
	}
	return fmt.Sprintf("%s%02d:%02d", sign, minutes/60, minutes%60)
}

func verifyRepair(before analysisResult, after metadata, marker string) error {
	dto, err := parseExifDate(firstNonEmpty(after.SubSecDateTimeOriginal, after.DateTimeOriginal))
	if err != nil {
		return fmt.Errorf("验证失败：%w", err)
	}
	create, err := parseExifDate(firstNonEmpty(after.SubSecCreateDate, after.CreateDate))
	if err != nil {
		return fmt.Errorf("验证失败：%w", err)
	}
	expected, err := parseExifDate(before.TargetLocal)
	if err != nil {
		return fmt.Errorf("内部目标日期无效：%w", err)
	}
	if !dto.Time.Equal(expected.Time) || !create.Time.Equal(expected.Time) {
		return fmt.Errorf("验证失败：DateTimeOriginal/CreateDate 没有统一为 %s", before.TargetLocal)
	}
	if after.OffsetTimeOriginal != before.TargetOffset || after.OffsetTimeDigitized != before.TargetOffset {
		return fmt.Errorf("验证失败：时区不是预期值 %s（实际 %s / %s）", before.TargetOffset, after.OffsetTimeOriginal, after.OffsetTimeDigitized)
	}
	if !strings.HasSuffix(after.XMPDateCreated, before.TargetOffset) || !strings.HasSuffix(after.XMPCreateDate, before.TargetOffset) {
		return fmt.Errorf("验证失败：XMP 拍摄/创建时间时区未同步")
	}
	if !strings.HasSuffix(after.IPTCTimeCreated, before.TargetOffset) || !strings.HasSuffix(after.IPTCDigitalTime, before.TargetOffset) {
		return fmt.Errorf("验证失败：IPTC 拍摄/数字化时间时区未同步")
	}
	if after.IPTCDigest == "" || after.CurrentIPTCDigest == "" || !strings.EqualFold(after.IPTCDigest, after.CurrentIPTCDigest) {
		return fmt.Errorf("验证失败：IPTCDigest 与 CurrentIPTCDigest 不一致")
	}
	if !strings.Contains(after.UserComment, marker) && !strings.Contains(after.UserComment, auditPrefix) {
		return fmt.Errorf("验证失败：没有读回 %s 审计标记", auditPrefix)
	}
	if before.Meta.ImageDataHash == "" || after.ImageDataHash == "" || before.Meta.ImageDataHash != after.ImageDataHash {
		return fmt.Errorf("验证失败：图像数据哈希发生变化")
	}
	if before.Meta.ModifyDate != after.ModifyDate || before.Meta.OffsetTime != after.OffsetTime {
		return fmt.Errorf("验证失败：不应修改的 ModifyDate/OffsetTime 发生变化")
	}
	if before.Meta.XMPModifyDate != after.XMPModifyDate || before.Meta.MetadataDate != after.MetadataDate || before.Meta.HistoryWhen != after.HistoryWhen {
		return fmt.Errorf("验证失败：不应修改的 XMP 修改/历史时间发生变化")
	}
	if before.Meta.GPSDateStamp != after.GPSDateStamp || before.Meta.GPSTimeStamp != after.GPSTimeStamp {
		return fmt.Errorf("验证失败：GPS 日期时间发生变化")
	}
	return nil
}

func copyFileExact(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

func fileSHA256(path string) ([32]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [32]byte{}, err
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}
