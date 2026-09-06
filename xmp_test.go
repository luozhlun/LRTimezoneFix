package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Optional real-file benchmarks. Reads source files; repairs temporary copies.
// LRTZ_BENCH_ROOT selects an XMP or JPEG sample directory.
func BenchmarkExifToolPipeline(b *testing.B) {
	root := os.Getenv("LRTZ_BENCH_ROOT")
	if root == "" {
		b.Skip("set LRTZ_BENCH_ROOT to a sample directory")
	}
	tool, err := findExifTool()
	if err != nil {
		b.Fatal(err)
	}
	files, err := findSupportedFiles(root)
	if err != nil || len(files) == 0 {
		b.Fatalf("samples: %v", err)
	}
	files = files[:min(128, len(files))]
	for _, workers := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("read/%d-sessions", workers), func(b *testing.B) {
			var cpu time.Duration
			for n := 0; n < b.N; n++ {
				var wg sync.WaitGroup
				var mu sync.Mutex
				for worker := 0; worker < workers; worker++ {
					group := files[len(files)*worker/workers : len(files)*(worker+1)/workers]
					wg.Go(func() {
						session, err := newExifToolSession(tool)
						if err != nil {
							b.Error(err)
							return
						}
						all, failures, _ := readMetadataBatchWithArguments(context.Background(), group, nil, session, metadataReadArguments(false))
						if len(failures) != 0 || len(all) != len(group) {
							b.Errorf("read: %v", failures)
						}
						if err := session.Close(); err != nil {
							b.Error(err)
						}
						mu.Lock()
						cpu += session.cmd.ProcessState.UserTime() + session.cmd.ProcessState.SystemTime()
						mu.Unlock()
					})
				}
				wg.Wait()
			}
			b.ReportMetric(float64(len(files)*b.N)/b.Elapsed().Seconds(), "files/s")
			b.ReportMetric(cpu.Seconds()/b.Elapsed().Seconds(), "cpu-cores")
		})
	}
	b.Run("repair", func(b *testing.B) {
		b.StopTimer()
		originals := files[:min(24, len(files))]
		all, failures := readMetadataBatch(tool, originals)
		if len(failures) != 0 {
			b.Fatal(failures)
		}
		for n := 0; n < b.N; n++ {
			dir := b.TempDir()
			candidates := make([]analysisResult, 0, len(originals))
			for _, file := range originals {
				r := analyzeMetadata(file, all[file])
				if !r.Repairable() {
					b.Fatalf("not repairable: %s", file)
				}
				r.File = filepath.Join(dir, filepath.Base(file))
				if err := copyFileExact(file, r.File); err != nil {
					b.Fatal(err)
				}
				candidates = append(candidates, r)
			}
			b.StartTimer()
			session, err := newExifToolSession(tool)
			if err != nil {
				b.Fatal(err)
			}
			for i := range candidates {
				if err := repairFileWithRunner(session, &candidates[i], "20260906_130000", time.Now()); err != nil {
					b.Fatal(err)
				}
			}
			if err := session.Close(); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
		}
		b.ReportMetric(float64(len(originals)*b.N)/b.Elapsed().Seconds(), "files/s")
	})
}

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

