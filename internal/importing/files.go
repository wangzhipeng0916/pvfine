package importing

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"pvfine/internal/pvf"
)

const (
	formText      = "text"
	listExtension = ".lst"
)

// utf8BOM 是 UTF-8 文件可能带的前导标记。提取侧不写它，但用户用编辑器另存很容易带出来；
// services/import.go 的文本模式同样只吃掉一个，这里必须对齐，否则同内容会被判成覆盖。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// NormalizeTextPayload 去掉一个前导 UTF-8 BOM 并校验编码。
//
// 判定与执行必须用同一份归一化结果：判定算摘要、执行再比摘要，两边口径不一致
// 就会出现"预览说没变、执行说变了"。
func NormalizeTextPayload(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return nil, errors.New("文本文件不是有效的 UTF-8")
	}
	return data, nil
}

// Classify 逐条判定候选条目相对当前归档的处置态。
//
// 每次只读一个载荷、比较完立刻丢弃：200,000 个文件的批次不能把内容全部留在内存里，
// 返回的 FileEntry 只携带元数据。
func Classify(a *pvf.Archive, source *Source) ([]FileEntry, error) {
	if a == nil {
		return nil, errors.New("导入中心: 目标归档为空")
	}
	if source == nil {
		return nil, errors.New("导入中心: 导入源为空")
	}
	entries := make([]FileEntry, 0, len(source.Candidates))
	for _, candidate := range source.Candidates {
		entries = append(entries, classify(a, candidate))
	}
	return entries, nil
}

// classify 的判定顺序不能调换：先排除不写的范围，再归一化类型，最后才读盘、比内容。
// 这样二进制条目即使磁盘缺失也会报 unsupported，而不是被当成「文件坏了」。
func classify(a *pvf.Archive, candidate Candidate) FileEntry {
	entry := FileEntry{Candidate: candidate}

	// 二进制条目不在 v1 范围：raw 的脚本 token 保存的是归档全局字符串池的绝对偏移，
	// 跨归档写入会静默错位，所以只列出、不写入（设计 §一 B1、§十一）。
	if candidate.Form != formText {
		return withState(entry, FileUnsupported, "二进制条目不在本次导入范围")
	}
	// .lst 是整表替换语义，必须转交登记表环节，不能当普通文件走 SetText（设计 §3.4）。
	if strings.EqualFold(path.Ext(candidate.ArchivePath), listExtension) {
		return withState(entry, FileUnsupported, "登记表由导入中心统一处理")
	}

	dataType := candidate.DataType
	if dataType == 0 {
		// 旧包的 manifest 没有 dataType。只能按现有导入路径的扩展名口径推断；
		// 这是兼容退路，不是首选，新包必须带上真实类型（设计 §3.1）。
		dataType = inferDataType(candidate.ArchivePath)
	}
	entry.DataType = dataType
	if dataType != pvf.TypeScript && dataType != pvf.TypeUnicode {
		// 声明为文本，类型却不是两种可编码文本之一，数据本身就自相矛盾。
		return withState(entry, FileUnsupported,
			fmt.Sprintf("文本条目却声明为数据类型 %d", dataType))
	}

	data, err := os.ReadFile(candidate.SourcePath)
	if err != nil {
		return withState(entry, FileInvalid, fmt.Sprintf("读取源文件失败: %v", err))
	}
	data, err = NormalizeTextPayload(data)
	if err != nil {
		// 导入侧最终会走 SetText，非 UTF-8 会被重新编码成垃圾；在计划阶段就拒绝，
		// 与 services/import.go 的文本模式保持一致。
		return withState(entry, FileInvalid, err.Error())
	}
	// 记下归一化之后的摘要，执行时重读比对：预览之后源文件被动过就不能装作没发生。
	entry.Digest = sha256.Sum256(data)

	index, exists := a.Find(candidate.ArchivePath)
	if !exists {
		entry.State = FileNew
		// 只有新增默认勾选；覆盖和内容相同都要求用户先看见再决定。
		entry.Selected = true
		return entry
	}

	// 类型不同就是真实变更，即使文本碰巧一样也不能判 unchanged：
	// 写回时 SetDataType 会改变归档条目的解释方式。
	if a.File(index).DataType != dataType {
		return withState(entry, FileOverwrite, "类型不同")
	}

	archiveText, err := a.Text(index)
	if err != nil {
		return withState(entry, FileInvalid, fmt.Sprintf("读取归档文本失败: %v", err))
	}
	// 比较基准必须是 a.Text：提取写盘的就是它，走用户渲染规则。
	// CanonicalText 刻意忽略渲染覆盖，用它比较会把「内容相同」永远判成覆盖（设计 D2）。
	if bytes.Equal(data, []byte(archiveText)) {
		// 「相同」是相对当前渲染规则的，换一套规则后同一份字节可能显示成别的文本。
		return withState(entry, FileUnchanged, "文本相同（与渲染规则相关）")
	}
	return withState(entry, FileOverwrite, "内容不同")
}

// inferDataType 复刻 services/import.go 的扩展名口径：.str / .nut 是本地化文本，
// 其余文本条目按脚本解释。只在 manifest 缺少 dataType 时使用。
func inferDataType(archivePath string) int32 {
	switch strings.ToLower(path.Ext(archivePath)) {
	case ".str", ".nut":
		return pvf.TypeUnicode
	}
	return pvf.TypeScript
}

func withState(entry FileEntry, state FileState, reason string) FileEntry {
	entry.State = state
	entry.Selected = false
	entry.Reason = reason
	return entry
}
