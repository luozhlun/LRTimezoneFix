# LRTimezoneFix

`LRTimezoneFix` 是一个 Windows 桌面工具，用于修复 Lightroom 调整拍摄时间后导出的 JPG/JPEG 中，日期时间已经平移、但时区及关联字段没有同步的问题。

当前版本：`1.1.0`

## 功能

- 现代化 Windows GUI，支持系统浅色/深色主题。
- 选择一个文件夹递归扫描，或一次选择多张 JPG/JPEG。
- 扫描阶段完全只读，显示实时进度。
- 将结果分类为“需要修复”“时间一致”“需人工检查”“读取失败”。
- 支持文件名搜索、状态筛选、逐文件元数据详情和选择性修复。
- 不写死日本、巴黎或任何城市，依据实际日期时间差通用推断 UTC 偏移。
- 修复前逐张备份；写入后验证元数据、摘要和 JPEG 图像数据。
- 写入独有的 `LRTimezoneFix/1;` 审计标记。

界面使用 [Wails v2](https://wails.io/) 和 Windows WebView2，前端为项目内嵌的原生 HTML/CSS/JavaScript。发布后的程序是单个 EXE，不需要安装 Node.js，也不会在运行时访问网络。

## 使用方法

运行要求：

- Windows 10/11 x64。
- 系统已安装 WebView2 Runtime；Windows 10/11 通常已经具备。
- [ExifTool](https://exiftool.org/) 已加入系统 `PATH`。

操作步骤：

1. 双击 `LRTimezoneFix.exe`。
2. 点击“选择文件夹”递归扫描，或者点击“选择 JPG”选择特定照片。
3. 点击“开始扫描”。此阶段不会修改任何文件。
4. 查看摘要、筛选结果，并点击照片查看关键字段和推断依据。
5. 勾选需要处理的照片，点击“修复所选照片”。
6. 在 Windows 原生确认框中确认后，程序才会开始备份和写入。
7. 修复结束后，程序会自动再次扫描，显示最终状态。

## 典型问题

Lightroom 调整时区后，导出的 JPG 可能出现：

```text
DateTimeOriginal       2025:10:01 13:39:15  +08:00
CreateDate             2025:10:01 12:39:15  +08:00
```

时间已经增加一小时，但两个时区标签仍残留旧的 `+08:00`。程序会推断并统一为：

```text
DateTimeOriginal       2025:10:01 13:39:15  +09:00
CreateDate             2025:10:01 13:39:15  +09:00
```

这样 UTC 时刻保持不变：`12:39 +08:00` 与 `13:39 +09:00` 都是 `04:39 UTC`。

## 通用推断规则

核心计算：

```text
墙上时间变化 = DateTimeOriginal - CreateDate
目标 UTC 偏移 = OffsetTimeDigitized + 墙上时间变化
```

支持例如：

- 中国 `+08:00` → 日本 `+09:00`：墙上时间 `+1` 小时。
- 美国中部 `-06:00` → 巴黎 `+01:00`：墙上时间 `+7` 小时。
- 中国 `+08:00` → 巴黎 `+01:00`：墙上时间 `-7` 小时。
- 跨午夜、跨月和跨年：使用完整年月日计算，不只比较小时数。

只有以下证据同时成立时才会自动列为修复候选：

1. JPG 中存在 Lightroom 或 RAW 来源证据。
2. `DateTimeOriginal` 与 `CreateDate` 存在整分钟差值。
3. `OffsetTimeOriginal` 与 `OffsetTimeDigitized` 相同，表明二者共同残留旧偏移。
4. 推断结果位于合法的 `-14:00` 到 `+14:00` 范围。

两个时区字段已经不一致、证据不足或差值无法唯一解释时，程序会标记为“需人工检查”并拒绝自动修改。

## 修复字段

程序通过 ExifTool 的 MWG 写法同步：

- EXIF `DateTimeOriginal`、`OffsetTimeOriginal`
- EXIF `CreateDate`、`OffsetTimeDigitized`
- XMP Photoshop `DateCreated`
- XMP `CreateDate`
- IPTC `DateCreated`、`TimeCreated`
- IPTC `DigitalCreationDate`、`DigitalCreationTime`
- Photoshop `IPTCDigest`

`ModifyDate`、`MetadataDate`、`HistoryWhen` 和普通 `OffsetTime` 不会改成拍摄地时区，因为它们记录的是 Lightroom 导出或后续处理时间。GPS 日期时间也不会修改。

## 备份与验证

程序对每张待修照片执行：

1. 在照片所在目录创建 `ExifTool_Backup_YYYYMMDD_HHMMSS`。
2. 复制原文件并用 SHA-256 验证备份一致。
3. 同步 EXIF、XMP、IPTC 和 `IPTCDigest`。
4. 写入 `LRTimezoneFix/1;` 审计标记；已有 `UserComment` 会保留。
5. 比较 ExifTool `ImageDataHash`，确认 JPEG 图像数据没有变化。
6. 验证不应修改的处理时间、XMP 历史和 GPS 时间。
7. 任一检查失败时，自动从备份恢复并再次校验 SHA-256。

审计标记示例：

```text
LRTimezoneFix/1; action=timezone-normalize; from=+08:00; to=+09:00; wall-shift=+01:00; utc-preserved=yes; normalized=DateTimeOriginal,CreateDate; version=1.1.0; repaired-at=2026-01-01T12:00:00+08:00
```

## 恢复照片

备份文件保存在原照片同目录下的：

```text
ExifTool_Backup_YYYYMMDD_HHMMSS
```

需要恢复时，关闭 Lightroom 或其他可能占用照片的软件，把备份 JPG 复制回上一级目录并覆盖当前文件。

## 开发与编译

要求：

- Go 1.26 或兼容版本
- Windows 10/11 x64
- 构建时可访问 Go 模块源

下载依赖：

```powershell
go mod download
```

运行测试：

```powershell
go test ./...
go vet ./...
```

编译无控制台的正式 GUI：

```powershell
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o LRTimezoneFix.exe .
```

需要调试原有命令行入口时，可以构建带控制台版本：

```powershell
go build -tags "desktop,production" -trimpath -o LRTimezoneFix-cli.exe .
LRTimezoneFix-cli.exe -root "D:\照片目录" -analyze-only -no-pause
```

前端文件直接位于 `frontend/dist`，不依赖 npm 构建流程。

Windows 图标、版本信息和高 DPI 清单已经保存在 `rsrc_windows_amd64.syso`。版本升级时可以使用 [go-winres](https://github.com/tc-hib/go-winres) 重新生成该资源文件。

## GitHub 仓库说明

`.gitignore` 已排除：

- 本地照片测试目录和所有 JPG/JPEG/RAW 样本。
- 本地测试、构建缓存和模块缓存目录。
- `ExifTool_Backup_*` 备份目录。
- EXE、日志、覆盖率和编辑器临时文件。

源码、`go.mod`、`go.sum`、README 和内嵌前端应提交到仓库；正式 EXE 建议作为 GitHub Release 附件发布，而不是直接提交进 Git。

## 版本记录

### 1.1.0

- 新增 Wails Windows GUI。
- 新增文件夹/多文件选择、扫描进度、筛选、搜索和详情抽屉。
- 新增选择性修复、原生二次确认和修复后自动复查。
- 完善 GitHub 忽略规则和构建说明。

### 1.0.0

- 首个 Go 命令行版本。
- 实现通用时区推断、备份、同步、审计和写后验证。
