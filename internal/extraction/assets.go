package extraction

import (
	"fmt"
	"sort"
	"strings"

	"pvfine/internal/pvf"
)

// AssetFile names the report that lists the client images the exported set
// references. It exists because an equipment (or any artwork-bearing) package is
// incomplete without them: the scripts alone cannot be shown in a client.
const AssetFile = "(6)资源与NPK对照.txt"

// AssetRecord is one distinct image path referenced by the exported set.
type AssetRecord struct {
	// Path is the value as the script writes it (`.img` path, case preserved
	// from the archive text).
	Path string `json:"path"`
	// NPK is the container file that holds this image, when the caller supplied
	// an index and the path was found in it. Empty means "no answer", which is
	// spelled out by Unknown.
	NPK string `json:"npk,omitempty"`
	// Unknown explains why NPK is empty. Dynamic paths (`%s`, `*`) are never a
	// miss: they cannot be decided statically and must not be downgraded.
	Unknown string `json:"unknown,omitempty"`
	// Sources lists up to a few "file [section]" pairs, so a reader can see
	// which script asked for the image.
	Sources []string `json:"sources,omitempty"`
}

// AssetInventory is the asset half of an export: what is referenced, how much of
// it can be located, and how much is inherently undecidable.
type AssetInventory struct {
	Records   []AssetRecord `json:"records"`
	Total     int           `json:"total"`
	Matched   int           `json:"matched"`
	Dynamic   int           `json:"dynamic"`
	Unmatched int           `json:"unmatched"`
	// Indexed reports whether the caller supplied a locator at all. Without one
	// the inventory still lists the referenced images, but every row says the
	// NPK index was not available.
	Indexed bool `json:"indexed"`
}

// AssetLocator answers "which container file holds this image path?". The
// archive cannot know: NPK files live outside the PVF, in a directory the user
// configures, and their contents are indexed by scanning. Injection keeps that
// knowledge in the layer that owns it.
type AssetLocator func(imagePath string) (npk string, found bool)

const assetSourceLimit = 3

// BuildAssetInventory re-reads the exported entries and collects their asset
// references. It follows the same rule as the registration fragments: the
// closure keeps only followable edges, so images have to come from a rescan —
// otherwise the very artifact that is supposed to say "which NPK to copy" would
// silently be empty.
func BuildAssetInventory(archive *pvf.Archive, scanner *Scanner, exported []int32, locate AssetLocator) (AssetInventory, error) {
	inventory := AssetInventory{Indexed: locate != nil}
	type aggregate struct {
		record  AssetRecord
		dynamic bool
	}
	byPath := map[string]*aggregate{}
	for _, index := range exported {
		result, err := scanner.ScanFile(index)
		if err != nil {
			return AssetInventory{}, err
		}
		for _, ref := range result.Refs {
			if ref.Kind != TargetAsset {
				continue
			}
			value := strings.TrimSpace(ref.Value)
			if value == "" {
				continue
			}
			key := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
			item := byPath[key]
			if item == nil {
				item = &aggregate{record: AssetRecord{Path: value}}
				if ref.State == StateUnknown {
					item.dynamic = true
					item.record.Unknown = "动态路径，只能在运行时判定"
				}
				byPath[key] = item
			}
			if len(item.record.Sources) < assetSourceLimit {
				item.record.Sources = append(item.record.Sources, fmt.Sprintf("%s [%s]", result.Path, ref.Section))
			}
		}
	}
	out := make([]AssetRecord, 0, len(byPath))
	keys := make([]string, 0, len(byPath))
	for key := range byPath {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := byPath[key]
		switch {
		case item.dynamic:
			inventory.Dynamic++
		case locate == nil:
			item.record.Unknown = "本次未提供 NPK 索引（未选择 NPK 目录或索引未就绪）"
		default:
			if npk, ok := locate(item.record.Path); ok {
				item.record.NPK = npk
				inventory.Matched++
			} else {
				item.record.Unknown = "NPK 索引里没有这个图片路径"
			}
		}
		out = append(out, item.record)
	}
	inventory.Records = out
	inventory.Total = len(out)
	inventory.Unmatched = inventory.Total - inventory.Matched - inventory.Dynamic
	return inventory, nil
}

// AssetReport renders the inventory as the text file that ships with the
// package.
func AssetReport(inventory AssetInventory, closureFiles int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# 资源与 NPK 对照（本次导出共 %d 个脚本文件，引用图片 %d 个）\n", closureFiles, inventory.Total)
	if !inventory.Indexed {
		builder.WriteString("# 注意：本次没有可用的 NPK 索引（应用里未选择 NPK 目录，或索引还在建立），所以只列图片路径。\n")
	} else {
		fmt.Fprintf(&builder, "# 索引结果：命中 %d 个，未在索引中 %d 个，动态模板 %d 个（动态的不算缺失，客户端运行时才知道实际图片）。\n",
			inventory.Matched, inventory.Unmatched, inventory.Dynamic)
	}
	listed := inventory.Records
	if len(listed) > assetReportLimit {
		listed = listed[:assetReportLimit]
		fmt.Fprintf(&builder, "# 受篇幅所限只展开前 %d 行，其余 %d 行见 manifest.json 的 assets 字段。\n",
			assetReportLimit, len(inventory.Records)-assetReportLimit)
	}
	builder.WriteString("#\n# 归属NPK\t图片路径\t来源（示例）\n")
	for _, record := range listed {
		target := record.NPK
		if target == "" {
			target = "(未归属) " + record.Unknown
		}
		fmt.Fprintf(&builder, "%s\t%s\t%s\n", target, record.Path, strings.Join(record.Sources, "; "))
	}
	return builder.String()
}

// assetReportLimit caps the text report so a whole-archive export does not
// produce a megabyte-sized sheet; the manifest keeps every record regardless.
const assetReportLimit = 5000

// manifestAssetLimit bounds how many records go into manifest.json. Past it the
// manifest keeps only the counts (and says how many were left out), because a
// whole-city export can reference tens of thousands of frames and the manifest
// has to stay readable as a single file.
const manifestAssetLimit = 10000
