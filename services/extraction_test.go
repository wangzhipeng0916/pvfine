package services

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	annotationrules "pvfine/internal/annotations"
	"pvfine/internal/extraction"
	"pvfine/internal/pvf"
)

// extractionFixture builds an archive whose chain exercises the shipped
// catalog end to end: a map pulls a passive object through its registry, and
// that object pulls an animation. Three entries, three categories.
func extractionFixture(t *testing.T) (*core, *ExtractionService) {
	t.Helper()
	a := pvf.New()
	for p, text := range map[string]string{
		"map/map.lst":                     "1\t`forest/a.map`\n",
		"map/forest/a.map":                "[dungeon]\n\t1\n[passive object]\n\t10\n\t1\n\t2\n\t3\n",
		"passiveobject/passiveobject.lst": "10\t`obj/door.obj`\n",
		"passiveobject/obj/door.obj":      "[use animation]\n\t`door.ani`\n",
		"passiveobject/obj/door.ani":      "[FRAME MAX]\n\t1\n",
		// 装备链：#34 的附件边 + #36 的资源盘点都要在真实产物路径上有覆盖。
		"equipment/equipment.lst": "7\t`equ/w.equ`\n",
		"equipment/equ/w.equ":     "[icon]\n\t`Item/Icon/weapon.img`\n\t7\n[appendage]\n\t5\n",
		"appendage/appendage.lst": "5\t`appendage/x.apd`\n",
		"appendage/x.apd":         "[change attack info]\n\t1\n",
	} {
		if _, err := a.AddFileText(p, text, pvf.TypeScript); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "test.pvf")
	if err := a.SaveAs(src); err != nil {
		t.Fatal(err)
	}
	opened, err := pvf.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCore()
	if err := c.setArchive(opened); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.closeArchive)
	return c, NewExtractionService(c)
}

func TestExtractionServiceClassifiesAndCompletes(t *testing.T) {
	_, svc := extractionFixture(t)
	categories, err := svc.Categories()
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(categories) < 5 {
		t.Fatalf("内置边目录应声明多个类别, got %d", len(categories))
	}

	// 全类别：主集就是闭包，没有补入。
	all, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if all.ExportedCount != all.ClosureCount || all.ExternalCount != 0 {
		t.Errorf("全类别导出应等于闭包: 闭包=%d 落盘=%d 补入=%d",
			all.ClosureCount, all.ExportedCount, all.ExternalCount)
	}

	// 只勾"场景与副本"：主集只剩余种子，其余靠补入保持闭包。
	selected, err := svc.Prepare(ExtractionRequest{
		Seeds: []string{"map/forest/a.map"}, Categories: []string{"scene"},
	})
	if err != nil {
		t.Fatalf("Prepare(scene): %v", err)
	}
	if selected.MainCount != 1 {
		t.Errorf("只勾场景时主集应为 1（种子本身）, got %d", selected.MainCount)
	}
	if selected.ExternalCount != 2 || selected.ExportedCount != 3 {
		t.Errorf("应把 obj 与 ani 补入: 补入=%d 落盘=%d", selected.ExternalCount, selected.ExportedCount)
	}
	if selected.DanglingCount != 0 {
		t.Errorf("closed 模式不应有悬空引用, got %d", selected.DanglingCount)
	}

	dir := t.TempDir()
	result, err := svc.Execute(selected.ID, dir)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Files != 3 || result.AutoCompleted != 2 {
		t.Errorf("落盘结果 = %+v, want 3 文件 / 2 补入", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, extraction.ManifestName))
	if err != nil {
		t.Fatalf("读取 manifest: %v", err)
	}
	var manifest extraction.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest 解析失败: %v", err)
	}
	if manifest.Selection == nil || manifest.Selection.Mode != "closed" {
		t.Fatalf("manifest.selection 异常: %+v", manifest.Selection)
	}
	if len(manifest.Selection.AutoCompleted) != 2 {
		t.Errorf("补入清单应含 2 条, got %v", manifest.Selection.AutoCompleted)
	}
	for _, dest := range manifest.Selection.AutoCompleted {
		if !strings.HasPrefix(filepath.ToSlash(dest), extraction.ExternalDir+"/") {
			t.Errorf("补入条目未落在 %s/ 下: %s", extraction.ExternalDir, dest)
		}
		if _, err := os.Stat(filepath.Join(dir, extraction.DefaultSubdir, filepath.FromSlash(dest))); err != nil {
			t.Errorf("清单里的补入条目不在磁盘上: %s", dest)
		}
	}
}

