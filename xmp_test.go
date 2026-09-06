package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func xmpTestMetadata(original, create, created string) metadata {
	return decodeMetadata(map[string]any{
		"File:FileType":             "XMP",
		"XMP-exif:DateTimeOriginal": original,
		"XMP-xmp:CreateDate":        create,
		"XMP-photoshop:DateCreated": created,
		"XMP-crs:RawFileName":       "DSC_0015.NEF",
	})
}

func TestXMPInference(t *testing.T) {
	for _, tc := range []struct {
		name, original, create, created, target string
		state                                   analysisState
	}{
		{"rounding", "2025:10:01 13:39:15.809+08:00", "2025:10:01 12:39:15.81+08:00", "2025:10:01 13:39:15.809+08:00", "+09:00", stateInitialResidue},
		{"midnight", "2026:01:01 00:10:00+08:00", "2025:12:31 23:10:00+08:00", "2026:01:01 00:10:00+08:00", "+09:00", stateInitialResidue},
		{"westward", "2025:10:01 05:39:15+08:00", "2025:10:01 12:39:15+08:00", "2025:10:01 05:39:15+08:00", "+01:00", stateInitialResidue},
		{"utc", "2025:10:01 13:39:15Z", "2025:10:01 12:39:15Z", "2025:10:01 13:39:15Z", "+01:00", stateInitialResidue},
		{"consistent", "2025:10:01 13:39:15+09:00", "2025:10:01 13:39:15+09:00", "2025:10:01 13:39:15+09:00", "", stateConsistent},
		{"missing offset", "2025:10:01 13:39:15", "2025:10:01 12:39:15+08:00", "2025:10:01 13:39:15", "", stateAmbiguous},
		{"different offsets", "2025:10:01 13:39:15+09:00", "2025:10:01 12:39:15+08:00", "2025:10:01 13:39:15+09:00", "", stateAmbiguous},
		{"conflicting datecreated", "2025:10:01 13:39:15+08:00", "2025:10:01 12:39:15+08:00", "2025:10:01 12:39:15+08:00", "", stateAmbiguous},
		{"missing datecreated", "2025:10:01 13:39:15+08:00", "2025:10:01 12:39:15+08:00", "", "", stateAmbiguous},
		{"non-minute", "2025:10:01 13:39:16+08:00", "2025:10:01 12:39:15+08:00", "2025:10:01 13:39:16+08:00", "", stateAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := xmpTestMetadata(tc.original, tc.create, tc.created)
			r := analyzeMetadata("photo.XMP", m)
			if r.State != tc.state || r.TargetOffset != tc.target {
				t.Fatalf("unexpected result: %+v", r)
			}
			if r.Repairable() {
				date, _ := parseExifDate(tc.original)
				if r.TargetLocal != date.Local {
					t.Fatalf("lost fractional precision: %s", r.TargetLocal)
				}
			}
		})
	}
	m := xmpTestMetadata("2025:10:01 13:39:15+08:00", "2025:10:01 12:39:15+08:00", "2025:10:01 13:39:15+08:00")
	m.RawFileName = ""
	if r := analyzeMetadata("photo.xmp", m); r.State != stateAmbiguous {
		t.Fatal("must require Lightroom/RAW evidence")
	}
}

