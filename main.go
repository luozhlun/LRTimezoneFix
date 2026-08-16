package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	appName       = "LRTimezoneFix"
	version       = "1.4.0"
	backupPrefix  = "ExifTool_Backup_"
	auditPrefix   = "LRTimezoneFix/1;"
	defaultMarker = "timezone-normalize"
)

type options struct {
	root        string
	yes         bool
	noPause     bool
	analyzeOnly bool
}

func main() {
	if len(os.Args) == 1 {
		if err := runGUI(); err != nil {
			fmt.Fprintf(os.Stderr, "%s 启动失败：%v\n", appName, err)
		}
		return
	}
	runCLI()
}

func runCLI() {
	configureConsole()
	opts, exitEarly := parseOptions()
	if exitEarly {
		return
	}

	pause := !opts.noPause && len(os.Args) == 1
	if pause {
		defer waitForEnter()
	}

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "\n错误：%v\n", err)
		os.Exit(1)
	}
}

func parseOptions() (options, bool) {
	var opts options
	flag.StringVar(&opts.root, "root", "", "要扫描的根目录；默认是 EXE 所在目录")
	flag.BoolVar(&opts.yes, "yes", false, "跳过交互确认（谨慎使用）")
	flag.BoolVar(&opts.noPause, "no-pause", false, "结束时不等待按键")
	flag.BoolVar(&opts.analyzeOnly, "analyze-only", false, "只分析，不提示和写入")
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s\n", appName, version)
		return opts, true
	}
	return opts, false
}

func run(opts options) error {
	root, err := resolveRoot(opts.root)
	if err != nil {
		return err
	}

	exifTool, err := findExifTool()
	if err != nil {
		return err
	}

	fmt.Printf("%s %s\n", appName, version)
	fmt.Println("Lightroom 导出 JPG 时区检查与修复工具")
	fmt.Printf("扫描目录：%s\n", root)
	fmt.Printf("ExifTool：%s\n\n", exifTool)

	files, err := findJPEGs(root)
	if err != nil {
		return fmt.Errorf("枚举 JPG 失败：%w", err)
	}
	if len(files) == 0 {
		fmt.Println("没有找到 JPG/JPEG 文件。")
		return nil
	}

	fmt.Printf("正在只读分析 %d 个文件……\n", len(files))
	allMetadata, readErrors := readMetadataBatch(exifTool, files)
	results := make([]analysisResult, 0, len(files))
	for i, file := range files {
		meta, ok := allMetadata[file]
		readErr := readErrors[file]
		if !ok && readErr == nil {
			readErr = fmt.Errorf("ExifTool 没有返回该文件的记录")
		}
		if readErr != nil {
			results = append(results, analysisResult{
				File:   file,
				State:  stateUnreadable,
				Reason: readErr.Error(),
			})
		} else {
			results = append(results, analyzeMetadata(file, meta))
		}
		if (i+1)%10 == 0 || i+1 == len(files) {
			fmt.Printf("  已分析 %d/%d\n", i+1, len(files))
		}
	}

	candidates := printAnalysis(root, results)
	if len(candidates) == 0 {
		fmt.Println("\n没有需要自动修复的文件；未修改任何内容。")
		return nil
	}
	if opts.analyzeOnly {
		fmt.Println("\n只读分析完成；未修改任何内容。")
		return nil
	}

	fmt.Println("\n修复将执行以下操作：")
	fmt.Println("  1. 将 DateTimeOriginal 与 CreateDate 两组时间统一为目标当地时间和时区")
	fmt.Println("  2. 同步 EXIF、XMP、IPTC，并刷新 Photoshop IPTCDigest")
	fmt.Printf("  3. 在 EXIF UserComment 写入 %s 审计标记\n", auditPrefix)
	fmt.Printf("  4. 在每个照片目录建立 %sYYYYMMDD_HHMMSS 备份目录\n", backupPrefix)
	fmt.Println("  5. 验证图像数据哈希、时间字段、摘要和审计标记")

	if !opts.yes && !confirm("\n修复上面列出的全部文件？输入 y 确认：") {
		fmt.Println("已取消；未修改任何内容。")
		return nil
	}

	batchTime := time.Now()
	stamp := batchTime.Format("20060102_150405")
	succeeded := 0
	failed := 0
	reader := exifToolCommandRunner(directExifToolRunner{exifTool: exifTool})
	if persistent, sessionErr := newExifToolSession(exifTool); sessionErr == nil {
		reader = persistent
		defer persistent.Close()
	}
	fmt.Println("\n================ 开始修复 ================")
	for i := range candidates {
		candidate := &candidates[i]
		if err := repairFileWithRunner(exifTool, reader, candidate, stamp, batchTime); err != nil {
			failed++
			fmt.Printf("失败：%s\n  %v\n", relativeName(root, candidate.File), err)
		} else {
			succeeded++
			fmt.Printf("完成：%s  %s -> %s\n", relativeName(root, candidate.File), candidate.SourceOffset, candidate.TargetOffset)
		}
	}

	fmt.Println("\n================ 修复结果 ================")
	fmt.Printf("成功：%d\n失败并恢复：%d\n", succeeded, failed)
	fmt.Printf("备份目录名称：%s%s\n", backupPrefix, stamp)
	if failed > 0 {
		return fmt.Errorf("有 %d 个文件未能完成修复，程序已尝试从备份恢复", failed)
	}
	return nil
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("无法确定 EXE 所在目录：%w", err)
		}
		root = filepath.Dir(exe)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("无效目录 %q：%w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("无法访问目录 %q：%w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("指定路径不是目录：%s", abs)
	}
	return filepath.Clean(abs), nil
}

