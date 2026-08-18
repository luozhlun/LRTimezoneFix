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
