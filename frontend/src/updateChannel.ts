/**
 * Go 侧构建时注入的版本号统一形态：去掉首尾空白与可选的 v 前缀。
 * 版本标签展示与更新频道门禁共用这一步，避免两处解析漂移。
 */
export function normalizeBuildVersion(raw: string): string {
  return raw.trim().replace(/^v/i, "");
}

/**
 * 本地构建（占位版本 0.0.0 / dev / 空）不接入 Release 更新频道：这类构建一旦触发
 * 更新，官方安装包会直接覆盖开发用的 exe。判据与 main.go 的 isReleaseBuild 保持一致。
 */
export function isLocalBuildVersion(raw: string): boolean {
  const version = normalizeBuildVersion(raw);
  return version === "" || version === "dev" || version === "0.0.0";
}
