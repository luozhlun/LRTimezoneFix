# LRTimezoneFix

`LRTimezoneFix` 是一个 Windows 桌面工具，用于修复 Lightroom 调整拍摄时间后导出的 JPG/JPEG 中，日期时间已经平移、但时区及关联字段没有同步的问题。

当前版本：`1.5.0`

## 功能

- 现代化 Windows GUI，支持系统浅色/深色主题。
- 使用 Windows 原生选择窗口，可递归扫描文件夹，也可只选多张 JPG/JPEG。
- 扫描阶段完全只读，显示实时进度。
- 递归扫描由用户选择的全部子目录，并可随时点击“终止扫描”。
- ExifTool 全程在后台静默运行，扫描时复用常驻会话，不反复弹出命令行窗口。
- 将结果分类为“需要修复”“时间一致”“需人工检查”“读取失败”。
- 支持文件名搜索、状态筛选、逐文件元数据详情和选择性修复。
- 结果列表按可见区域懒加载 JPG 内嵌 EXIF 缩略图，并可在资源管理器中定位照片。
- 有 GPS 坐标时，离线推算 IANA 地理时区、拍摄日期对应的 UTC 偏移和夏令时状态，仅供人工参考。
- 拍摄时间位于时区偏移变更前后 24 小时、重复时段或跳过时段时，在列表和详情中提示人工核对。
- 照片没有 GPS、GPS 无效或缺少可用日期时会明确说明，不影响扫描、默认勾选或修复结论。
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
   扫描超大目录或磁盘根目录时，可以点击进度条右侧的“终止扫描”。
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

## GPS 时区参考

程序会读取照片中的 GPS 经纬度，并通过内嵌的离线时区边界查找 IANA 时区，再结合 GPS UTC 时间（优先）或拍摄当地时间判断当时的 UTC 偏移和夏令时状态。结果显示在照片的“元数据详情”中。

这项信息与自动修复推断完全解耦：不会改变照片分类、目标偏移、默认勾选或写入内容。若参考时间距离时区偏移变更不足 24 小时，或落入夏令时回拨的重复时段、拨快的跳过时段，列表会显示黄色标签，并建议人工核对。

