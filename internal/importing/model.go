// Package importing 负责「导入中心」的只读前段：把一份提取产物解析成候选条目，
// 并逐条判定它相对目标归档是新增、覆盖还是内容未变。
//
// 本包只读归档：判定不修改任何条目，写回由服务层在 CloneForBatch 得到的克隆体上完成。
// 与提取侧（internal/extraction）保持同一套路径与类型语义，是这批判定成立的前提。
package importing

import "pvfine/internal/extraction"

// Candidate 是一个待导入条目。
type Candidate struct {
	ArchivePath string // 目标归档路径(来自 manifest.Files[].Path)
	SourcePath  string // 磁盘上的实际文件(绝对路径)
	Form        string // "text" | "raw"
	// DataType 1 TypeScript / 3 TypeUnicode / 0 未知
	DataType int32
	Size     int64
	// Digest 是载荷（去 BOM 后）的 SHA-256。执行时重读并比对，源文件在预览之后
	// 被改动就必须整批作废：登记表判定依赖"本次实际应用的文件集合"，逐条降级是错的。
	Digest        [32]byte
	Origin        string // "manifest"
	Category      string
	AutoCompleted bool
}

// Source 是一次导入的解析结果。
type Source struct {
	Kind       string // "extraction"
	Root       string // 导出根目录
	Manifest   *extraction.Manifest
	Candidates []Candidate
}

// FileState 是一个条目相对当前归档的处置态。
type FileState string

const (
	// FileNew 表示目标归档里还没有这个路径，默认勾选写入。
	FileNew FileState = "new"
	// FileOverwrite 表示归档里有同路径条目，但内容或类型不同，需用户显式勾选。
	FileOverwrite FileState = "overwrite"
	// FileUnchanged 表示按当前渲染规则读出的文本与磁盘完全一致，写入只会制造假变更。
	FileUnchanged FileState = "unchanged"
	// FileUnsupported 表示该条目不在本次导入范围（二进制、登记表等），列出但不写。
	FileUnsupported FileState = "unsupported"
	// FileInvalid 表示连判定都无法完成（源文件读不到、归档文本解不开）。
	FileInvalid FileState = "invalid"
)

// FileEntry 是一条候选及其判定结果。
type FileEntry struct {
	Candidate
	State    FileState
	Selected bool
	Reason   string
}