func TestExtractionServiceStrictReportsDangling(t *testing.T) {
	_, svc := extractionFixture(t)
	strict, err := svc.Prepare(ExtractionRequest{
		Seeds: []string{"map/forest/a.map"}, Categories: []string{"scene"}, Strict: true,
	})
	if err != nil {
		t.Fatalf("Prepare(strict): %v", err)
	}
	if strict.ExternalCount != 0 {
		t.Errorf("strict 模式不得补入, got %d", strict.ExternalCount)
	}
	// 悬空只统计"导出集自己的出边指向集外"的那一条：a.map → door.obj。
	// door.obj → door.ani 不算，因为 door.obj 本身根本没导出——严格子集里
	// 不存在的东西不会产生悬空引用，这正是 strict 的语义。
	if strict.DanglingCount != 1 {
		t.Errorf("strict 悬空引用应为 1（a.map → door.obj）, got %d", strict.DanglingCount)
	}
	result, err := svc.Execute(strict.ID, t.TempDir())
	if err != nil {
		t.Fatalf("Execute(strict): %v", err)
	}
	if result.Files != 1 || result.Dangling != 1 {
		t.Errorf("strict 落盘 = %+v, want 1 文件 / 1 悬空", result)
	}
}

func TestExtractionServicePlanLifecycle(t *testing.T) {
	_, svc := extractionFixture(t)
	if _, err := svc.Execute("not-a-plan", t.TempDir()); !errors.Is(err, ErrExtractionStale) {
		t.Errorf("未知 planID 应报 ErrExtractionStale, got %v", err)
	}
	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := svc.Execute(preview.ID, ""); err == nil {
		t.Error("空目录必须被拒绝")
	}
	svc.Release(preview.ID)
	if _, err := svc.Execute(preview.ID, t.TempDir()); !errors.Is(err, ErrExtractionStale) {
		t.Errorf("释放后应报 ErrExtractionStale, got %v", err)
	}
	if _, err := svc.Prepare(ExtractionRequest{}); err == nil {
		t.Error("无种子必须被拒绝")
	}
}

// TestExtractionPlanExpiresAfterArchiveEdit proves a preview is bound to
// content rather than to a pointer: an in-place edit changes neither the
// archive object nor its generation, so only the bytes can reveal it.
func TestExtractionPlanExpiresAfterArchiveEdit(t *testing.T) {
	c, svc := extractionFixture(t)
	a := c.archive

	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if preview.ClosureCount != 3 {
		t.Fatalf("基线闭包应为 3, got %d", preview.ClosureCount)
	}

	// 改闭包成员的内容并落盘：变更日志与待写编辑都会被清空，只剩字节能说明它变了。
	ani, ok := a.Find("passiveobject/obj/door.ani")
	if !ok {
		t.Fatal("door.ani 不存在")
	}
	if err := a.SetText(ani, "[FRAME MAX]\n\t2\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a.ClearMutations()
	if len(a.PendingEdits()) != 0 || a.MutationCheckpoint() != 0 {
		t.Fatalf("保存后应无廉价信号可查: edits=%d journal=%d",
			len(a.PendingEdits()), a.MutationCheckpoint())
	}
	if _, err := svc.Execute(preview.ID, t.TempDir()); !errors.Is(err, ErrExtractionStale) {
		t.Errorf("内容已改并落盘，Execute 应报 ErrExtractionStale, got %v", err)
	}

	// 重新准备等于重新取证，绑定新内容后即可执行。
	again, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare(again): %v", err)
	}
	if _, err := svc.Execute(again.ID, t.TempDir()); err != nil {
		t.Fatalf("Execute(again): %v", err)
	}

	// 登记表是另一条通道：删掉 door.obj 的登记行既不换 generation 也不改闭包成员，
	// 但解析结果变了，旧预览必须作废，新预览不再穿过它。
	list, ok := a.Find("passiveobject/passiveobject.lst")
	if !ok {
		t.Fatal("passiveobject.lst 不存在")
	}
	if err := a.SetText(list, "99\t`obj/gone.obj`\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(again.ID, t.TempDir()); !errors.Is(err, ErrExtractionStale) {
		t.Errorf("登记表已改，Execute 应报 ErrExtractionStale, got %v", err)
	}
	third, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare(third): %v", err)
	}
	if third.ClosureCount != 1 {
		t.Errorf("取消登记后闭包应缩到 1（只剩 a.map）, got %d", third.ClosureCount)
	}
	if _, err := svc.Execute(third.ID, t.TempDir()); err != nil {
		t.Fatalf("Execute(third): %v", err)
	}
}

