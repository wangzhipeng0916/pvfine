package services

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	modpkg "pvfine/internal/mod"
	"pvfine/internal/pvf"
)

// closureExportFixture is a chain the table-based dependency pass cannot see: an
// .obj whose [use animation] names an .ani. Nothing here is a text table or a
// list row, so whatever completes the package can only have come from the
// reference graph. `broken.obj` references a file the archive does not hold,
// which no completion can invent — it has to be reported instead.
func closureExportFixture(t *testing.T) (*core, *ExportService) {
	t.Helper()
	a := pvf.New()
	for p, text := range map[string]string{
		"passiveobject/passiveobject.lst": "10\t`obj/door.obj`\n11\t`obj/broken.obj`\n",
		"passiveobject/obj/door.obj":      "[use animation]\n\t`door.ani`\n",
		"passiveobject/obj/door.ani":      "[FRAME MAX]\n\t1\n",
		"passiveobject/obj/unused.ani":    "[FRAME MAX]\n\t2\n",
		"passiveobject/obj/broken.obj":    "[use animation]\n\t`gone.ani`\n",
	} {
		if _, err := a.AddFileText(p, text, pvf.TypeScript); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "closure.pvf")
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
	return c, NewExportService(c)
}

// exportedSet lists what the preview says will land in the package. The writer
// prefixes whole files with pvf/ and table deltas with merge/, so the check keys
// on the archive path suffix rather than the destination layout.
func exportedSet(preview *ExportPreview) map[string]bool {
	out := map[string]bool{}
	for _, file := range preview.Files {
		normalized := strings.ToLower(filepath.ToSlash(file.Path))
		out[strings.TrimPrefix(normalized, "pvf/")] = true
		out[strings.TrimPrefix(normalized, "merge/")] = true
		out[normalized] = true
	}
	return out
}

func warningsMention(preview *ExportPreview, needle string) bool {
	for _, warning := range preview.Warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}

// exportedAnis lists the animation entries in the package, ignoring pack.json and
// the destination prefixes: "did the closure carry the referenced .ani" is the
// whole question this file asks, and the answer must not depend on layout.
func exportedAnis(preview *ExportPreview) []string {
	out := []string{}
	for _, file := range preview.Files {
		if normalized := strings.ToLower(filepath.ToSlash(file.Path)); strings.HasSuffix(normalized, ".ani") {
			out = append(out, normalized)
		}
	}
	return out
}

// TestExportModCompletesReferenceClosure is the E13 layer wired into MOD export:
// selecting the .obj must carry the .ani it references, must not carry an
// unreferenced neighbour, and the preview must say how many entries came from
// the closure rather than from the selection.
func TestExportModCompletesReferenceClosure(t *testing.T) {
	_, exports := closureExportFixture(t)
	const objPath = "passiveobject/obj/door.obj"

	plain := modRequest("selection")
	plain.Scopes = []string{objPath}
	bare, err := exports.Prepare(plain)
	if err != nil {
		t.Fatalf("Prepare(without deps): %v", err)
	}
	if len(exportedAnis(bare)) != 0 {
		t.Fatalf("未开依赖补齐时不该带任何 .ani: %v", exportedAnis(bare))
	}
	if !exportedSet(bare)["passiveobject/obj/door.obj"] {
		t.Fatalf("选中的 .obj 必须在包里: %v", exportedSet(bare))
	}
	if bare.ReferenceDependencies != 0 {
		t.Errorf("未开开关不该有闭包补齐: %d", bare.ReferenceDependencies)
	}

	withDeps := modRequest("selection")
	withDeps.Scopes = []string{objPath}
	withDeps.IncludeDependencies = true
	preview, err := exports.Prepare(withDeps)
	if err != nil {
		t.Fatalf("Prepare(deps): %v", err)
	}
	paths := exportedSet(preview)
	if !paths[objPath] {
		t.Fatalf("选中的 .obj 必须在包里: %v", paths)
	}
	// 这一条就是本层存在的理由：文字表补齐看不见 [use animation]。
	if !paths["passiveobject/obj/door.ani"] {
		t.Errorf("引用的 .ani 应由引用闭包补齐: %v", paths)
	}
	if paths["passiveobject/obj/unused.ani"] {
		t.Errorf("没被引用的同目录文件不该进包: %v", paths)
	}
	if preview.ReferenceDependencies < 1 {
		t.Errorf("预览要说明补了几个引用依赖, got %d", preview.ReferenceDependencies)
	}
	if got := exportedAnis(preview); len(got) != 1 || !strings.HasSuffix(got[0], "door.ani") {
		t.Errorf("包里应正好补进 door.ani 一个动画: %v", got)
	}
	if warningsMention(preview, "不存在") {
		t.Errorf("这条链上没有缺失目标，不该报: %v", preview.Warnings)
	}
}

// TestExportModClosureReportsDanglingTargets pins the honesty half: when the
// package's own file references something the archive does not contain, the
// preview must name it, because that is the one failure completion cannot fix.
func TestExportModClosureReportsDanglingTargets(t *testing.T) {
	_, exports := closureExportFixture(t)
	request := modRequest("selection")
	request.Scopes = []string{"passiveobject/obj/broken.obj"}
	request.IncludeDependencies = true
	preview, err := exports.Prepare(request)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !warningsMention(preview, "gone.ani") {
		t.Errorf("缺失目标要指名, got %v", preview.Warnings)
	}
	if !warningsMention(preview, "不存在") {
		t.Errorf("要用「不存在」说清性质, got %v", preview.Warnings)
	}
}