GPS 边界查询使用 [tzf](https://github.com/ringsaturn/tzf)；其边界数据来自 [timezone-boundary-builder](https://github.com/evansiroky/timezone-boundary-builder) 和 OpenStreetMap 贡献者，数据依 [ODbL 1.0](https://opendatacommons.org/licenses/odbl/1-0/) 提供。默认简化边界在时区交界附近可能存在约百米级误差，因此所有 GPS 推算均只作为参考。

## 备份与验证

程序对每张待修照片执行：

1. 在照片所在目录创建 `ExifTool_Backup_YYYYMMDD_HHMMSS`。
2. 复制原文件并用 SHA-256 验证备份一致，同时写入 `LRTimezoneFix_Backup.log`。
3. 同步 EXIF、XMP、IPTC 和 `IPTCDigest`。
4. 写入 `LRTimezoneFix/1;` 审计标记；已有 `UserComment` 会保留。
5. 比较 ExifTool `ImageDataHash`，确认 JPEG 图像数据没有变化。
6. 验证不应修改的处理时间、XMP 历史和 GPS 时间。
7. 任一检查失败时，自动从备份恢复并再次校验 SHA-256。

审计标记示例：

```text
LRTimezoneFix/1; action=timezone-normalize; from=+08:00; to=+09:00; wall-shift=+01:00; utc-preserved=yes; normalized=DateTimeOriginal,CreateDate; version=1.5.0; repaired-at=2026-01-01T12:00:00+08:00
```

## 恢复照片

备份文件保存在原照片同目录下的：

```text
ExifTool_Backup_YYYYMMDD_HHMMSS
```

需要恢复时，关闭 Lightroom 或其他可能占用照片的软件，把备份 JPG 复制回上一级目录并覆盖当前文件。

## 开发依赖与编译

开发环境：

- Windows 10/11 x64。
- Go 1.26 或与 `go.mod` 兼容的更新版本。
- Wails v2.13 由 `go.mod` 管理，不需要全局安装 Wails CLI。
- 不需要 Node.js、npm 或 GCC；前端是直接嵌入的原生 HTML/CSS/JavaScript。
- `go-winres` 仅在修改应用图标、Windows 版本信息或清单时需要，普通编译不需要。

在项目目录下载 Go 依赖：

```powershell
go mod download
```

运行测试：

```powershell
go test -tags "desktop,production" ./...
go vet -tags "desktop,production" ./...
```

编译无控制台的正式 GUI：

```powershell
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o LRTimezoneFix.exe .
```

需要进行命令行调试时，可以构建带控制台版本：

```powershell
go build -tags "desktop,production" -trimpath -o LRTimezoneFix-cli.exe .
LRTimezoneFix-cli.exe -root "D:\照片目录" -analyze-only -no-pause
```

前端文件直接位于 `frontend/dist`，不依赖 npm 构建流程。

Windows 图标、版本信息和高 DPI 清单已经保存在 `rsrc_windows_amd64.syso`。因此从仓库直接编译时不需要安装 `go-winres`。

修改图标或升级版本信息时，先安装固定版本的 [go-winres](https://github.com/tc-hib/go-winres)：

```powershell
go install github.com/tc-hib/go-winres@v0.3.3
```

修改 `build/appicon.png` 或 `winres/winres.json` 后，重新生成 Windows 资源：

```powershell
go-winres make --arch amd64 --out rsrc
```

生成的 `rsrc_windows_amd64.syso` 应随源码提交。升级版本时还要同步修改 `main.go` 中的版本号和 `winres/winres.json` 中的文件/产品版本，然后重新编译 EXE。

## GitHub 仓库说明

`.gitignore` 已排除：

- 本地照片测试目录和所有 JPG/JPEG/RAW 样本。
- 本地测试、构建缓存和模块缓存目录。
- `ExifTool_Backup_*` 备份目录。
- EXE、日志、覆盖率和编辑器临时文件。

源码、`go.mod`、`go.sum`、README 和内嵌前端应提交到仓库；正式 EXE 建议作为 GitHub Release 附件发布，而不是直接提交进 Git。

## 版本记录

### 1.5.0

- 新增完全离线的 GPS 地理时区参考，显示 IANA 时区、参考当地时间、UTC 偏移和夏令时状态。
- 在时区偏移变更前后 24 小时以及重复/跳过的当地时间段显示人工核对警告，并在照片列表增加黄色提示标签。
- 完整处理无 GPS、无效坐标、缺少日期和边界查询失败等情况；GPS 结果不参与自动修复判断。

### 1.4.0

- 结果列表新增 JPG 内嵌 EXIF 缩略图，按可见区域懒加载，不拖慢初始扫描。
- 新增“在资源管理器中显示”按钮，可打开照片目录并选中文件。
- 每个备份目录新增 `LRTimezoneFix_Backup.log`，记录版本、路径、哈希、时区修正和验证/恢复状态。

### 1.3.0

- 递归扫描不再跳过以 `.` 开头的目录，完整遵循用户选择的扫描范围。
- 新增“终止扫描”按钮，可中止目录查找或 ExifTool 元数据分析。
- 扫描磁盘根目录时自动跳过无法访问的系统子目录，不因单个权限错误中断。
- 元数据读取批次调整为 16 张，改善扫描进度反馈。

### 1.2.0

- ExifTool 子进程改为后台静默运行，扫描时不再闪现终端窗口。
- 扫描和写后复查复用 ExifTool `-stay_open` 会话，减少反复启动开销。
- 保留 Windows 原生的文件夹与 JPG 选择窗口。

### 1.1.0

- 新增 Wails Windows GUI。
- 新增文件夹/多文件选择、扫描进度、筛选、搜索和详情抽屉。
- 新增选择性修复、原生二次确认和修复后自动复查。
- 完善 GitHub 忽略规则和构建说明。

### 1.0.0

- 首个 Go 命令行版本。
- 实现通用时区推断、备份、同步、审计和写后验证。