func TestPersistentRepairJPEGAndXMP(t *testing.T) {
	tool, err := findExifTool()
	if err != nil {
		t.Skip("ExifTool is required for integration test")
	}
	dir := t.TempDir()
	jpg := filepath.Join(dir, "照片.jpg")
	f, err := os.Create(jpg)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	comment := "原备注 C:\\photos\\new\r\n-execute\n第二行"
	session, err := newExifToolSession(tool)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	_, stderr, err := session.RunFiles(dir, []string{filepath.Base(jpg)}, "-overwrite_original",
		"-EXIF:DateTimeOriginal=2025:10:01 13:39:15", "-EXIF:CreateDate=2025:10:01 12:39:15",
		"-EXIF:OffsetTimeOriginal=+08:00", "-EXIF:OffsetTimeDigitized=+08:00",
		"-IPTC:DateCreated=2025:10:01", "-IPTC:TimeCreated=13:39:15+08:00",
		"-IPTC:DigitalCreationDate=2025:10:01", "-IPTC:DigitalCreationTime=12:39:15+08:00",
		"-EXIF:UserComment="+comment, "-XMP-xmp:CreatorTool=Adobe Photoshop Lightroom Classic")
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	sidecar := filepath.Join(dir, "照片.xmp")
	xmp := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" exif:DateTimeOriginal="2025-10-01T13:39:15.65+08:00" xmp:CreateDate="2025-10-01T12:39:15.65+08:00" photoshop:DateCreated="2025-10-01T13:39:15.65+08:00" xmp:CreatorTool="Adobe Photoshop Lightroom Classic"/></rdf:RDF></x:xmpmeta>`
	if err := os.WriteFile(sidecar, []byte(xmp), 0600); err != nil {
		t.Fatal(err)
	}
	files := []string{jpg, sidecar}
	all, failures := readMetadataBatch(tool, files)
	if len(failures) != 0 || len(all) != 2 {
		t.Fatalf("scan failed: %v", failures)
	}
	if all[jpg].ImageDataHash != "" {
		t.Fatal("scan must not hash JPEG image data")
	}
	for _, file := range files {
		r := analyzeMetadata(file, all[file])
		if !r.Repairable() {
			t.Fatalf("%s: %s", file, r.Reason)
		}
		if err := repairFileWithRunner(session, &r, "20260906_130000", time.Now()); err != nil {
			t.Fatal(err)
		}
		after, err := readMetadataWithRunner(session, file)
		if err != nil {
			t.Fatal(err)
		}
		if !isXMP(file) && !strings.HasPrefix(after.UserComment, comment+"\n") {
			t.Fatalf("comment changed: %q", after.UserComment)
		}
		if again := analyzeMetadata(file, after); again.State != stateConsistent {
			t.Fatalf("second scan: %+v", again)
		}
	}
	// Command failure must be reported without desynchronizing the next request.
	if out, stderr, err := session.RunFiles(dir, []string{"missing.jpg"}, "-XMP-xmp:Rating=1"); err == nil {
		t.Fatalf("write failure not reported: %s / %s", out, stderr)
	}
	if _, err := readMetadataWithRunner(session, sidecar); err != nil {
		t.Fatalf("session after failure: %v", err)
	}
}

func TestParallelScanProgressAndCancellation(t *testing.T) {
	tool, err := findExifTool()
	if err != nil {
		t.Skip("ExifTool is required for integration test")
	}
	dir := t.TempDir()
	files := make([]string, 80)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("%03d.xmp", i))
		if err := os.WriteFile(files[i], []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about=""/></rdf:RDF></x:xmpmeta>`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	last := 0
	all, failures, err := readMetadataBatchWithProgressContext(context.Background(), tool, files, func(done, total int) {
		if done <= last || done > total || total != len(files) {
			t.Errorf("bad progress %d after %d / %d", done, last, total)
		}
		last = done
	})
	if err != nil || len(failures) != 0 || len(all) != len(files) || last != len(files) {
		t.Fatalf("scan: %d rows, %v, %v", len(all), failures, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err = readMetadataBatchWithProgressContext(ctx, tool, files, func(int, int) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
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
	session, err := newExifToolSession(tool)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := repairFileWithRunner(session, &r, stamp, time.Now()); err != nil {
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
	if err := repairFileWithRunner(failedXMPReader{writer: session}, &r, "20260906_120001", time.Now()); err == nil {
		t.Fatal("expected read-back failure")
	}
	restored, err := os.ReadFile(file)
	if err != nil || string(restored) != string(original) {
		t.Fatal("failed repair did not restore original XMP")
	}
}

type failedXMPReader struct{ writer exifToolCommandRunner }

func (r failedXMPReader) RunFiles(dir string, names []string, args ...string) ([]byte, string, error) {
	if len(args) > 0 && args[0] == "-overwrite_original_in_place" {
		return r.writer.RunFiles(dir, names, args...)
	}
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
