#!/usr/bin/env bash
# 本地构建：产出 bin/<APP_NAME> 开发版（占位版本 0.0.0，更新频道随之关闭）。
# 只做三件事：摆版本机的环境变量、跑 wails3、清掉 wails3 顺带产生的 git 噪音。
# 发版请用 .agents/skills/release，不要用它代替。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP_NAME="${APP_NAME:-pvfine}"
BUILD_TMP="${LOCAL_BUILD_TMP:-$(dirname "$ROOT")/.${APP_NAME}-build-tmp}"

# TEMP 默认落在内存盘上，Go 编译与归档测试会撞 ENOSPC，改用与仓库同盘的目录。
mkdir -p "$BUILD_TMP"
export TMPDIR="$BUILD_TMP" TEMP="$BUILD_TMP" TMP="$BUILD_TMP"

# 本机不通 proxy.golang.org，调用方没指定时补国内镜像。
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

BIN="bin/${APP_NAME}"
if [ "$(go env GOOS)" = "windows" ]; then
  BIN="${BIN}.exe"
fi

BUILD_LOG="${BUILD_TMP}/build.log"
echo "==> 本地构建 ${BIN}"
echo "    TMPDIR=${BUILD_TMP}"
echo "    GOPROXY=${GOPROXY}"

wails3 task build 2>&1 | tee "${BUILD_LOG}"

if [ ! -f "${BIN}" ]; then
  echo "构建失败：未生成 ${BIN}" >&2
  exit 1
fi

# wails3 的 build:frontend 会重新生成绑定并写成 LF，而仓库 checkout 是 CRLF；
# npm install 还会重写锁文件的 peer 标记。这些差异与 HEAD 等价，留着会把真实
# 改动埋进 git status（行尾噪音的特征正是 status 报 M 而 git diff 为空），
# 所以按“内容确认等价”才还原，不看文件名。
echo "==> 清理构建噪音"
for path in frontend/bindings go.mod go.sum; do
  [ -e "${path}" ] || continue
  tracked="$(git status --porcelain -- "${path}" | grep -v '^??' || true)"
  [ -n "${tracked}" ] || continue
  if [ -n "$(git diff --ignore-cr-at-eol --name-only -- "${path}")" ]; then
    echo "    跳过 ${path}：有实质改动，请自行确认"
    continue
  fi
  git checkout -- "${path}"
  echo "    还原 ${path}（仅行尾差异）"
done

# 锁文件的重写不是行尾差异，只有在 package.json 没动过时才敢还原。
if git diff --name-only -- frontend/package.json | grep -q .; then
  echo "    跳过 frontend/package-lock.json：package.json 有改动，请一并提交依赖"
elif [ -n "$(git diff --name-only -- frontend/package-lock.json)" ]; then
  git checkout -- frontend/package-lock.json
  echo "    还原 frontend/package-lock.json（npm install 重写）"
fi

VERSION_FLAG="$(grep -o -- '-X main.appVersion=[^ "]*' "${BUILD_LOG}" | tail -1)"
if [ -z "${VERSION_FLAG}" ]; then
  VERSION_FLAG="main.appVersion=<未注入，取源码默认 0.0.0>"
fi

echo "==> 完成：${BIN}（$(wc -c < "${BIN}") 字节）"
echo "    ${VERSION_FLAG}"
echo "    占位版本不会初始化更新器；菜单「检查更新」与设置页入口均不可用"
