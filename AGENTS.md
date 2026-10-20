# Repository Guidelines

## 项目结构与模块组织

- `main.go` 是 Wails 入口，负责窗口配置和服务注册。
- `internal/pvf/` 是独立可测试的 Go 解析与打包内核，包含加密、zlib 分块、字符串池和编解码逻辑；测试与实现同目录放置。
- `services/` 提供 `ArchiveService`、`EditorService` 及共享并发状态；服务测试位于 `services/services_test.go`。
- `frontend/src/` 是 Vue 3 + TypeScript 界面，按 `components/` 和 `stores/` 组织；`frontend/bindings/` 是 Wails 生成的绑定，不要手工修改。
- `build/` 保存 Wails 各平台构建配置，`docs/` 保存格式说明。不要提交新的构建产物或临时归档文件。
- 更新日志分两份：`CHANGELOG.md` 是上游 `dof-dev/pvfine` 的发布记录，本分支相对上游的改动写在 `CHANGELOG.fork.md`。分开是为了合并上游时不撞车，上游条目不要复制进本分支那份。两份都以 `?raw` 在构建时嵌进应用（设置页「系统与维护 → 文档与更新记录」），所以文件必须被 git 跟踪：仓库外的构建缺它会直接失败；注意用户全局 gitignore 含 `*.local.md`，这类文件名会静默漏掉。

## 构建、测试与开发命令

- `wails3 task dev`：启动 Wails 热重载开发环境。
- `go test ./...`：运行全部 Go 单元测试。
- `PVF_TESTFILE=/path/to/Script.pvf go test ./...`：启用真实 PVF 归档的集成、编辑和字节级回归测试；未设置时相关测试会跳过。
- `cd frontend && npm ci`：按锁文件安装前端依赖。
- `cd frontend && npm run build`：执行 `vue-tsc` 类型检查并生成生产前端资源。
- `wails3 task build`：构建完整桌面应用；`go build ./...` 仅验证 Go 编译。
- `./scripts/build-local.sh`：本地开发版构建，产物 `bin/pvfine`（Windows 上是 `pvfine.exe`）。除编译外还负责补 Go 代理、把构建中间物放到系统 temp（本机是内存盘，重启即清，不在仓库盘留残渣）、还原 `wails3` 顺带产生的行尾与锁文件噪音。本地版版本号取占位 `0.0.0`，因此不接入更新频道；发版不要用它（发版走 release skill，正式版靠 `-X main.appVersion` 注入真实版本）。

## 编码风格与命名约定

Go 代码使用 `gofmt`（可运行 `go fmt ./...`），包名使用小写，导出标识符使用 PascalCase。Vue 单文件组件使用 `<script setup lang="ts">`，组件采用 PascalCase，Pinia store 使用 `useXxxStore`。保持现有 2 空格前端缩进和严格 TypeScript 配置；生成的 bindings 通过 Wails 命令更新。

## 测试约定

测试文件命名为 `*_test.go`，测试函数使用 `TestXxx`。解析内核优先覆盖合成归档往返、编辑保存和编码边界；涉及真实数据时使用 `PVF_TESTFILE`，不要把个人路径硬编码。新增行为应同时补充针对性测试，并在改动范围允许时运行 `go test ./...` 和前端构建。

## 提交与 Pull Request

现有提交使用简短、动词开头的中文说明（如 `优化解析`、`按 section 名称配置格式规则`），初始化提交使用 `chore:` 前缀。提交应聚焦单一变更。PR 描述需说明用户可见影响、主要实现、验证命令及是否需要 `PVF_TESTFILE`；涉及界面时附截图或录屏，并说明兼容性影响。不要提交 `frontend/dist`、`bin`、依赖目录或 secrets。

用户能察觉的功能或行为变化，在同一个提交里给 `CHANGELOG.fork.md` 的 `## [未发布]` 段补一条，按 `✨ 新特性` / `🎨 界面与交互` / `🔨 重构与维护` 归类；写结论与实测依据，不要搬运提交说明原文。

## 安全与配置

不要读取、打印或提交 `.env`、密钥、token、凭据或密码。真实 `Script.pvf` 仅用于本地验证；分享日志和 PR 前检查路径、归档内容及构建输出中没有敏感信息。
