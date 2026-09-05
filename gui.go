package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	wailsoptions "github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

const progressEvent = "lrtimezonefix:progress"
const guiProgressInterval = 100 * time.Millisecond

type GUIApp struct {
	ctx              context.Context
	mu               sync.Mutex
	busy             bool
	closed           bool
	scanCancel       context.CancelFunc
	sessions         map[string]*guiSession
	progress         guiProgressGate
	thumbnailMu      sync.Mutex
	thumbnailSession *exifToolSession
}

type guiSession struct {
	Root    string
	Results []analysisResult
}

type AppInfo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	ExifToolReady bool   `json:"exifToolReady"`
	ExifToolPath  string `json:"exifToolPath"`
	ExifToolError string `json:"exifToolError"`
}

type GUISelection struct {
	Mode  string   `json:"mode"`
	Root  string   `json:"root"`
	Files []string `json:"files"`
	Label string   `json:"label"`
}

type GUIFileResult struct {
	Index          int                  `json:"index"`
	Path           string               `json:"path"`
	DisplayName    string               `json:"displayName"`
	State          string               `json:"state"`
	StateLabel     string               `json:"stateLabel"`
	Repairable     bool                 `json:"repairable"`
	Reason         string               `json:"reason"`
	DateTime       string               `json:"dateTimeOriginal"`
	CreateDate     string               `json:"createDate"`
	OriginalOffset string               `json:"offsetTimeOriginal"`
	CreateOffset   string               `json:"offsetTimeDigitized"`
	TargetLocal    string               `json:"targetLocal"`
	TargetOffset   string               `json:"targetOffset"`
	Shift          string               `json:"shift"`
	GPSReference   GPSTimezoneReference `json:"gpsTimezoneReference"`
}

type GUISummary struct {
	Total      int `json:"total"`
	Candidates int `json:"candidates"`
	Consistent int `json:"consistent"`
	Ambiguous  int `json:"ambiguous"`
	Unreadable int `json:"unreadable"`
}

type GUIScanReport struct {
	SessionID string          `json:"sessionId"`
	Root      string          `json:"root"`
	ExifTool  string          `json:"exifTool"`
	Summary   GUISummary      `json:"summary"`
	Files     []GUIFileResult `json:"files"`
}

type GUIRepairRequest struct {
	SessionID string `json:"sessionId"`
	Indices   []int  `json:"indices"`
}

type GUIRepairItem struct {
	Index   int    `json:"index"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

type GUIRepairReport struct {
	Cancelled        bool            `json:"cancelled"`
	Succeeded        int             `json:"succeeded"`
	Failed           int             `json:"failed"`
	BackupFolderName string          `json:"backupFolderName"`
	Results          []GUIRepairItem `json:"results"`
}

type GUIProgress struct {
	Phase   string `json:"phase"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Message string `json:"message"`
}

type guiProgressGate struct {
	mu      sync.Mutex
	last    GUIProgress
	lastAt  time.Time
	hasLast bool
}

func (g *guiProgressGate) allow(progress GUIProgress, at time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	allowed := !g.hasLast ||
		progress.Phase != g.last.Phase ||
		progress.Total != g.last.Total ||
		progress.Done < g.last.Done ||
		(progress.Total > 0 && progress.Done >= progress.Total) ||
		at.Sub(g.lastAt) >= guiProgressInterval
	if !allowed {
		return false
	}
	g.last = progress
	g.lastAt = at
	g.hasLast = true
	return true
}

func newGUIApp() *GUIApp {
	return &GUIApp{sessions: make(map[string]*guiSession)}
}

func (a *GUIApp) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
	a.closed = false
}