// TestExtractionScannerFollowsRuleReload locks in a delegation: the relation
// table extraction resolves with comes from the rule store, and the only way
// those rules change is by activating a new annotation engine. So the engine's
// identity belongs in the scanner cache key, or an override would keep
// extracting with the old relations until the archive itself changed.
func TestExtractionScannerFollowsRuleReload(t *testing.T) {
	c, svc := extractionFixture(t)
	if _, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	archive, generation, rules := svc.currentArchive()
	built, err := svc.scannerFor(archive, generation, rules)
	if err != nil {
		t.Fatalf("scannerFor: %v", err)
	}
	if built == nil {
		t.Fatal("Prepare 之后应有一个已预热的扫描器")
	}

	replacement, err := annotationrules.LoadDefault()
	if err != nil {
		t.Fatalf("加载规则失败: %v", err)
	}
	c.mu.Lock()
	c.annotationEngine = replacement
	c.mu.Unlock()

	if _, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}}); err != nil {
		t.Fatalf("Prepare(reload): %v", err)
	}
	archive2, generation2, rules2 := svc.currentArchive()
	rebuilt, err := svc.scannerFor(archive2, generation2, rules2)
	if err != nil {
		t.Fatalf("scannerFor(reload): %v", err)
	}
	if rebuilt == built {
		t.Error("规则实例已更换，扫描器却仍复用旧的 relation 表")
	}
}

// TestExtractionPreviewAnnouncesTruncation pins the UI's honesty contract for
// MaxEntries: a capped closure has the same shape as a complete one, so the
// preview must say "this is a sample" instead of letting the user approve a
// truncated set and discover the missing files after export.
func TestExtractionPreviewAnnouncesTruncation(t *testing.T) {
	_, svc := extractionFixture(t)
	full, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare(full): %v", err)
	}
	if full.ClosureCount < 3 {
		t.Fatalf("fixture 闭包应至少 3 条, got %d", full.ClosureCount)
	}
	for _, warning := range full.Warnings {
		if strings.Contains(warning, "上限") {
			t.Fatalf("未设上限却报了截断警告: %s", warning)
		}
	}

	capped, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}, MaxEntries: 2})
	if err != nil {
		t.Fatalf("Prepare(capped): %v", err)
	}
	if capped.ClosureCount > 2 {
		t.Fatalf("上限没起作用: %d", capped.ClosureCount)
	}
	found := ""
	for _, warning := range capped.Warnings {
		if strings.Contains(warning, "上限") {
			found = warning
			break
		}
	}
	if found == "" {
		t.Errorf("命中上限必须给出警告, got %v", capped.Warnings)
	}
	if !strings.Contains(found, "样本") {
		t.Errorf("警告要说明计数只是样本: %s", found)
	}
}

// TestExtractionServiceAcceptsIDSeeds pins E4's promise at the service boundary:
// the same entry must come out of the registry form as of the path form, and a
// number nobody claims has to surface as a warning rather than silently
// shrinking the seed set.
func TestExtractionServiceAcceptsIDSeeds(t *testing.T) {
	_, svc := extractionFixture(t)
	fromPath, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare(path): %v", err)
	}
	fromID, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map:1"}})
	if err != nil {
		t.Fatalf("Prepare(relation:id): %v", err)
	}
	if fromID.ClosureCount != fromPath.ClosureCount || fromID.ExportedCount != fromPath.ExportedCount {
		t.Errorf("登记表种子与路径种子应给出同一份闭包: id=%d/%d path=%d/%d",
			fromID.ClosureCount, fromID.ExportedCount, fromPath.ClosureCount, fromPath.ExportedCount)
	}
	if len(fromID.Warnings) != 0 {
		t.Errorf("可解析的种子不该有警告: %v", fromID.Warnings)
	}

	// 表里没有的 ID：必须说清楚是哪一个种子解析失败，而不是给出"闭包为空"的沉默结果。
	_, err = svc.Prepare(ExtractionRequest{Seeds: []string{"map:999999"}})
	if err == nil {
		t.Fatal("无法解析的种子不该换来一个空计划")
	}
	if !strings.Contains(err.Error(), "种子") {
		t.Errorf("错误要指向种子问题, got %v", err)
	}
}

