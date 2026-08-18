package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ringsaturn/tzf"
	_ "time/tzdata"
)

const timezoneTransitionWarningWindow = 24 * time.Hour

// GPSTimezoneReference is read-only contextual information. It is deliberately
// kept separate from the repair inference and never changes repair eligibility.
type GPSTimezoneReference struct {
	Status            string  `json:"status"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	Coordinates       string  `json:"coordinates"`
	Timezone          string  `json:"timezone"`
	ReferenceTime     string  `json:"referenceTime"`
	DateSource        string  `json:"dateSource"`
	Offset            string  `json:"offset"`
	DSTLabel          string  `json:"dstLabel"`
	NeedsManualReview bool    `json:"needsManualReview"`
	Warning           string  `json:"warning"`
	Note              string  `json:"note"`
}

var (
	timezoneFinderOnce sync.Once
	timezoneFinder     tzf.F
	timezoneFinderErr  error
)

func analyzePhotoMetadata(file string, m metadata) analysisResult {
	result := analyzeMetadata(file, m)
	result.GPSReference = buildGPSTimezoneReference(m, result)
	return result
}

func buildGPSTimezoneReference(m metadata, result analysisResult) GPSTimezoneReference {
	ref := GPSTimezoneReference{Status: "missing_gps", Note: "照片未提供可用的 GPS 坐标。"}
	if strings.TrimSpace(m.GPSLatitude) == "" || strings.TrimSpace(m.GPSLongitude) == "" {
		return ref
	}

	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(m.GPSLatitude), 64)
	longitude, lngErr := strconv.ParseFloat(strings.TrimSpace(m.GPSLongitude), 64)
	if latErr != nil || lngErr != nil || math.IsNaN(latitude) || math.IsNaN(longitude) || math.IsInf(latitude, 0) || math.IsInf(longitude, 0) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		ref.Status = "invalid_gps"
		ref.Note = "照片中的 GPS 坐标格式无效，无法推算地理时区。"
		return ref
	}
	ref.Latitude = latitude
	ref.Longitude = longitude
	ref.Coordinates = fmt.Sprintf("%.6f, %.6f", latitude, longitude)

	finder, err := getTimezoneFinder()
	if err != nil {
		ref.Status = "lookup_failed"
		ref.Note = "离线时区边界数据加载失败，无法提供 GPS 时区参考。"
		return ref
	}
	timezoneName := finder.GetTimezoneName(longitude, latitude)
	if timezoneName == "" {
		ref.Status = "timezone_not_found"
		ref.Note = "未能从该 GPS 坐标确定地理时区。"
		return ref
	}
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		ref.Status = "lookup_failed"
		ref.Note = "已找到地理时区，但无法加载其历史规则。"
		return ref
	}
	ref.Timezone = timezoneName

	centerUTC, localTime, source, ambiguity, err := timezoneReferenceTime(m, result, location)
	if err != nil {
		ref.Status = "missing_date"
		ref.Note = "已找到地理时区，但照片缺少可用于判断当日偏移的拍摄日期。"
		return ref
	}

	ref.Status = "available"
	ref.ReferenceTime = localTime.Format("2006:01:02 15:04:05")
	ref.DateSource = source
	_, offsetSeconds := localTime.Zone()
	ref.Offset = formatOffset(offsetSeconds / 60)
	if localTime.IsDST() {
		zone, _ := localTime.Zone()
		ref.DSTLabel = "是"
		if zone != "" {
			ref.DSTLabel += "（" + zone + "）"
		}
	} else {
		zone, _ := localTime.Zone()
		ref.DSTLabel = "否"
		if zone != "" && !strings.HasPrefix(zone, "+") && !strings.HasPrefix(zone, "-") {
			ref.DSTLabel += "（" + zone + "）"
		}
	}
	if ambiguity == "ambiguous" {
		ref.Offset = "存在两种可能"
		ref.DSTLabel = "无法唯一判断"
	} else if ambiguity == "nonexistent" {
		ref.Offset = "当地时间不存在"
		ref.DSTLabel = "无法判断"
	}

	warnings := make([]string, 0, 2)
	if ambiguity == "ambiguous" {
		warnings = append(warnings, "该当地时间位于时钟回拨产生的重复时段，可能对应两个 UTC 时刻。")
	} else if ambiguity == "nonexistent" {
		warnings = append(warnings, "该当地时间位于时钟拨快跳过的时段，在此时区中不存在。")
	}
	if transition, ok := nearestOffsetTransition(centerUTC, location, timezoneTransitionWarningWindow); ok {
		warnings = append(warnings, fmt.Sprintf("距离时区偏移变更不足 24 小时：%s 将从 %s 调整为 %s。", transition.Local.Format("2006-01-02 15:04"), formatOffset(transition.Before/60), formatOffset(transition.After/60)))
	}
	if len(warnings) > 0 {
		ref.NeedsManualReview = true
		ref.Warning = strings.Join(warnings, " ") + " 照片时间可能经过 Lightroom 平移，建议人工核对。"
	}
	ref.Note = "GPS 推算仅供参考，不参与自动修复判断或默认勾选。"
	return ref
}

func getTimezoneFinder() (tzf.F, error) {
	timezoneFinderOnce.Do(func() {
		timezoneFinder, timezoneFinderErr = tzf.NewDefaultFinder()
	})
	return timezoneFinder, timezoneFinderErr
}

func timezoneReferenceTime(m metadata, result analysisResult, location *time.Location) (time.Time, time.Time, string, string, error) {
	if gpsUTC, ok := parseGPSDateTime(m.GPSDateTime); ok {
		return gpsUTC, gpsUTC.In(location), "GPS UTC 时间", "", nil
	}

	value := firstNonEmpty(result.TargetLocal, m.SubSecDateTimeOriginal, m.DateTimeOriginal)
	parsed, err := parseExifDate(value)
	if err != nil {
		return time.Time{}, time.Time{}, "", "", err
	}
	wall := parsed.Time
	instants := possibleUTCInstants(wall, location)
	if len(instants) > 0 {
		ambiguity := ""
		if len(instants) > 1 {
			ambiguity = "ambiguous"
		}
		chosen := instants[0]
		return chosen, chosen.In(location), "拍摄当地时间", ambiguity, nil
	}

	// time.Date normalizes a wall time inside a spring-forward gap. Retain that
	// result only so the UI can explain the invalid local time and nearby rule.
	local := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), location)
	return local.UTC(), local, "拍摄当地时间", "nonexistent", nil
}

func parseGPSDateTime(value string) (time.Time, bool) {
	parsed, err := parseExifDate(value)
	if err != nil || parsed.RawOffset == "" {
		return time.Time{}, false
	}
	offsetMinutes := 0
	if parsed.RawOffset != "Z" {
		offsetMinutes, err = parseOffset(parsed.RawOffset)
		if err != nil {
			return time.Time{}, false
		}
	}
	return parsed.Time.Add(-time.Duration(offsetMinutes) * time.Minute), true
}

func possibleUTCInstants(wall time.Time, location *time.Location) []time.Time {
	offsets := make(map[int]bool)
	for hours := -48; hours <= 48; hours++ {
		_, offset := wall.Add(time.Duration(hours) * time.Hour).In(location).Zone()
		offsets[offset] = true
	}
	instants := make([]time.Time, 0, len(offsets))
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if sameWallClock(wall, local) {
			instants = append(instants, candidate)
		}
	}
	sort.Slice(instants, func(i, j int) bool { return instants[i].Before(instants[j]) })
	return instants
}

func sameWallClock(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day() &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute() && a.Second() == b.Second() && a.Nanosecond() == b.Nanosecond()
}

type offsetTransition struct {
	UTC    time.Time
	Local  time.Time
	Before int
	After  int
}

func nearestOffsetTransition(center time.Time, location *time.Location, warningWindow time.Duration) (offsetTransition, bool) {
	searchStart := center.Add(-48 * time.Hour).Truncate(time.Minute)
	searchEnd := center.Add(48 * time.Hour)
	previous := searchStart
	_, previousOffset := previous.In(location).Zone()
	var best offsetTransition
	bestDistance := time.Duration(math.MaxInt64)
	for cursor := previous.Add(30 * time.Minute); !cursor.After(searchEnd); cursor = cursor.Add(30 * time.Minute) {
		_, offset := cursor.In(location).Zone()
		if offset != previousOffset {
			transitionUTC := locateOffsetTransition(previous, cursor, location, previousOffset)
			_, afterOffset := transitionUTC.In(location).Zone()
			distance := transitionUTC.Sub(center)
			if distance < 0 {
				distance = -distance
			}
			if distance <= warningWindow && distance < bestDistance {
				bestDistance = distance
				best = offsetTransition{UTC: transitionUTC, Local: transitionUTC.In(location), Before: previousOffset, After: afterOffset}
			}
		}
		previous = cursor
		previousOffset = offset
	}
	return best, bestDistance != time.Duration(math.MaxInt64)
}

func locateOffsetTransition(low, high time.Time, location *time.Location, beforeOffset int) time.Time {
	for high.Sub(low) > time.Second {
		mid := low.Add(high.Sub(low) / 2)
		_, offset := mid.In(location).Zone()
		if offset == beforeOffset {
			low = mid
		} else {
			high = mid
		}
	}
	return high.Truncate(time.Second)
}
