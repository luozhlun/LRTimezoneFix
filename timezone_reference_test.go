package main

import (
	"strings"
	"testing"
	"time"
)

func TestGPSTimezoneReferenceMissingGPS(t *testing.T) {
	ref := buildGPSTimezoneReference(baseTestMetadata(), analysisResult{})
	if ref.Status != "missing_gps" || ref.NeedsManualReview {
		t.Fatalf("unexpected missing-GPS reference: %+v", ref)
	}
}

func TestGPSTimezoneReferenceUsesGPSUTCAndDetectsParisDST(t *testing.T) {
	m := baseTestMetadata()
	m.GPSLatitude = "48.856600"
	m.GPSLongitude = "2.352200"
	m.GPSDateTime = "2025:07:15 12:30:00Z"
	result := analyzeMetadata("paris.jpg", m)

	ref := buildGPSTimezoneReference(m, result)
	if ref.Status != "available" || ref.Timezone != "Europe/Paris" {
		t.Fatalf("unexpected Paris lookup: %+v", ref)
	}
	if ref.Offset != "+02:00" || !strings.HasPrefix(ref.DSTLabel, "是") || ref.DateSource != "GPS UTC 时间" {
		t.Fatalf("unexpected DST result: %+v", ref)
	}
	if ref.NeedsManualReview {
		t.Fatalf("ordinary summer date should not warn: %+v", ref)
	}
}

func TestGPSTimezoneReferenceWarnsWithinDayOfTransition(t *testing.T) {
	m := baseTestMetadata()
	m.GPSLatitude = "48.856600"
	m.GPSLongitude = "2.352200"
	// Europe/Paris advances at 2025-03-30 01:00 UTC. This is 13 hours before it.
	m.GPSDateTime = "2025:03:29 12:00:00Z"

	ref := buildGPSTimezoneReference(m, analyzeMetadata("paris.jpg", m))
	if !ref.NeedsManualReview || !strings.Contains(ref.Warning, "不足 24 小时") || !strings.Contains(ref.Warning, "+01:00") || !strings.Contains(ref.Warning, "+02:00") {
		t.Fatalf("expected nearby transition warning: %+v", ref)
	}
}

func TestLocalWallTimeDetectsRepeatedAndSkippedHours(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	repeated, _ := parseExifDate("2025:11:02 01:30:00")
	if got := len(possibleUTCInstants(repeated.Time, location)); got != 2 {
		t.Fatalf("fall-back wall time candidates=%d, want 2", got)
	}
	skipped, _ := parseExifDate("2025:03:09 02:30:00")
	if got := len(possibleUTCInstants(skipped.Time, location)); got != 0 {
		t.Fatalf("spring-forward wall time candidates=%d, want 0", got)
	}
}

func TestGPSTimezoneReferenceDoesNotPresentSingleAnswerForRepeatedHour(t *testing.T) {
	m := baseTestMetadata()
	m.DateTimeOriginal = "2025:11:02 01:30:00"
	m.SubSecDateTimeOriginal = "2025:11:02 01:30:00"
	m.GPSLatitude = "40.712800"
	m.GPSLongitude = "-74.006000"

	ref := buildGPSTimezoneReference(m, analysisResult{})
	if ref.Timezone != "America/New_York" || ref.Offset != "存在两种可能" || ref.DSTLabel != "无法唯一判断" || !ref.NeedsManualReview {
		t.Fatalf("unexpected repeated-hour reference: %+v", ref)
	}
}

func TestNearestOffsetTransitionHonoursWindow(t *testing.T) {
	location, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	near := time.Date(2025, 3, 29, 12, 0, 0, 0, time.UTC)
	transition, ok := nearestOffsetTransition(near, location, 24*time.Hour)
	if !ok || transition.Before != 3600 || transition.After != 7200 {
		t.Fatalf("unexpected nearby transition: %+v ok=%v", transition, ok)
	}
	far := time.Date(2025, 3, 27, 12, 0, 0, 0, time.UTC)
	if _, ok := nearestOffsetTransition(far, location, 24*time.Hour); ok {
		t.Fatal("transition outside 24-hour window should not warn")
	}
}

func TestLoadTimezoneLocationCachesRules(t *testing.T) {
	first, err := loadTimezoneLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadTimezoneLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("timezone rules were loaded more than once")
	}
}

func TestDecodeMetadataIncludesSignedCompositeGPS(t *testing.T) {
	m := decodeMetadata(map[string]any{
		"Composite:GPSLatitude":  -33.8688,
		"Composite:GPSLongitude": 151.2093,
		"Composite:GPSDateTime":  "2025:01:15 02:00:00Z",
	})
	if m.GPSLatitude != "-33.8688" || m.GPSLongitude != "151.2093" || m.GPSDateTime == "" {
		t.Fatalf("unexpected decoded GPS metadata: %+v", m)
	}
}