// TestExtractionDialogRefusesStalePlan pins the guard that must run *before* any
// native dialog: a stale or unknown plan id has to fail immediately rather than
// asking the user for a destination that cannot be written to.
func TestExtractionDialogRefusesStalePlan(t *testing.T) {
	_, svc := extractionFixture(t)
	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"map/forest/a.map"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, id := range []string{"", "not-a-plan", preview.ID + "-x"} {
		if _, err := svc.ExecuteDialog(id); !errors.Is(err, ErrExtractionStale) {
			t.Errorf("ExecuteDialog(%q) 应拒绝过期计划, got %v", id, err)
		}
	}
	// 关掉预览后连正当 id 也不再可执行。
	svc.Release(preview.ID)
	if _, err := svc.ExecuteDialog(preview.ID); !errors.Is(err, ErrExtractionStale) {
		t.Errorf("已释放的计划应拒绝执行, got %v", err)
	}
}

// TestExtractionEquipmentClosureCarriesAppendageAndAssets pins the two halves the
// equipment presets depend on, in the product path rather than the CLI: the
// attachment must come along through the appendage registry, and the referenced
// client image must be reported in the preview (with an honest "no NPK index"
// warning) and written into the package as a cross-reference sheet.
func TestExtractionEquipmentClosureCarriesAppendageAndAssets(t *testing.T) {
	_, svc := extractionFixture(t)
	preview, err := svc.Prepare(ExtractionRequest{Seeds: []string{"equipment/equ/w.equ"}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if preview.ClosureCount != 2 {
		t.Errorf("装备闭包应含本体与附件, got %d", preview.ClosureCount)
	}
	if preview.AssetTotal != 1 {
		t.Errorf("预览要在选目录之前就说图片有几个, got %d", preview.AssetTotal)
	}
	if preview.AssetMatched != 0 {
		t.Errorf("夹具没有 NPK 目录，不该报命中, got %d", preview.AssetMatched)
	}
	mentioned := false
	for _, warning := range preview.Warnings {
		if strings.Contains(warning, "NPK") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Errorf("没有索引必须显式警告，不能静默: %v", preview.Warnings)
	}

	result, err := svc.Execute(preview.ID, t.TempDir())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.AssetTotal != 1 || result.AssetSheet != extraction.AssetFile {
		t.Errorf("产物要带上资源清单: %+v", result)
	}
}

// TestAssetContainerCopyRejectsEscapesAndCountsBytes pins the opt-in artwork
// copy: only containers the index actually names may land in the package, a
// path that tries to leave the NPK directory is an error rather than a silent
// skip, and the byte count is what the preview promised.
func TestAssetContainerCopyRejectsEscapesAndCountsBytes(t *testing.T) {
	npkDirectory, root := t.TempDir(), t.TempDir()
	payload := strings.Repeat("N", 4096)
	if err := os.WriteFile(filepath.Join(npkDirectory, "sprite_a.NPK"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(npkDirectory, "sprite_b.NPK"), []byte(payload[:100]), 0o644); err != nil {
		t.Fatal(err)
	}

	want := int64(len(payload) + 100)
	if got := projectAssetBytes(npkDirectory, []string{"sprite_a.NPK", "sprite_b.NPK"}); got != want {
		t.Errorf("预览体积应为 %d, got %d", want, got)
	}
	if got := projectAssetBytes("", []string{"sprite_a.NPK"}); got != 0 {
		t.Errorf("没有 NPK 目录时应报 0, got %d", got)
	}
	if got := projectAssetBytes(npkDirectory, []string{"gone.NPK"}); got != 0 {
		t.Errorf("读不到的容器不该计入, got %d", got)
	}

	copied, bytes, err := copyAssetContainers(npkDirectory, root, []string{"sprite_a.NPK", "sprite_b.NPK"})
	if err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	if copied != 2 || bytes != want {
		t.Errorf("应拷 2 个共 %d 字节, got %d/%d", want, copied, bytes)
	}
	data, err := os.ReadFile(filepath.Join(root, "npk", "sprite_a.NPK"))
	if err != nil || string(data) != payload {
		t.Errorf("容器内容不符: %v len=%d", err, len(data))
	}
	if _, err := os.ReadFile(filepath.Join(root, "npk", "sprite_b.NPK.part")); err == nil {
		t.Error("成功的拷贝不该留下 .part 临时文件")
	}

	if _, _, err := copyAssetContainers(npkDirectory, root, []string{"../../evil.NPK"}); err == nil {
		t.Error("越出 NPK 目录的容器名必须报错，不能静默跳过")
	}
	if _, _, err := copyAssetContainers(npkDirectory, root, []string{"gone.NPK"}); err == nil {
		t.Error("索引给出却读不到的容器必须报错：承诺了图片就要么给全要么失败")
	}
	if _, _, err := copyAssetContainers("", root, []string{"sprite_a.NPK"}); err != nil {
		t.Errorf("没有目录时不该报错，只是无可拷贝: %v", err)
	}
}

// TestExtractionPresetsApplyDefaultsButNeverNarrowClosure pins what a preset may
// and may not do: it supplies category defaults and echoes itself back, an
// unknown id is an error rather than a silent "no preset", an explicit category
// list wins over the preset, and the closure itself is the same size either way
// — narrowing the *walk* is not what a preset means.
func TestExtractionPresetsApplyDefaultsButNeverNarrowClosure(t *testing.T) {
	_, svc := extractionFixture(t)
	presets, err := svc.Presets()
	if err != nil {
		t.Fatalf("Presets: %v", err)
	}
	found := map[string]bool{}
	for _, preset := range presets {
		found[preset.ID] = true
	}
	for _, want := range []string{"dungeon", "equipment", "item", "giftbox", "town", "combat", "aic", "worldmap"} {
		if !found[want] {
			t.Errorf("服务侧预设列表缺少 %s: %v", want, found)
		}
	}

	seed := []string{"equipment/equ/w.equ"}
	byPreset, err := svc.Prepare(ExtractionRequest{Seeds: seed, Preset: "equipment"})
	if err != nil {
		t.Fatalf("Prepare(装备预设): %v", err)
	}
	if byPreset.Preset != "equipment" {
		t.Errorf("预览要回显用了哪个预设, got %q", byPreset.Preset)
	}
	plain, err := svc.Prepare(ExtractionRequest{Seeds: seed})
	if err != nil {
		t.Fatalf("Prepare(无预设): %v", err)
	}
	if byPreset.ClosureCount != plain.ClosureCount {
		t.Errorf("预设不得改变闭包大小：预设 %d vs 无预设 %d", byPreset.ClosureCount, plain.ClosureCount)
	}

	if _, err := svc.Prepare(ExtractionRequest{Seeds: seed, Preset: "no-such-preset"}); err == nil ||
		!strings.Contains(err.Error(), "未知的提取预设") {
		t.Errorf("未知预设要报错而不是当成没选, got %v", err)
	}

	explicit, err := svc.Prepare(ExtractionRequest{Seeds: seed, Preset: "equipment", Categories: []string{"item"}})
	if err != nil {
		t.Fatalf("Prepare(显式类别): %v", err)
	}
	// 闭包模式下取消勾选不减总量：少掉的那一项会以 _external/ 补回来。
	if explicit.MainCount+explicit.ExternalCount != plain.MainCount+plain.ExternalCount {
		t.Errorf("取消勾选不该减少落盘总量: %d+%d vs %d+%d",
			explicit.MainCount, explicit.ExternalCount, plain.MainCount, plain.ExternalCount)
	}
	if explicit.MainCount >= plain.MainCount {
		t.Errorf("只勾 item 时主集该比全类别小: %d vs %d", explicit.MainCount, plain.MainCount)
	}
}
