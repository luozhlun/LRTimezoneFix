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
	return repairFileWithRunner(exifTool, directExifToolRunner{exifTool: exifTool}, candidate, stamp, repairedAt)
}

func repairFileWithRunner(exifTool string, reader exifToolCommandRunner, candidate *analysisResult, stamp string, repairedAt time.Time) (err error) {
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
	logEntryWritten := false
	if err := appendBackupLogEntry(backupDir, file, backupPath, sourceHash, *candidate, repairedAt); err != nil {
		return fmt.Errorf("无法写入备份日志：%w", err)
	}
	logEntryWritten = true

	restoreNeeded := true
	defer func() {
		if !restoreNeeded || err == nil {
			return
		}
		status := "FAILED_AND_RESTORED"
		if restoreErr := copyFileExact(backupPath, file); restoreErr != nil {
			err = fmt.Errorf("%v；自动恢复也失败：%w；请手动使用备份 %s", err, restoreErr, backupPath)
			status = "FAILED_RESTORE_ERROR"
		} else {
			restoredHash, hashErr := fileSHA256(file)
			if hashErr != nil || restoredHash != sourceHash {
				err = fmt.Errorf("%v；自动恢复后的 SHA-256 验证失败；请手动使用备份 %s", err, backupPath)
				status = "FAILED_RESTORE_HASH_ERROR"
			}
		}
		if logEntryWritten {
			_ = appendBackupLogResult(backupDir, file, status, err.Error(), time.Now())
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

	after, err := readMetadataWithRunner(reader, file)
	if err != nil {
		return fmt.Errorf("写后读取失败：%w", err)
	}
	if err := verifyRepair(*candidate, after, marker); err != nil {
		return err
	}
	if err := appendBackupLogResult(backupDir, file, "REPAIR_VERIFIED", "元数据、摘要和 JPEG 图像数据验证通过", time.Now()); err != nil {
		return fmt.Errorf("无法更新备份日志：%w", err)
	}

	restoreNeeded = false
	return nil
}

func appendBackupLogEntry(backupDir, sourcePath, backupPath string, sourceHash [32]byte, candidate analysisResult, batchTime time.Time) error {
	logPath := filepath.Join(backupDir, "LRTimezoneFix_Backup.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	var text strings.Builder
	if info.Size() == 0 {
		fmt.Fprintf(&text, "LRTimezoneFix Backup Log\nLogFormat=1\nToolVersion=%s\nBatchTime=%s\nPurpose=修复 Lightroom 导出 JPG 的时区残留；本目录保存修改前原文件。\nRestore=如需恢复，请关闭可能占用照片的软件，将备份 JPG 复制回 OriginalPath 并覆盖。\n\n", version, batchTime.Format(time.RFC3339))
	}
	fmt.Fprintf(&text, "[Backup]\nFileName=%s\nOriginalPath=%s\nBackupPath=%s\nOriginalSHA256=%x\nSourceOffset=%s\nTargetOffset=%s\nWallShift=%s\nTargetLocal=%s\nBackupState=VERIFIED\n\n",
		filepath.Base(sourcePath), sourcePath, backupPath, sourceHash, candidate.SourceOffset, candidate.TargetOffset, formatSignedMinutes(candidate.ShiftMinutes), candidate.TargetLocal)
	if _, err := file.WriteString(text.String()); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func appendBackupLogResult(backupDir, sourcePath, status, detail string, at time.Time) error {
	logPath := filepath.Join(backupDir, "LRTimezoneFix_Backup.log")
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	detail = strings.NewReplacer("\r", " ", "\n", " ").Replace(detail)
	_, writeErr := fmt.Fprintf(file, "[Result]\nFileName=%s\nStatus=%s\nTime=%s\nDetail=%s\n\n", filepath.Base(sourcePath), status, at.Format(time.RFC3339), detail)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
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