func TestGPSOffsetTakesPriorityOverLegacyInference(t *testing.T) {
	for _, tc := range []struct {
		name, date, latitude, longitude, offset string
	}{
		{"Iceland UTC", "2026:09:27 08:04:05", "64.1499", "-21.9301", "+00:00"},
		{"Paris winter", "2025:01:15 13:39:15", "48.8566", "2.3522", "+01:00"},
		{"Paris summer", "2025:07:15 13:39:15", "48.8566", "2.3522", "+02:00"},
		{"Sydney summer", "2025:01:15 13:39:15", "-33.8688", "151.2093", "+11:00"},
		{"Kathmandu quarter hour", "2025:07:15 13:39:15", "27.7172", "85.3240", "+05:45"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseTestMetadata()
			m.DateTimeOriginal = tc.date
			m.SubSecDateTimeOriginal = tc.date + ".809+08:00"
			date, _ := parseExifDate(tc.date)
			m.CreateDate = date.Time.Add(-time.Hour).Format("2006:01:02 15:04:05")
			m.GPSLatitude, m.GPSLongitude = tc.latitude, tc.longitude
			r := analyzePhotoMetadata("photo.jpg", m)
			if !r.Repairable() || r.TargetOffset != tc.offset || r.TargetLocal != tc.date+".809" {
				t.Fatalf("GPS must override the legacy +09:00 inference and preserve the wall time: %+v", r)
			}
		})
	}
}

func TestGPSCorrectsConsistentOrMissingCameraOffsets(t *testing.T) {
	for _, offset := range []string{"+08:00", "", "Z", "+00:00"} {
		t.Run(offset, func(t *testing.T) {
			m := baseTestMetadata()
			m.CreateDate = m.DateTimeOriginal
			m.OffsetTimeOriginal, m.OffsetTimeDigitized = offset, offset
			m.GPSLatitude, m.GPSLongitude = "64.1499", "-21.9301"
			r := analyzePhotoMetadata("photo.jpg", m)
			if offset == "Z" || offset == "+00:00" {
				if r.State != stateConsistent {
					t.Fatalf("UTC representations must be equivalent: %+v", r)
				}
				m.UserComment = auditPrefix
				m.CurrentIPTCDigest = "bbbb"
				if maintenance := analyzePhotoMetadata("photo.jpg", m); maintenance.State != stateMarkerMaintenance || maintenance.TargetOffset != "+00:00" {
					t.Fatalf("UTC digest maintenance must use a canonical target offset: %+v", maintenance)
				}
			} else if !r.Repairable() || r.TargetOffset != "+00:00" {
				t.Fatalf("GPS should supply the missing/corrected offset: %+v", r)
			}
		})
	}
}

func TestGPSUnavailableRetainsLegacyInference(t *testing.T) {
	for _, latitude := range []string{"", "invalid", "91"} {
		m := baseTestMetadata()
		m.GPSLatitude, m.GPSLongitude = latitude, "2.3522"
		r := analyzePhotoMetadata("photo.jpg", m)
		if !r.Repairable() || r.TargetOffset != "+09:00" {
			t.Fatalf("unavailable GPS must retain legacy behavior: %+v", r)
		}
	}
}

func TestGPSDoesNotAutomaticallyRepairNearTransitions(t *testing.T) {
	for _, tc := range []struct{ name, wall, gpsUTC string }{
		{"before spring", "2025:03:08 13:30:00", ""},
		{"skipped hour", "2025:03:09 02:30:00", ""},
		{"repeated hour", "2025:11:02 01:30:00", ""},
		{"GPS UTC near transition", "2025:11:02 01:30:00", "2025:11:02 06:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseTestMetadata()
			m.DateTimeOriginal, m.CreateDate = tc.wall, tc.wall
			m.SubSecDateTimeOriginal = tc.wall
			m.GPSLatitude, m.GPSLongitude = "40.7128", "-74.0060"
			m.GPSDateTime = tc.gpsUTC
			r := analyzePhotoMetadata("photo.jpg", m)
			if r.State != stateAmbiguous || r.Repairable() || !r.GPSReference.NeedsManualReview {
				t.Fatalf("near-transition photos must require review: %+v", r)
			}
		})
	}
}

func TestGPSXMPAllowsConflictingOffsetsButRequiresMatchingWallTime(t *testing.T) {
	m := xmpTestMetadata("2026:09:27 08:04:05.440Z", "2026:09:27 10:04:05.44+02:00", "2026:09:27 08:04:05.440+02:00")
	m.GPSLatitude, m.GPSLongitude = "64.1499", "-21.9301"
	r := analyzePhotoMetadata("photo.xmp", m)
	if !r.Repairable() || r.TargetOffset != "+00:00" || r.TargetLocal != "2026:09:27 08:04:05.440" {
		t.Fatalf("GPS should resolve stale XMP offsets: %+v", r)
	}
	m.XMPDateCreated = "2026:09:27 07:04:05Z"
	if r := analyzePhotoMetadata("photo.xmp", m); r.Repairable() {
		t.Fatalf("GPS cannot resolve conflicting capture wall times: %+v", r)
	}
	m = baseTestMetadata()
	m.CreatorTool = ""
	m.GPSLatitude, m.GPSLongitude = "64.1499", "-21.9301"
	if r := analyzePhotoMetadata("photo.jpg", m); r.Repairable() {
		t.Fatalf("GPS should retain the source evidence requirement: %+v", r)
	}
}
