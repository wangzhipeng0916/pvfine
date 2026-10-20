import { describe, expect, it } from "vitest";
import { isLocalBuildVersion, normalizeBuildVersion } from "../src/updateChannel";

describe("更新频道门禁", () => {
  it("统一版本号形态", () => {
    expect(normalizeBuildVersion("  v0.16.0 ")).toBe("0.16.0");
    expect(normalizeBuildVersion("V0.16.0")).toBe("0.16.0");
    expect(normalizeBuildVersion("dev")).toBe("dev");
  });

  it("本地构建的占位版本禁用更新频道", () => {
    for (const raw of ["0.0.0", "v0.0.0", "dev", "", "   "]) {
      expect(isLocalBuildVersion(raw)).toBe(true);
    }
  });

  it("真实发布版本仍接入更新频道", () => {
    for (const raw of ["0.16.0", "v0.16.0", "1.0.0-rc.1"]) {
      expect(isLocalBuildVersion(raw)).toBe(false);
    }
  });
});
