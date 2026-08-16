package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type metadata struct {
	DateTimeOriginal       string
	CreateDate             string
	ModifyDate             string
	SubSecDateTimeOriginal string
	SubSecCreateDate       string
	SubSecModifyDate       string
	OffsetTime             string
	OffsetTimeOriginal     string
	OffsetTimeDigitized    string
	XMPDateCreated         string
	XMPCreateDate          string
	XMPModifyDate          string
	MetadataDate           string
	IPTCDateCreated        string
	IPTCTimeCreated        string
	IPTCDigitalDate        string
	IPTCDigitalTime        string
	HistoryWhen            string
	GPSDateStamp           string
	GPSTimeStamp           string
	UserComment            string
	IPTCDigest             string
	CurrentIPTCDigest      string
	ImageDataHash          string
	CreatorTool            string
	RawFileName            string
	PreservedFileName      string
}

type analysisState int

const (
	stateConsistent analysisState = iota
	stateInitialResidue
	stateMarkerMaintenance
	stateAmbiguous
	stateUnreadable
)

func (s analysisState) Label() string {
	switch s {
	case stateConsistent:
		return "时间一致"
	case stateInitialResidue:
		return "时区异常候选"
	case stateMarkerMaintenance:
		return "摘要维护"
	case stateAmbiguous:
		return "跳过：情况含糊"
	case stateUnreadable:
		return "读取失败"
	default:
		return "未知"
	}
}

type analysisResult struct {
	File         string
	Meta         metadata
	State        analysisState
	Reason       string
	TargetLocal  string
	SourceOffset string
	TargetOffset string
	ShiftMinutes int
}

func (r analysisResult) Repairable() bool {
	return r.State == stateInitialResidue || r.State == stateMarkerMaintenance
}

type parsedDate struct {
	Time      time.Time
	Local     string
	Fraction  string
	RawOffset string
}

var datePattern = regexp.MustCompile(`^(\d{4}):(\d{2}):(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(\.\d+)?(?:([+-]\d{2}:\d{2}|Z))?`)
var offsetPattern = regexp.MustCompile(`^([+-])(\d{2}):(\d{2})$`)

func findExifTool() (string, error) {
	path, err := exec.LookPath("exiftool")
	if err != nil {
		return "", errors.New("PATH 中没有找到 exiftool；请先确认 ExifTool 可在命令行直接运行")
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		return abs, nil
	}
	return path, nil
}

func readMetadata(exifTool, file string) (metadata, error) {
	return readMetadataWithRunner(directExifToolRunner{exifTool: exifTool}, file)
}

func readMetadataWithRunner(runner exifToolCommandRunner, file string) (metadata, error) {
	all, failures := readMetadataBatchWithRunner([]string{file}, nil, runner)
	if err := failures[file]; err != nil {
		return metadata{}, err
	}
	value, ok := all[file]
	if !ok {
		return metadata{}, errors.New("ExifTool 没有返回该文件的记录")
	}
	return value, nil
}

func readMetadataBatch(exifTool string, files []string) (map[string]metadata, map[string]error) {
	return readMetadataBatchWithProgress(exifTool, files, nil)
}

func readMetadataBatchWithProgress(exifTool string, files []string, progress func(done, total int)) (map[string]metadata, map[string]error) {
	results, failures, _ := readMetadataBatchWithProgressContext(context.Background(), exifTool, files, progress)
	return results, failures
}

func readMetadataBatchWithProgressContext(ctx context.Context, exifTool string, files []string, progress func(done, total int)) (map[string]metadata, map[string]error, error) {
	runner := exifToolCommandRunner(directExifToolRunner{exifTool: exifTool})
	session, err := newExifToolSession(exifTool)
	if err == nil {
		runner = session
		defer session.Close()
	}
	return readMetadataBatchWithRunnerContext(ctx, files, progress, runner)
}

func readMetadataBatchWithRunner(files []string, progress func(done, total int), runner exifToolCommandRunner) (map[string]metadata, map[string]error) {
	results, failures, _ := readMetadataBatchWithRunnerContext(context.Background(), files, progress, runner)
	return results, failures
}