// TestExportModClosureFollowsAnEditedReference pins freshness. The scanner is
// cached across previews, so the question this layer has to answer is whether a
// second Prepare sees a reference the user just added — an answered-from-cache
// "no" would ship a MOD whose script points at a file the package does not hold.
// The edit goes through EditorService because that is the path that moves the
// signals an export preview is validated against.
func TestExportModClosureFollowsAnEditedReference(t *testing.T) {
	c, exports := closureExportFixture(t)
	request := modRequest("selection")
	request.Scopes, request.IncludeDependencies = []string{"passiveobject/obj/door.obj"}, true

	first, err := exports.Prepare(request)
	if err != nil {
		t.Fatalf("Prepare(first): %v", err)
	}
	if got := exportedAnis(first); len(got) != 1 {
		t.Fatalf("基线应只补进 door.ani: %v", got)
	}

	i, ok := c.archive.Find("passiveobject/obj/door.obj")
	if !ok {
		t.Fatal("fixture 缺少 door.obj")
	}
	if err := NewEditorService(c).SetText(i,
		"[use animation]\n\t`door.ani`\n\t`unused.ani`\n"); err != nil {
		t.Fatal(err)
	}

	second, err := exports.Prepare(request)
	if err != nil {
		t.Fatalf("Prepare(second): %v", err)
	}
	paths := exportedSet(second)
	if !paths["passiveobject/obj/unused.ani"] {
		t.Errorf("改后新增的引用应由下一次预览补上: %v", paths)
	}
	if second.ReferenceDependencies < 2 {
		t.Errorf("补入计数要跟着变, got %d", second.ReferenceDependencies)
	}
}

// TestExportReferenceClosureRunsUnderHeldCoreReadLock pins the lock discipline
// that a full-suite run once turned into a hang: Prepare holds the core read lock
// for its whole body, so anything it calls must not take that lock again. A
// recursive RLock is harmless until a writer queues up — and in the app writers
// do queue (autosave, index rebuilds), which is how the first version of this pass
// deadlocked a test run rather than failing it.
func TestExportReferenceClosureRunsUnderHeldCoreReadLock(t *testing.T) {
	c, exports := closureExportFixture(t)
	index, ok := c.archive.Find("passiveobject/obj/door.obj")
	if !ok {
		t.Fatal("fixture 缺少 door.obj")
	}
	data, err := c.archive.Text(index)
	if err != nil {
		t.Fatal(err)
	}

	// A queued writer is what makes a recursive RLock block, so make one wait
	// before taking the read lock ourselves.
	writerDone := make(chan struct{})
	go func() {
		c.mu.Lock()
		c.mu.Unlock()
		close(writerDone)
	}()
	time.Sleep(50 * time.Millisecond)

	c.mu.RLock()
	// 补齐会用 writer.Validate 逐条检查，而它先查包名；Prepare 的包一定带名字，
	// 所以这里也要有，否则校验失败会被当成「闭包没补到东西」。
	pkg := &modpkg.Package{Metadata: modpkg.Metadata{Name: "测试MOD"}, Entries: []modpkg.Entry{{
		Path: "passiveobject/obj/door.obj", Operation: modpkg.ReplaceFile,
		DataType: pvf.TypeScript, Encoding: modpkg.Text, Data: []byte(data),
	}}}
	c.mu.RUnlock()

	writable := ""
	for _, descriptor := range exports.Formats() {
		if descriptor.CanWrite {
			writable = descriptor.ID
			break
		}
	}
	if writable == "" {
		t.Skip("没有可写的 mod 格式")
	}
	format, err := modpkg.DefaultRegistry().Get(writable)
	if err != nil || format.Writer == nil {
		t.Fatalf("取格式 %s 失败: %v", writable, err)
	}
	writer := format.Writer

	// 同一条goroutine 上复现 Prepare 的持锁姿势；卡住就是回归。
	source := &exportSource{archive: c.archive}
	result := make(chan int, 1)
	go func() {
		c.mu.RLock()
		defer c.mu.RUnlock()
		added, _, err := exports.addExportReferenceClosureLocked(source, pkg, writer)
		if err != nil {
			t.Errorf("补齐失败: %v", err)
		}
		result <- added
	}()
	select {
	case added := <-result:
		if added < 1 {
			t.Errorf("持读锁时补齐应仍能工作, got %d", added)
		}
		// 跳过会写日志并计入诊断，不体现在返回值上，所以单独断言：静默跳过
		// 会让这个测试以为「闭包正常」而实际什么都没补。
		if source.diagnostics.skipped > 0 {
			t.Errorf("补齐被静默跳过 %d 处", source.diagnostics.skipped)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("引用闭包补齐在调用方已持 core 读锁时卡死：它又去取了一次读锁")
	}
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("等待写锁的 goroutine 没拿到锁，测试自身没有还原现场")
	}
}