func runGUI() error {
	app := newGUIApp()
	return wails.Run(&wailsoptions.App{
		Title:                    appName,
		Width:                    1240,
		Height:                   820,
		MinWidth:                 980,
		MinHeight:                680,
		BackgroundColour:         &wailsoptions.RGBA{R: 242, G: 246, B: 250, A: 255},
		AssetServer:              &assetserver.Options{Assets: frontendAssets},
		OnStartup:                app.startup,
		OnShutdown:               app.shutdown,
		Bind:                     []interface{}{app},
		CSSDragProperty:          "--wails-draggable",
		CSSDragValue:             "drag",
		EnableDefaultContextMenu: false,
		Windows: &windows.Options{
			Theme:                windows.SystemDefault,
			BackdropType:         windows.Mica,
			IsZoomControlEnabled: false,
			DisablePinchZoom:     true,
			WindowClassName:      "LRTimezoneFixWindow",
		},
	})
}

func (a *GUIApp) GetAppInfo() AppInfo {
	info := AppInfo{Name: appName, Version: version}
	path, err := findExifTool()
	if err != nil {
		info.ExifToolError = err.Error()
		return info
	}
	info.ExifToolReady = true
	info.ExifToolPath = path
	return info
}

func (a *GUIApp) shutdown(context.Context) {
	a.mu.Lock()
	cancel := a.scanCancel
	a.scanCancel = nil
	a.sessions = nil
	a.closed = true
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	a.thumbnailMu.Lock()
	session := a.thumbnailSession
	a.thumbnailSession = nil
	a.thumbnailMu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

func (a *GUIApp) GetThumbnail(sessionID string, index int) (string, error) {
	file, err := a.sessionFile(sessionID, index)
	if err != nil {
		return "", err
	}
	exifTool, err := findExifTool()
	if err != nil {
		return "", err
	}
	session, err := a.getThumbnailSession(exifTool)
	if err != nil {
		return "", err
	}
	stdout, stderr, err := session.RunFiles(filepath.Dir(file), []string{filepath.Base(file)}, "-j", "-b", "-ThumbnailImage")
	if err != nil {
		a.invalidateThumbnailSession(session)
		return "", fmt.Errorf("读取 EXIF 缩略图失败：%v；%s", err, strings.TrimSpace(stderr))
	}
	var rows []struct {
		ThumbnailImage string `json:"ThumbnailImage"`
	}
	if err := json.Unmarshal(stdout, &rows); err != nil || len(rows) == 0 || rows[0].ThumbnailImage == "" {
		return "", nil
	}
	encoded := strings.TrimPrefix(rows[0].ThumbnailImage, "base64:")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 4 || len(raw) > 4<<20 || raw[0] != 0xff || raw[1] != 0xd8 {
		return "", nil
	}
	return "data:image/jpeg;base64," + encoded, nil
}

func (a *GUIApp) RevealFile(sessionID string, index int) error {
	file, err := a.sessionFile(sessionID, index)
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("照片不存在或无法访问：%w", err)
	}
	cmd := exec.Command("explorer.exe", "/select,"+file)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法打开资源管理器：%w", err)
	}
	return cmd.Process.Release()
}

func (a *GUIApp) sessionFile(sessionID string, index int) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok || index < 0 || index >= len(session.Results) {
		return "", errors.New("扫描结果已经过期，请重新扫描")
	}
	return session.Results[index].File, nil
}

func (a *GUIApp) getThumbnailSession(exifTool string) (*exifToolSession, error) {
	a.thumbnailMu.Lock()
	defer a.thumbnailMu.Unlock()
	// Keep thumbnailMu before mu. shutdown releases mu before acquiring
	// thumbnailMu, so this ordering cannot deadlock with shutdown.
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, errors.New("应用已经关闭")
	}
	if a.thumbnailSession != nil {
		return a.thumbnailSession, nil
	}
	session, err := newExifToolSession(exifTool)
	if err != nil {
		return nil, err
	}
	a.thumbnailSession = session
	return session, nil
}

func (a *GUIApp) invalidateThumbnailSession(session *exifToolSession) {
	a.thumbnailMu.Lock()
	if a.thumbnailSession == session {
		a.thumbnailSession = nil
	}
	a.thumbnailMu.Unlock()
	_ = session.Close()
}