func readMetadataBatchWithRunnerContext(ctx context.Context, files []string, progress func(done, total int), runner exifToolCommandRunner) (map[string]metadata, map[string]error, error) {
	results := make(map[string]metadata, len(files))
	failures := make(map[string]error)
	byDirectory := make(map[string][]string)
	for _, file := range files {
		clean := filepath.Clean(file)
		dir := filepath.Dir(clean)
		byDirectory[dir] = append(byDirectory[dir], clean)
	}

	done := 0
	const batchSize = 16
	for dir, group := range byDirectory {
		for start := 0; start < len(group); start += batchSize {
			if err := ctx.Err(); err != nil {
				return results, failures, err
			}
			end := min(start+batchSize, len(group))
			batch := group[start:end]
			args := metadataReadArguments()
			names := make([]string, 0, len(batch))
			for _, file := range batch {
				names = append(names, filepath.Base(file))
			}
			stdout, stderr, err := runner.RunFiles(dir, names, args...)
			if cancelErr := ctx.Err(); cancelErr != nil {
				return results, failures, cancelErr
			}
			if err != nil {
				for _, file := range batch {
					failures[file] = fmt.Errorf("ExifTool 读取失败：%v；%s", err, strings.TrimSpace(stderr))
				}
				done += len(batch)
				if progress != nil {
					progress(done, len(files))
				}
				continue
			}

			var rows []map[string]any
			if err := json.Unmarshal(stdout, &rows); err != nil {
				for _, file := range batch {
					failures[file] = fmt.Errorf("无法解析 ExifTool JSON：%w", err)
				}
				done += len(batch)
				if progress != nil {
					progress(done, len(files))
				}
				continue
			}
			for _, row := range rows {
				name := filepath.Base(getString(row, "SourceFile"))
				full := filepath.Join(dir, name)
				results[full] = decodeMetadata(row)
			}
			done += len(batch)
			if progress != nil {
				progress(done, len(files))
			}
		}
	}
	return results, failures, nil
}

func metadataReadArguments() []string {
	return []string{
		"-j", "-G1", "-s", "-n", "-a",
		"-api", "RequestAll=3",
		"-ExifIFD:DateTimeOriginal",
		"-ExifIFD:CreateDate",
		"-IFD0:ModifyDate",
		"-ExifIFD:SubSecTimeOriginal",
		"-ExifIFD:SubSecTimeDigitized",
		"-ExifIFD:SubSecTime",
		"-Composite:SubSecDateTimeOriginal",
		"-Composite:SubSecCreateDate",
		"-Composite:SubSecModifyDate",
		"-ExifIFD:OffsetTime",
		"-ExifIFD:OffsetTimeOriginal",
		"-ExifIFD:OffsetTimeDigitized",
		"-XMP-photoshop:DateCreated",
		"-XMP-xmp:CreateDate",
		"-XMP-xmp:ModifyDate",
		"-XMP-xmp:MetadataDate",
		"-IPTC:DateCreated",
		"-IPTC:TimeCreated",
		"-IPTC:DigitalCreationDate",
		"-IPTC:DigitalCreationTime",
		"-XMP-xmpMM:HistoryWhen",
		"-GPS:GPSDateStamp",
		"-GPS:GPSTimeStamp",
		"-ExifIFD:UserComment",
		"-Photoshop:IPTCDigest",
		"-File:CurrentIPTCDigest",
		"-File:ImageDataHash",
		"-XMP-xmp:CreatorTool",
		"-XMP-crs:RawFileName",
		"-XMP-xmpMM:PreservedFileName",
	}
}

func runExifToolForFiles(exifTool, dir string, names []string, args ...string) ([]byte, string, error) {
	argFile, err := os.CreateTemp("", "lrtimezonefix-*.args")
	if err != nil {
		return nil, "", fmt.Errorf("无法创建 ExifTool UTF-8 参数文件：%w", err)
	}
	argPath := argFile.Name()
	defer os.Remove(argPath)

	var content strings.Builder
	content.WriteString("--\n")
	for _, name := range names {
		// ExifTool argfiles use one argument per line. Newlines are not valid in
		// Windows filenames, so no additional escaping is necessary here.
		content.WriteString(name)
		content.WriteByte('\n')
	}
	if _, err := argFile.WriteString(content.String()); err != nil {
		argFile.Close()
		return nil, "", fmt.Errorf("无法写入 ExifTool UTF-8 参数文件：%w", err)
	}
	if err := argFile.Close(); err != nil {
		return nil, "", err
	}

	args = append(args, "-charset", "filename=UTF8", "-@", argPath)
	return runExifTool(exifTool, dir, args...)
}

