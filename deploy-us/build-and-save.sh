#!/usr/bin/env bash
# 【在 build 机上跑】——一台有 OpenIM + OpenIMChat 源码的机器（你的开发机即可）。
# 产出：openim-images-<tag>.tar.gz（预置镜像）+ 把 config 打包进 deploy-us/openim-config、chat-config。
# 之后把整个 deploy-us/ 目录（含 tar.gz）scp 到美服，生产机只需 docker load + up，不拉代码、不编译。
set -euo pipefail

IMAGE_TAG="${IMAGE_TAG:-us}"
# ★ 美服架构 = amd64。Dockerfile 的 builder 阶段用 --platform=$BUILDPLATFORM + Go 交叉编译：
#   重活（go mod download / mage build）在 build 机原生架构跑，arm64 mac 交叉 amd64 也快，
#   只有最终镜像的轻量 RUN（apk、go get gomake）走 QEMU。
PLATFORM="${PLATFORM:-linux/amd64}"
DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"     # deploy-us 目录（本脚本所在）
# 源码位置：deploy-us 就在 OpenIM 仓库内 → 默认取父目录；Chat 默认找兄弟目录（两种命名都试）
OPENIM_DIR="${OPENIM_DIR:-$(dirname "$DEPLOY_DIR")}"
if [ -z "${CHAT_DIR:-}" ]; then
  for d in OpenIM_Chat OpenIMChat; do
    if [ -d "$(dirname "$OPENIM_DIR")/$d" ]; then CHAT_DIR="$(dirname "$OPENIM_DIR")/$d"; break; fi
  done
fi
CHAT_DIR="${CHAT_DIR:?未找到 OpenIM_Chat/OpenIMChat 兄弟目录，请 export CHAT_DIR=<Chat 源码路径>}"
OUT="$DEPLOY_DIR/openim-images-${IMAGE_TAG}.tar.gz"

green() { printf '\033[32m%s\033[0m\n' "$*" 2>/dev/null || echo "$*"; }

# 1) buildx 构建 amd64 镜像（--load 落到本地 docker；单平台可 --load）
green "== buildx build openim-server:$IMAGE_TAG ($PLATFORM) =="
docker buildx build --platform "$PLATFORM" -t "openim-server:$IMAGE_TAG" --load "$OPENIM_DIR"
green "== buildx build openim-chat:$IMAGE_TAG ($PLATFORM) =="
docker buildx build --platform "$PLATFORM" -t "openim-chat:$IMAGE_TAG" --load "$CHAT_DIR"

# 2) save 成 tar.gz（只打包自己 build 的两个；basics 由生产机 pull 公共镜像）
green "== docker save → $OUT =="
docker save "openim-server:$IMAGE_TAG" "openim-chat:$IMAGE_TAG" | gzip > "$OUT"
green "  产出: $(ls -lh "$OUT" | awk '{print $5}')"

# 3) 打包 config 到部署包（原始默认 config，生产机再跑 patch-config.sh 改密码/地址）
green "== 复制 config 到部署包 =="
rm -rf "$DEPLOY_DIR/openim-config" "$DEPLOY_DIR/chat-config"
cp -R "$OPENIM_DIR/config" "$DEPLOY_DIR/openim-config"
cp -R "$CHAT_DIR/config"   "$DEPLOY_DIR/chat-config"
echo "  openim-config/ chat-config/ 已就绪"

green "完成。把整个 deploy-us/（含 $(basename "$OUT")）scp 到美服，例如："
echo "  rsync -avz '$DEPLOY_DIR/' <美服>:/dl/deploy-us/"
echo "  然后在美服：cd /dl/deploy-us && bash patch-config.sh && bash deploy.sh（.env 已预填密码/secret，零手工输入）"
