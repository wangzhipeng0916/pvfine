/**
 * 设置页内置文档的数据层。markdown 在构建时以 ?raw 打进包，运行时不联网、
 * 也不引入渲染器：这里只做「取章节」和「轻度清洗」两件事，正文最终用等宽
 * 预格式展示，所以按行扫描足够，不需要解析整棵树。
 */

/** 只裁掉首尾空行：正文缩进要原样留着，等宽视图靠它表达层级。 */
function trimBlankLines(lines: string[]): string {
  const out = [...lines];
  while (out.length > 0 && out[0].trim() === "") out.shift();
  while (out.length > 0 && out[out.length - 1].trim() === "") out.pop();
  return out.join("\n");
}

/** 取某个标题下的正文，遇到同级或更高级标题、或裸分隔线时结束。 */
export function extractMarkdownSection(markdown: string, title: string): string {
  const lines = markdown.split(/\r?\n/);
  let level = 0;
  const collected: string[] = [];
  for (const line of lines) {
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const text = heading[2].trim();
      if (level === 0) {
        if (text === title || text.includes(title)) {
          level = heading[1].length;
        }
        continue;
      }
      if (heading[1].length <= level) break;
    }
    if (level > 0 && /^-{3,}\s*$/.test(line)) break;
    if (level > 0) collected.push(line);
  }
  return trimBlankLines(collected);
}

/**
 * 轻度清洗：去掉标题井号与列表符号（保留原缩进）、把 `**粗体**` 与 `[文字](链接)`
 * 还原成裸文本、丢弃分隔线。反引号与 `1.` 序号保留，前者在等宽视图里正好标出
 * 代码与文件名，后者去掉就丢了顺序。
 */
export function plainMarkdown(markdown: string): string {
  return trimBlankLines(
    markdown
      .split(/\r?\n/)
      .filter((line) => !/^-{3,}\s*$/.test(line))
      .map((line) =>
        line
          .replace(/^(\s*)#{1,6}\s+/, "$1")
          .replace(/^(\s*)[-*]\s+/, "$1")
          .replace(/\*\*([^*]+)\*\*/g, "$1")
          .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      )
  );
}