func decodeMetadata(m map[string]any) metadata {
	mainOriginal := getString(m, "ExifIFD:DateTimeOriginal")
	mainCreate := getString(m, "ExifIFD:CreateDate")
	mainModify := getString(m, "IFD0:ModifyDate")
	return metadata{
		DateTimeOriginal:       mainOriginal,
		CreateDate:             mainCreate,
		ModifyDate:             mainModify,
		SubSecDateTimeOriginal: firstNonEmpty(getString(m, "Composite:SubSecDateTimeOriginal"), combineSubSec(mainOriginal, getString(m, "ExifIFD:SubSecTimeOriginal"), getString(m, "ExifIFD:OffsetTimeOriginal"))),
		SubSecCreateDate:       firstNonEmpty(getString(m, "Composite:SubSecCreateDate"), combineSubSec(mainCreate, getString(m, "ExifIFD:SubSecTimeDigitized"), getString(m, "ExifIFD:OffsetTimeDigitized"))),
		SubSecModifyDate:       firstNonEmpty(getString(m, "Composite:SubSecModifyDate"), combineSubSec(mainModify, getString(m, "ExifIFD:SubSecTime"), getString(m, "ExifIFD:OffsetTime"))),
		OffsetTime:             getString(m, "ExifIFD:OffsetTime"),
		OffsetTimeOriginal:     getString(m, "ExifIFD:OffsetTimeOriginal"),
		OffsetTimeDigitized:    getString(m, "ExifIFD:OffsetTimeDigitized"),
		XMPDateCreated:         getString(m, "XMP-photoshop:DateCreated"),
		XMPCreateDate:          getString(m, "XMP-xmp:CreateDate"),
		XMPModifyDate:          getString(m, "XMP-xmp:ModifyDate"),
		MetadataDate:           getString(m, "XMP-xmp:MetadataDate"),
		IPTCDateCreated:        getString(m, "IPTC:DateCreated"),
		IPTCTimeCreated:        getString(m, "IPTC:TimeCreated"),
		IPTCDigitalDate:        getString(m, "IPTC:DigitalCreationDate"),
		IPTCDigitalTime:        getString(m, "IPTC:DigitalCreationTime"),
		HistoryWhen:            getString(m, "XMP-xmpMM:HistoryWhen"),
		GPSDateStamp:           getString(m, "GPS:GPSDateStamp"),
		GPSTimeStamp:           getString(m, "GPS:GPSTimeStamp"),
		UserComment:            getString(m, "ExifIFD:UserComment"),
		IPTCDigest:             getString(m, "Photoshop:IPTCDigest"),
		CurrentIPTCDigest:      getString(m, "File:CurrentIPTCDigest"),
		ImageDataHash:          getString(m, "File:ImageDataHash"),
		CreatorTool:            getString(m, "XMP-xmp:CreatorTool"),
		RawFileName:            getString(m, "XMP-crs:RawFileName"),
		PreservedFileName:      getString(m, "XMP-xmpMM:PreservedFileName"),
	}
}