func (a *GUIApp) ChooseFolder() (GUISelection, error) {
	path, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择要递归扫描的照片文件夹",
	})
	if err != nil || path == "" {
		return GUISelection{}, err
	}
	path = filepath.Clean(path)
	return GUISelection{Mode: "folder", Root: path, Label: path}, nil
}

func (a *GUIApp) ChooseFiles() (GUISelection, error) {
	files, err := wailsruntime.OpenMultipleFilesDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择要扫描的 JPG/JPEG",
		Filters: []wailsruntime.FileFilter{{
			DisplayName: "JPEG 照片 (*.jpg;*.jpeg)",
			Pattern:     "*.jpg;*.jpeg",
		}},
	})
	if err != nil || len(files) == 0 {
		return GUISelection{}, err
	}
	files, err = normalizeJPEGSelection(files)
	if err != nil {
		return GUISelection{}, err
	}
	root := commonParent(files)
	label := fmt.Sprintf("已选择 %d 张照片", len(files))
	if len(files) == 1 {
		label = files[0]
	}
	return GUISelection{Mode: "files", Root: root, Files: files, Label: label}, nil
}

func (a *GUIApp) Scan(selection GUISelection) (GUIScanReport, error) {
	scanCtx, err := a.beginScanOperation()
	if err != nil {
		return GUIScanReport{}, err
	}
	defer a.endOperation()

	exifTool, err := findExifTool()
	if err != nil {
		return GUIScanReport{}, err
	}
	if selection.Mode == "folder" {
		a.emitProgress("scan", 0, 0, "正在递归查找 JPG/JPEG……")
	}
	files, root, err := resolveGUISelectionContext(scanCtx, selection, func(visited, found int) {
		a.emitProgress("scan", 0, 0, fmt.Sprintf("正在查找照片：已检查 %d 项，发现 %d 张", visited, found))
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return GUIScanReport{}, errors.New("扫描已终止；未修改任何文件")
		}
		return GUIScanReport{}, err
	}
	if len(files) == 0 {
		return GUIScanReport{}, errors.New("所选范围内没有找到 JPG/JPEG 文件")
	}

	progressTotal := len(files) * 2
	a.emitProgress("scan", 0, progressTotal, fmt.Sprintf("正在读取 %d 张照片的元数据", len(files)))
	allMetadata, readErrors, err := readMetadataBatchWithProgressContext(scanCtx, exifTool, files, func(done, total int) {
		a.emitProgress("scan", done, progressTotal, fmt.Sprintf("已读取 %d / %d", done, total))
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return GUIScanReport{}, errors.New("扫描已终止；未修改任何文件")
		}
		return GUIScanReport{}, err
	}

	a.emitProgress("scan", len(files), progressTotal, fmt.Sprintf("正在分析 %d 张照片的时区信息", len(files)))
	results := make([]analysisResult, 0, len(files))
	for index, file := range files {
		if scanCtx.Err() != nil {
			return GUIScanReport{}, errors.New("扫描已终止；未修改任何文件")
		}
		meta, ok := allMetadata[file]
		readErr := readErrors[file]
		if !ok && readErr == nil {
			readErr = errors.New("ExifTool 没有返回该文件的记录")
		}
		if readErr != nil {
			results = append(results, analysisResult{File: file, State: stateUnreadable, Reason: readErr.Error()})
		} else {
			results = append(results, analyzePhotoMetadata(file, meta))
		}
		a.emitProgress("scan", len(files)+index+1, progressTotal, fmt.Sprintf("已分析 %d / %d", index+1, len(files)))
	}
	if scanCtx.Err() != nil {
		return GUIScanReport{}, errors.New("扫描已终止；未修改任何文件")
	}

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return GUIScanReport{}, errors.New("扫描已终止；未修改任何文件")
	}
	a.sessions = map[string]*guiSession{sessionID: {Root: root, Results: results}}
	a.mu.Unlock()
	report := buildGUIScanReport(sessionID, root, exifTool, results)
	a.emitProgress("scan", progressTotal, progressTotal, "扫描完成")
	return report, nil
}

