package importing

import (
	"fmt"
	"os"

	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// FragmentRow 是一行登记表片段。
//
// 片段文件本身只有 id + 路径两列，文件名（`_registries/x.lst.fragment`）不带
// 目录信息，所以 ListPath 只能从 manifest 的 Selection.RegistryFragments 里
// 还原：90 与 110 布局下 `equipment/equipment.lst` 与 `list/equipment.lst` 的
// 文件名完全一样，靠文件名猜必然错表。
type FragmentRow struct {
	ListPath string // manifest 里记录的归档表路径(源归档的写法)
	ID       string
	Path     string // 表里的原始写法
}

// ReadFragments 读 manifest 声明的登记表片段（默认落在
// <root>/pvf/_registries/*.fragment，实际路径由 layout 决定的 Dest 解析）。
//
// 片段格式就是纯 .lst 行（提取侧 fragmentBody 写的），注释与空行由
// pvf.ParseListText 一并容忍，所以旧包新包一视同仁，不需要额外的格式标记。
//
// 声明了片段却读不到文件时必须报错：这说明产物不完整，装作没这回事会让用户
// 以为「这次导入不需要登记」。
func ReadFragments(root string, manifest *extraction.Manifest) ([]FragmentRow, error) {
	if manifest == nil || manifest.Selection == nil {
		return nil, nil
	}
	records := manifest.Selection.RegistryFragments
	if len(records) == 0 {
		return nil, nil
	}
	rows := make([]FragmentRow, 0, len(records))
	for _, record := range records {
		target, err := manifest.DestPath(root, extraction.ExportedFile{Dest: record.Dest})
		if err != nil {
			return nil, fmt.Errorf("登记表片段 %q: %w", record.Dest, err)
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("读取登记表片段 %q 失败: %w", record.Dest, err)
		}
		// 片段是 UTF-8 文本，和导入的文本条目同一口径：允许一个可选 BOM，
		// 非 UTF-8 直接拒绝。
		data, err = NormalizeTextPayload(data)
		if err != nil {
			return nil, fmt.Errorf("登记表片段 %q: %w", record.Dest, err)
		}
		pairs, err := pvf.ParseListText(string(data))
		if err != nil {
			return nil, fmt.Errorf("解析登记表片段 %q 失败: %w", record.Dest, err)
		}
		// 行数不必与记录严格相等（记录里的 rows 是提取时的快照，片段允许用户
		// 编辑），但「声明有行、解析出 0 行」说明格式变了，不能静默当成空表。
		if len(pairs) == 0 && record.Rows+record.EntityRows > 0 {
			return nil, fmt.Errorf("登记表片段 %q 声明了 %d 行，但解析出 0 行（片段格式可能已改变）",
				record.Dest, record.Rows+record.EntityRows)
		}
		for _, pair := range pairs {
			rows = append(rows, FragmentRow{ListPath: record.ListPath, ID: pair.ID, Path: pair.Path})
		}
	}
	return rows, nil
}
