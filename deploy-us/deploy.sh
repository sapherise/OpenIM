#!/usr/bin/env bash
# 【在美服生产机上跑】——load 预置镜像 + 起服务 + 健康检查。首次部署和日常更新都用它。
# 前提：build 机已跑 build-and-save.sh 产出 openim-images-<tag>.tar.gz 并 scp 到本目录；且已跑过 patch-config.sh。
# 生产机全程不拉代码、不装 Go、不编译。
set -euo pipefail

IMAGE_TAG="${IMAGE_TAG:-us}"
DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DEPLOY_DIR"
TAR="openim-images-${IMAGE_TAG}.tar.gz"

green() { printf '\033[32m%s\033[0m\n' "$*" 2>/dev/null || echo "$*"; }

# 0) 前置校验 —— 堵住"半初始化"坑：mongo 只在【首次、空数据目录】时按 .env 初始化用户，
#    用占位密码起过一次后再改 .env 不会更新已建用户，只能 mongosh 手改或清数据重来。
if grep -q "CHANGE_ME" .env 2>/dev/null; then
  echo "❌ .env 里还有 CHANGE_ME 占位密码 —— 先改成美服专属强密码，再 export 同值跑 patch-config.sh，最后回来跑本脚本" >&2
  exit 1
fi
if [ ! -d openim-config ] || [ ! -d chat-config ]; then
  echo "❌ 缺 openim-config/ 或 chat-config/ —— 这是 build-and-save.sh 的产物，应随部署包一起 rsync 过来" >&2
  exit 1
fi
if grep -rq "CHANGE_ME" openim-config chat-config 2>/dev/null; then
  echo "❌ config 里有 CHANGE_ME —— patch-config.sh 跑的时候没 export 真实密码，重跑一遍" >&2
  exit 1
fi
if ! grep -q "mongo:27017" openim-config/mongodb.yml 2>/dev/null; then
  echo "❌ openim-config/mongodb.yml 地址还不是容器服务名 —— 还没跑 patch-config.sh，先跑它" >&2
  exit 1
fi

# 1) load 预置镜像（更新时把新 tar scp 过来再跑本脚本即可）
if [ -f "$TAR" ]; then
  green "== docker load < $TAR =="
  gunzip -c "$TAR" | docker load
else
  echo "⚠️ 未找到 $TAR —— 若镜像已在本机（openim-server:$IMAGE_TAG）则继续，否则先从 build 机拷过来"
fi

# 2) 先起 basics（让 mongo 初始化 openIM 用户），再起 openim —— 避免 server 在 basics 没 ready 时启动
green "== docker compose up -d basics =="
docker compose --env-file .env up -d mongodb redis kafka etcd
sleep 15
green "== docker compose up -d（全部，server/chat 用刚 load 的镜像）=="
docker compose --env-file .env up -d

# 3) 等 openim-server healthy（mage start 拉起 12 进程约 1~2min）
green "== 等 openim-server healthy（最多 3min）=="
ok=""
for _ in $(seq 1 18); do
  status=$(docker inspect -f '{{.State.Health.Status}}' openim-server 2>/dev/null || echo unknown)
  [ "$status" = "healthy" ] && { ok=1; green "openim-server healthy"; break; }
  sleep 10
done
[ -z "$ok" ] && echo "⚠️ openim-server 未在 3min 内 healthy，查 docker logs openim-server"

# 4) 健康检查
green "== docker compose ps =="
docker compose --env-file .env ps
for p in 10002 10001 10008; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$p/" 2>/dev/null || echo 000)
  [ "$code" != "000" ] && green "  port $p 有响应(HTTP $code)" || echo "  ⚠️ port $p 无响应（还在启动？看 docker logs）"
done
green "完成。外部验证：curl https://im-us.lumiscape.xxx/api/"