func (a *GUIApp) Repair(request GUIRepairRequest) (GUIRepairReport, error) {
	if err := a.beginOperation(); err != nil {
		return GUIRepairReport{}, err
	}
	defer a.endOperation()

	a.mu.Lock()
	session, ok := a.sessions[request.SessionID]
	a.mu.Unlock()
	if !ok {
		return GUIRepairReport{}, errors.New("扫描结果已经过期，请重新扫描")
	}
	indices, err := validateRepairIndices(session.Results, request.Indices)
	if err != nil {
		return GUIRepairReport{}, err
	}

	answer, err := wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         "确认修复时区",
		Message:       fmt.Sprintf("将修复选中的 %d 张照片。\n\n每张原文件都会先备份，写入后还会验证元数据和 JPEG 图像数据。是否继续？", len(indices)),
		DefaultButton: "No",
		CancelButton:  "No",
	})
	if err != nil {
		return GUIRepairReport{}, err
	}
	if !strings.EqualFold(answer, "Yes") {
		return GUIRepairReport{Cancelled: true}, nil
	}

	exifTool, err := findExifTool()
	if err != nil {
		return GUIRepairReport{}, err
	}
	batchTime := time.Now()
	stamp := batchTime.Format("20060102_150405")
	report := GUIRepairReport{BackupFolderName: backupPrefix + stamp}
	reader := exifToolCommandRunner(directExifToolRunner{exifTool: exifTool})
	if persistent, sessionErr := newExifToolSession(exifTool); sessionErr == nil {
		reader = persistent
		defer persistent.Close()
	}
	for position, index := range indices {
		candidate := &session.Results[index]
		a.emitProgress("repair", position, len(indices), "正在修复 "+filepath.Base(candidate.File))
		item := GUIRepairItem{Index: index}
		if err := repairFileWithRunner(exifTool, reader, candidate, stamp, batchTime); err != nil {
			item.Error = err.Error()
			report.Failed++
		} else {
			item.Success = true
			report.Succeeded++
			candidate.State = stateConsistent
			candidate.Reason = "已由 LRTimezoneFix 修复并通过验证"
		}
		report.Results = append(report.Results, item)
		a.emitProgress("repair", position+1, len(indices), fmt.Sprintf("已处理 %d / %d", position+1, len(indices)))
	}
	a.emitProgress("repair", len(indices), len(indices), "修复完成")
	return report, nil
}

func (a *GUIApp) beginOperation() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("应用已经关闭")
	}
	if a.busy {
		return errors.New("另一个操作正在进行，请稍候")
	}
	a.busy = true
	return nil
}

func (a *GUIApp) beginScanOperation() (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("应用已经关闭")
	}
	if a.busy {
		return nil, errors.New("另一个操作正在进行，请稍候")
	}
	baseCtx := a.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)
	a.busy = true
	a.scanCancel = cancel
	return ctx, nil
}

func (a *GUIApp) CancelScan() bool {
	a.mu.Lock()
	cancel := a.scanCancel
	a.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (a *GUIApp) endOperation() {
	a.mu.Lock()
	cancel := a.scanCancel
	a.scanCancel = nil
	a.busy = false
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *GUIApp) emitProgress(phase string, done, total int, message string) {
	ctx := a.ctx
	if ctx == nil {
		return
	}
	progress := GUIProgress{Phase: phase, Done: done, Total: total, Message: message}
	if !a.progress.allow(progress, time.Now()) {
		return
	}
	wailsruntime.EventsEmit(ctx, progressEvent, progress)
}

func resolveGUISelection(selection GUISelection) ([]string, string, error) {
	return resolveGUISelectionContext(context.Background(), selection, nil)
}

func resolveGUISelectionContext(ctx context.Context, selection GUISelection, progress func(visited, found int)) ([]string, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	switch selection.Mode {
	case "folder":
		root, err := filepath.Abs(selection.Root)
		if err != nil {
			return nil, "", fmt.Errorf("无效文件夹：%w", err)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, "", errors.New("所选文件夹不存在或无法访问")
		}
		files, err := findJPEGsWithContext(ctx, root, progress)
		return files, filepath.Clean(root), err
	case "files":
		files, err := normalizeJPEGSelection(selection.Files)
		if err != nil {
			return nil, "", err
		}
		return files, commonParent(files), nil
	default:
		return nil, "", errors.New("请先选择照片文件夹或 JPG/JPEG 文件")
	}
}

func normalizeJPEGSelection(input []string) ([]string, error) {
	seen := make(map[string]bool)
	files := make([]string, 0, len(input))
	for _, value := range input {
		abs, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("无效文件路径 %q：%w", value, err)
		}
		abs = filepath.Clean(abs)
		ext := strings.ToLower(filepath.Ext(abs))
		if ext != ".jpg" && ext != ".jpeg" {
			return nil, fmt.Errorf("不是 JPG/JPEG 文件：%s", abs)
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf("文件不存在或无法访问：%s", abs)
		}
		key := strings.ToLower(abs)
		if !seen[key] {
			seen[key] = true
			files = append(files, abs)
		}
	}
	sort.Strings(files)
	return files, nil
}