func runExifTool(exifTool, dir string, args ...string) ([]byte, string, error) {
	cmd := exec.Command(exifTool, args...)
	cmd.Dir = dir
	cmd.Env = cleanLocaleEnvironment(os.Environ())
	configureChildProcess(cmd)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

func cleanLocaleEnvironment(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, item := range env {
		upper := strings.ToUpper(item)
		if strings.HasPrefix(upper, "LC_ALL=") || strings.HasPrefix(upper, "LC_CTYPE=") || strings.HasPrefix(upper, "LANG=") {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}

func getString(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, v := range typed {
			parts = append(parts, fmt.Sprint(v))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(typed)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func combineSubSec(main, subSec, offset string) string {
	if main == "" {
		return ""
	}
	if subSec != "" {
		main += "." + strings.TrimPrefix(subSec, ".")
	}
	return main + offset
}

func parseExifDate(value string) (parsedDate, error) {
	match := datePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return parsedDate{}, fmt.Errorf("无法解析日期 %q", value)
	}
	parts := make([]int, 6)
	for i := 0; i < 6; i++ {
		parsed, err := strconv.Atoi(match[i+1])
		if err != nil {
			return parsedDate{}, err
		}
		parts[i] = parsed
	}
	nanosecond := 0
	fraction := match[7]
	if fraction != "" {
		digits := strings.TrimPrefix(fraction, ".")
		if len(digits) > 9 {
			digits = digits[:9]
		}
		padded := digits + strings.Repeat("0", 9-len(digits))
		nanosecond, _ = strconv.Atoi(padded)
	}
	t := time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], parts[5], nanosecond, time.UTC)
	local := fmt.Sprintf("%04d:%02d:%02d %02d:%02d:%02d%s", parts[0], parts[1], parts[2], parts[3], parts[4], parts[5], fraction)
	return parsedDate{Time: t, Local: local, Fraction: fraction, RawOffset: match[8]}, nil
}

func parseOffset(value string) (int, error) {
	match := offsetPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("无法解析时区 %q", value)
	}
	hours, _ := strconv.Atoi(match[2])
	minutes, _ := strconv.Atoi(match[3])
	if hours > 14 || minutes > 59 {
		return 0, fmt.Errorf("无效时区 %q", value)
	}
	total := hours*60 + minutes
	if match[1] == "-" {
		total = -total
	}
	return total, nil
}

func formatOffset(minutes int) string {
	sign := "+"
	if minutes < 0 {
		sign = "-"
		minutes = -minutes
	}
	return fmt.Sprintf("%s%02d:%02d", sign, minutes/60, minutes%60)
}

func isLightroomJPEG(m metadata) bool {
	if strings.Contains(strings.ToLower(m.CreatorTool), "lightroom") {
		return true
	}
	for _, name := range []string{m.RawFileName, m.PreservedFileName} {
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".nef" || ext == ".cr2" || ext == ".cr3" || ext == ".arw" || ext == ".raf" || ext == ".dng" {
			return true
		}
	}
	return false
}

func analyzeMetadata(file string, m metadata) analysisResult {
	base := analysisResult{File: file, Meta: m}
	// Use the EXIF main timestamps (whole seconds) for timezone inference. Some
	// Lightroom exports represent the same subsecond as .809 in one field and
	// .81 in another, which is a harmless 1 ms formatting/rounding difference.
	// Preserve the more precise DateTimeOriginal value only for the write target.
	dtoClock, err := parseExifDate(m.DateTimeOriginal)
	if err != nil {
		base.State = stateAmbiguous
		base.Reason = err.Error()
		return base
	}
	createClock, err := parseExifDate(m.CreateDate)
	if err != nil {
		base.State = stateAmbiguous
		base.Reason = err.Error()
		return base
	}
	dtoPrecise, err := parseExifDate(firstNonEmpty(m.SubSecDateTimeOriginal, m.DateTimeOriginal))
	if err != nil {
		base.State = stateAmbiguous
		base.Reason = err.Error()
		return base
	}
	originalOffset, err := parseOffset(m.OffsetTimeOriginal)
	if err != nil {
		base.State = stateAmbiguous
		base.Reason = err.Error()
		return base
	}
	createOffset, err := parseOffset(m.OffsetTimeDigitized)
	if err != nil {
		base.State = stateAmbiguous
		base.Reason = err.Error()
		return base
	}

	wallDelta := dtoClock.Time.Sub(createClock.Time)
	if wallDelta%time.Minute != 0 {
		base.State = stateAmbiguous
		base.Reason = "两组时间的差值不是整分钟，拒绝自动推断"
		return base
	}
	deltaMinutes := int(wallDelta / time.Minute)
	digestMismatch := m.IPTCDigest != "" && m.CurrentIPTCDigest != "" && !strings.EqualFold(m.IPTCDigest, m.CurrentIPTCDigest)
	hasMarker := strings.Contains(m.UserComment, auditPrefix)

	if deltaMinutes == 0 && originalOffset == createOffset {
		if hasMarker && digestMismatch {
			base.State = stateMarkerMaintenance
			base.Reason = "时间已一致，但本工具标记存在且 IPTCDigest 需要刷新"
			base.TargetLocal = dtoPrecise.Local
			base.SourceOffset = m.OffsetTimeOriginal
			base.TargetOffset = m.OffsetTimeOriginal
			return base
		}
		base.State = stateConsistent
		if digestMismatch {
			base.Reason = "时间一致，但检测到非本工具产生的 IPTCDigest 不一致；未自动修改"
		}
		return base
	}

	if !isLightroomJPEG(m) {
		base.State = stateAmbiguous
		base.Reason = "没有找到 Lightroom/RAW 来源证据，拒绝自动修改"
		return base
	}

	if originalOffset == createOffset && deltaMinutes != 0 {
		targetOffset := createOffset + deltaMinutes
		if abs(deltaMinutes) > 14*60 || targetOffset < -14*60 || targetOffset > 14*60 {
			base.State = stateAmbiguous
			base.Reason = "推断出的时区超出合理范围"
			return base
		}
		base.State = stateInitialResidue
		base.Reason = "拍摄时间已平移，但 OffsetTimeOriginal 仍与旧数字化时区相同"
		base.TargetLocal = dtoPrecise.Local
		base.SourceOffset = formatOffset(createOffset)
		base.TargetOffset = formatOffset(targetOffset)
		base.ShiftMinutes = deltaMinutes
		return base
	}

	base.State = stateAmbiguous
	base.Reason = "字段之间的差异无法唯一解释为未经处理的 Lightroom 时区调整"
	return base
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
