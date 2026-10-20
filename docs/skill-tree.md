# 技能树只读工具

入口：打开 PVF 后，在“工具”菜单末尾选择“技能树”。选择职业、转职及 SP / TP；顶部显示该职业的通用技能，点击技能可查看其 `[explain]` 描述（解析字符串引用并保留换行），或打开技能文件。同一坐标的技能合为一个图标，角标标出数量，点击后通过右侧列表切换，保留原位置与文件顺序。支持画布缩放，图标复用现有 NPK 图片索引。未配置客户端资源时显示占位图标。

## 数据来源

- `clientonly/skillshoptreespindex.co`、`clientonly/skillshoptreetpindex.co` 的 `[skill tree]` 按职业 token / 相对路径读取，路径相对于 `clientonly/`。
- 树文件中的每个 `[character job]` 独立保留，第二个值 `none` 表示未转职；不将未转职树合并进已转职树。
- 技能定义从 `skill/skilllist.lst` 注册的职业列表查找。ID 属于职业上下文，通用技能也必须通过当前职业的列表解析，不能共用全局 ID 索引。
- `clientonly/commonskilllist.co` 的 `[common skill]` 保留顺序；合并所有匹配职业的 `[except skill info]/[info]` 排除项。
- `[next skill]` 允许重复出现，每个值依源顺序保留。连线只连接同一分支存在的目标；缺失目标显示数据提示，不推断服务器学习条件。

## 坐标

使用 `testdata/110US.pvf` 检查了全部 17 个职业的 34 个 SP / TP 注册及所有转职分支。样例中的 `[cell pos]` 是列、行，换算为 `x = 列 × 47`、`y = 行 × 67`；`[icon pos]` 是直接像素坐标。两种写法可在同一分支混用，缔造者还使用 23、117 等半格横向偏移，不能先取整成网格坐标。

这些间距来自真实数据的规律，尚未通过客户端代码验证；未来遇到不同版本的布局参数时应独立扩展换算策略。SP 画布左侧显示该行最左侧技能的 `[required level]`，同位置按文件顺序取第一个；缺失等级或空行不显示数值。TP 不显示等级标注。技能卡片下方显示技能名，不模拟玩家学习等级。

关闭弹窗后，在本次应用会话中保留职业、转职及 SP / TP 选择；重开时重新读取所选技能树，若归档不再包含该选项则回退到首个可用选项。Cmd / Ctrl + 单击左下角路径可在编辑器打开技能树源文件。

读取使用当前归档覆盖层，不修改归档，也不自动提交编辑器尚未应用的草稿；“重新读取”更新已应用的数据。未知转职 token 原样显示。后续可在保留原始坐标种类与后继顺序的基础上扩展编辑能力。

## 验证

```sh
go test ./...
PVF_TESTFILE="$PWD/testdata/110US.pvf" go test ./services -run 'TestSkillTree|TestCommonSkillsJobExclusions' -v
cd frontend && npm test && npm run build
```