func commonParent(files []string) string {
	if len(files) == 0 {
		return ""
	}
	common := filepath.Dir(files[0])
	for _, file := range files[1:] {
		dir := filepath.Dir(file)
		for !sameOrChildPath(common, dir) {
			parent := filepath.Dir(common)
			if parent == common {
				return filepath.VolumeName(common) + string(filepath.Separator)
			}
			common = parent
		}
	}
	return common
}

func sameOrChildPath(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateRepairIndices(results []analysisResult, input []int) ([]int, error) {
	seen := make(map[int]bool)
	indices := make([]int, 0, len(input))
	for _, index := range input {
		if index < 0 || index >= len(results) {
			return nil, errors.New("修复列表包含无效文件，请重新扫描")
		}
		if !results[index].Repairable() {
			return nil, fmt.Errorf("%s 已不属于可修复状态，请重新扫描", filepath.Base(results[index].File))
		}
		if !seen[index] {
			seen[index] = true
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return nil, errors.New("没有选择需要修复的照片")
	}
	sort.Ints(indices)
	return indices, nil
}

func buildGUIScanReport(sessionID, root, exifTool string, results []analysisResult) GUIScanReport {
	report := GUIScanReport{
		SessionID: sessionID,
		Root:      root,
		ExifTool:  exifTool,
		Summary:   GUISummary{Total: len(results)},
		Files:     make([]GUIFileResult, len(results)),
	}
	for index, result := range results {
		item := GUIFileResult{
			Index:          index,
			Path:           result.File,
			DisplayName:    relativeName(root, result.File),
			State:          guiStateName(result.State),
			StateLabel:     result.State.Label(),
			Repairable:     result.Repairable(),
			Reason:         result.Reason,
			DateTime:       result.Meta.DateTimeOriginal,
			CreateDate:     result.Meta.CreateDate,
			OriginalOffset: result.Meta.OffsetTimeOriginal,
			CreateOffset:   result.Meta.OffsetTimeDigitized,
			TargetLocal:    result.TargetLocal,
			TargetOffset:   result.TargetOffset,
			Shift:          formatSignedMinutes(result.ShiftMinutes),
			GPSReference:   result.GPSReference,
		}
		report.Files[index] = item
		switch result.State {
		case stateInitialResidue, stateMarkerMaintenance:
			report.Summary.Candidates++
		case stateConsistent:
			report.Summary.Consistent++
		case stateAmbiguous:
			report.Summary.Ambiguous++
		case stateUnreadable:
			report.Summary.Unreadable++
		}
	}
	return report
}

func guiStateName(state analysisState) string {
	switch state {
	case stateInitialResidue:
		return "candidate"
	case stateMarkerMaintenance:
		return "maintenance"
	case stateConsistent:
		return "consistent"
	case stateAmbiguous:
		return "ambiguous"
	case stateUnreadable:
		return "unreadable"
	default:
		return "unknown"
	}
}