func findJPEGs(root string) ([]string, error) {
	return findJPEGsWithContext(context.Background(), root, nil)
}

func findJPEGsWithContext(ctx context.Context, root string, progress func(visited, found int)) ([]string, error) {
	var files []string
	visited := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			// Large roots such as a system drive commonly contain protected
			// directories. Skip an inaccessible subtree instead of aborting the
			// entire user-requested scan.
			return nil
		}
		visited++
		if entry.IsDir() {
			if path != root && shouldSkipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".jpg" || ext == ".jpeg" {
			files = append(files, path)
		}
		if progress != nil && (visited == 1 || visited%500 == 0) {
			progress(visited, len(files))
		}
		return nil
	})
	if progress != nil {
		progress(visited, len(files))
	}
	return files, err
}

func shouldSkipDirectory(name string) bool {
	return isBackupDirectory(name)
}

func isBackupDirectory(name string) bool {
	if !strings.HasPrefix(name, backupPrefix) {
		return false
	}
	_, err := time.Parse("20060102_150405", strings.TrimPrefix(name, backupPrefix))
	return err == nil
}

func printAnalysis(root string, results []analysisResult) []analysisResult {
	counts := map[analysisState]int{}
	var candidates []analysisResult
	for _, result := range results {
		counts[result.State]++
		if result.Repairable() {
			candidates = append(candidates, result)
		}
	}

	fmt.Println("\n================ 分析结果 ================")
	fmt.Printf("总文件数：%d\n", len(results))
	fmt.Printf("时区异常候选：%d\n", counts[stateInitialResidue])
	fmt.Printf("本工具摘要维护：%d\n", counts[stateMarkerMaintenance])
	fmt.Printf("时间已经一致：%d\n", counts[stateConsistent])
	fmt.Printf("证据不足/情况含糊：%d\n", counts[stateAmbiguous])
	fmt.Printf("读取失败：%d\n", counts[stateUnreadable])

	for _, result := range results {
		if !result.Repairable() && result.State != stateAmbiguous && result.State != stateUnreadable {
			continue
		}
		fmt.Printf("\n[%s] %s\n", result.State.Label(), relativeName(root, result.File))
		if result.Meta.DateTimeOriginal != "" {
			fmt.Printf("  DateTimeOriginal : %s  %s\n", result.Meta.DateTimeOriginal, result.Meta.OffsetTimeOriginal)
			fmt.Printf("  CreateDate       : %s  %s\n", result.Meta.CreateDate, result.Meta.OffsetTimeDigitized)
		}
		if result.Repairable() {
			fmt.Printf("  计划             : 两组统一为 %s%s\n", result.TargetLocal, result.TargetOffset)
		}
		if result.Reason != "" {
			fmt.Printf("  说明             : %s\n", result.Reason)
		}
	}
	return candidates
}

func relativeName(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

func waitForEnter() {
	fmt.Print("\n按 Enter 键关闭窗口……")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
