import { describe, expect, it } from "vitest";
import { extractMarkdownSection, plainMarkdown } from "../src/docs";
import changelogLocal from "../../CHANGELOG.fork.md?raw";
import changelogUpstream from "../../CHANGELOG.md?raw";
import readme from "../../README.md?raw";

describe("内置文档数据层", () => {
  const sample = [
    "# 标题",
    "",
    "## ✨ 核心特性",
    "",
    "- 🚀 **百万索引秒开**：流式按需解密。",
    "- 📝 **编辑**：支持 `Vim` 模式。",
    "",
    "---",
    "",
    "## ⌨️ 快捷键",
    "",
    "- Ctrl+S 保存",
  ].join("\n");

  it("只取到目标章节，遇分隔线或下一级标题即止", () => {
    const section = extractMarkdownSection(sample, "核心特性");
    expect(section).toContain("百万索引秒开");
    expect(section).not.toContain("快捷键");
    expect(extractMarkdownSection(sample, "不存在的章节")).toBe("");
  });

  it("清洗掉井号、列表符号、粗体与链接，保留反引号与缩进", () => {
    const clean = plainMarkdown(sample);
    expect(clean).not.toContain("## ");
    expect(clean).not.toContain("- 🚀");
    expect(clean).toContain("🚀 百万索引秒开");
    expect(clean).toContain("`Vim`");
    expect(clean).not.toContain("**");
    expect(clean).not.toContain("---");
    expect(plainMarkdown("- [CHANGELOG.md](CHANGELOG.md) 说明")).toBe("CHANGELOG.md 说明");
    expect(plainMarkdown("  - 缩进项")).toBe("  缩进项");
  });

  it("仓库里的三份文档都能被正确取出", () => {
    const features = plainMarkdown(extractMarkdownSection(readme, "核心特性"));
    expect(features).toContain("百万索引秒开");
    expect(features).toContain("MCP 服务");

    const disclaimer = extractMarkdownSection(readme, "免责声明");
    expect(plainMarkdown(disclaimer)).toContain("法律风险");
    expect(disclaimer).not.toContain("开源协议");

    expect(changelogUpstream).toContain("## [v0.16.0]");
    expect(plainMarkdown(changelogUpstream)).toContain("[v0.16.0]");

    expect(changelogLocal).toContain("提取中心");
    expect(changelogLocal).toContain("清单所属表");
  });
});