func TestMixedFileSelection(t *testing.T) {
	root := t.TempDir()
	files := []string{filepath.Join(root, "a.jpg"), filepath.Join(root, "b.JPEG"), filepath.Join(root, "c.XMP")}
	for _, file := range append(append([]string{}, files...), filepath.Join(root, "d.NEF")) {
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := findSupportedFiles(root)
	if err != nil || !reflect.DeepEqual(found, files) {
		t.Fatalf("files=%v err=%v", found, err)
	}
	normalized, err := normalizeFileSelection(append(files, files[2]))
	if err != nil || !reflect.DeepEqual(normalized, files) {
		t.Fatalf("selection=%v err=%v", normalized, err)
	}
}

// Exercise real ExifTool writes on temporary sidecars only, including Lightroom
// adjustments and structured history which must survive the three date writes.
func TestXMPRepairIntegration(t *testing.T) {
	tool, err := findExifTool()
	if err != nil {
		t.Skip("ExifTool is required for integration test")
	}
	file := filepath.Join(t.TempDir(), "photo.xmp")
	data := `<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Adobe XMP Core 9.1">
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" xmlns:crs="http://ns.adobe.com/camera-raw-settings/1.0/" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/" xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#"
exif:DateTimeOriginal="2025-10-01T13:39:15.809+08:00" xmp:CreateDate="2025-10-01T12:39:15.81+08:00" photoshop:DateCreated="2025-10-01T13:39:15.809+08:00"
xmp:ModifyDate="2026-01-01T12:00:00+08:00" xmp:MetadataDate="2026-01-01T12:05:00+08:00" crs:RawFileName="DSC_0015.NEF" crs:Exposure2012="+0.35" crs:Temperature="5500" crs:HasSettings="True" xmp:Rating="4"
exif:GPSLatitude="33,38.3150722818N" exif:GPSLongitude="130,11.790297987E" exif:GPSDateTime="2025-10-01T04:39:15Z">
<crs:ToneCurvePV2012><rdf:Seq><rdf:li>0, 0</rdf:li><rdf:li>255, 255</rdf:li></rdf:Seq></crs:ToneCurvePV2012>
<xmpMM:History><rdf:Seq><rdf:li rdf:parseType="Resource"><stEvt:action>saved</stEvt:action><stEvt:when>2026-01-01T12:05:00+08:00</stEvt:when></rdf:li></rdf:Seq></xmpMM:History>
</rdf:Description></rdf:RDF></x:xmpmeta>`
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	checkXMPRepair(t, tool, file)
}

func TestXMPGPSRead(t *testing.T) {
	tool, err := findExifTool()
	if err != nil {
		t.Skip("ExifTool is required for integration test")
	}
	for _, tc := range []struct {
		name, latitude, longitude, gpsTime, wantLatitude, wantLongitude, zone, offset string
	}{
		{"DSC_0015 coordinates without GPS time", "33,38.3150722818N", "130,11.790297987E", "", "33.63858453803", "130.19650496645", "Asia/Tokyo", "+09:00"},
		{"south and west with GPS time", "33,27S", "70,40W", `exif:GPSDateTime="2025-10-01T04:39:15Z"`, "-33.45", "-70.6666666666667", "America/Santiago", "-03:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "photo.xmp")
			data := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" exif:DateTimeOriginal="2025-10-01T13:39:15.65+08:00" exif:GPSLatitude="` + tc.latitude + `" exif:GPSLongitude="` + tc.longitude + `" ` + tc.gpsTime + `/></rdf:RDF></x:xmpmeta>`
			if err := os.WriteFile(file, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			m, err := readMetadata(tool, file)
			if err != nil {
				t.Fatal(err)
			}
			if m.GPSLatitude != tc.wantLatitude || m.GPSLongitude != tc.wantLongitude {
				t.Fatalf("GPS coordinates: %s / %s", m.GPSLatitude, m.GPSLongitude)
			}
			ref := buildGPSTimezoneReference(m, analysisResult{})
			if ref.Status != "available" || ref.Timezone != tc.zone || ref.Offset != tc.offset {
				t.Fatalf("unexpected GPS reference: %+v", ref)
			}
			if tc.gpsTime != "" && ref.DateSource != "GPS UTC 时间" {
				t.Fatalf("GPS timestamp not used: %+v", ref)
			}
			t.Logf("GPS reference: %+v", ref)
		})
	}
}

func checkXMPRepair(t *testing.T, tool, file string) {
	t.Helper()
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	readAll := func() map[string]any {
		out, stderr, err := runExifToolForFiles(tool, filepath.Dir(file), []string{filepath.Base(file)}, "-j", "-G1", "-struct", "-n", "-XMP:all")
		if err != nil {
			t.Fatalf("%v: %s", err, stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatal(err)
		}
		for _, tag := range []string{"XMP-exif:DateTimeOriginal", "XMP-xmp:CreateDate", "XMP-photoshop:DateCreated"} {
			delete(rows[0], tag)
		}
		return rows[0]
	}
	before := readAll()
	m, err := readMetadata(tool, file)
	if err != nil {
		t.Fatal(err)
	}
	r := analyzeMetadata(file, m)
	if !r.Repairable() {
		t.Fatalf("not repairable: %s", r.Reason)
	}
	stamp := "20260906_120000"
	if err := repairFile(tool, &r, stamp, time.Now()); err != nil {
		t.Fatal(err)
	}
	if after := readAll(); !reflect.DeepEqual(before, after) {
		t.Fatalf("unrelated XMP metadata changed:\nbefore=%v\nafter=%v", before, after)
	}
	backup, err := os.ReadFile(filepath.Join(filepath.Dir(file), backupPrefix+stamp, filepath.Base(file)))
	if err != nil || string(backup) != string(original) {
		t.Fatal("original backup differs")
	}
	after, err := readMetadata(tool, file)
	if err != nil {
		t.Fatal(err)
	}
	if r := analyzeMetadata(file, after); r.State != stateConsistent {
		t.Fatalf("second scan: %+v", r)
	}
	if !strings.Contains(after.XMPDateTimeOriginal, "+09:00") {
		t.Fatal("target timezone was not written")
	}
	// A failed read-back must restore the actual pre-write sidecar.
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := repairFileWithRunner(tool, failedXMPReader{}, &r, "20260906_120001", time.Now()); err == nil {
		t.Fatal("expected read-back failure")
	}
	restored, err := os.ReadFile(file)
	if err != nil || string(restored) != string(original) {
		t.Fatal("failed repair did not restore original XMP")
	}
}

type failedXMPReader struct{}

func (failedXMPReader) RunFiles(string, []string, ...string) ([]byte, string, error) {
	return nil, "", errors.New("simulated read-back failure")
}

func TestRealXMPSamples(t *testing.T) {
	root := os.Getenv("LRTZ_XMP_SAMPLES")
	if root == "" {
		t.Skip("set LRTZ_XMP_SAMPLES to read real sidecars and repair temporary copies")
	}
	tool, err := findExifTool()
	if err != nil {
		t.Fatal(err)
	}
	files, err := findSupportedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	all, failures := readMetadataBatch(tool, files)
	if len(failures) > 0 {
		t.Fatalf("read failures: %v", failures)
	}
	fractions := make(map[bool]bool)
	for _, file := range files {
		m := all[file]
		r := analyzeMetadata(file, m)
		if !r.Repairable() || r.TargetOffset != "+09:00" {
			t.Fatalf("%s: %+v", file, r)
		}
		a, _ := parseExifDate(m.SubSecDateTimeOriginal)
		b, _ := parseExifDate(m.SubSecCreateDate)
		rounded := a.Fraction != b.Fraction
		if fractions[rounded] {
			continue
		}
		fractions[rounded] = true
		copy := filepath.Join(t.TempDir(), filepath.Base(file))
		if err := copyFileExact(file, copy); err != nil {
			t.Fatal(err)
		}
		checkXMPRepair(t, tool, copy)
	}
	t.Logf("Read-only scan: %d XMP candidates; repaired %d representative temporary copies", len(files), len(fractions))
}
