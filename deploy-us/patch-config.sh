#!/usr/bin/env bash
# 【在美服生产机上跑】——改部署包内 config（openim-config/ chat-config/，由 build-and-save.sh 从源码打包而来）：
#   ① 密码/secret  ② 基础件地址改成容器网络服务名（localhost:映射端口 → 服务名:内部端口）。
# config 经 compose 的 volume 挂进容器；改完 docker compose restart 即生效、无需动镜像。幂等。
set -euo pipefail

# ── 密码/secret：和 deploy-us/.env、DLServer application-us.yml 对齐 ──
MONGO_OPENIM_PASSWORD="${MONGO_OPENIM_PASSWORD:-CHANGE_ME_mongo_openim}"
REDIS_PASSWORD="${REDIS_PASSWORD:-CHANGE_ME_redis}"
# OpenIM secret：必须同时 == DLServer imserver.secret == Chat share.yml 的 openIM.secret
OPENIM_SECRET="${OPENIM_SECRET:-CHANGE_ME_openim_secret}"

# ── 容器网络服务名（= compose service 名 : 容器内部端口）。一般不用改 ──
MONGO_ADDR="${MONGO_ADDR:-mongo:27017}"
REDIS_ADDR="${REDIS_ADDR:-redis:6379}"
KAFKA_ADDR="${KAFKA_ADDR:-kafka:9092}"     # kafka INTERNAL listener（非宿主映射的 19094）
ETCD_ADDR="${ETCD_ADDR:-etcd:2379}"
OPENIM_API_URL="${OPENIM_API_URL:-http://openim-server:10002}"   # Chat 调 OpenIM api 的地址

# ── config 目录（部署包内，build-and-save.sh 打包而来）──
DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
OPENIM_CONFIG="${OPENIM_CONFIG_DIR:-$DEPLOY_DIR/openim-config}"
CHAT_CONFIG="${CHAT_CONFIG_DIR:-$DEPLOY_DIR/chat-config}"

# ── 前置校验：占位值直接拒绝，避免把 CHANGE_ME 写进 config ──
for v in MONGO_OPENIM_PASSWORD REDIS_PASSWORD OPENIM_SECRET; do
  case "${!v}" in *CHANGE_ME*)
    echo "❌ $v 还是占位值 —— export $v='<真实值>'（与 deploy-us/.env 对齐）后再跑" >&2
    exit 1;;
  esac
done
# secret 沿用国服值检测：打包 config 里的原 secret 就是国服的，美服必须换新值，
# 否则同一 secret 签出的 token 在两套环境互通，隔离失效
old_secret=$(grep -m1 -E '^[[:space:]]*secret:' "$OPENIM_CONFIG/share.yml" 2>/dev/null | sed 's/^[[:space:]]*secret:[[:space:]]*//')
if [ -n "$old_secret" ] && [ "$OPENIM_SECRET" = "$old_secret" ]; then
  echo "⚠️⚠️ OPENIM_SECRET 与打包 config 的原值（国服 secret）相同 —— 美服务必换新 secret，否则两环境 token 互通！"
fi

set_yaml() {  # file  key  value —— 替换文件中【第一处】`key: value` 行，保留缩进，值按字面写入
  local file="$1" key="$2" val="$3"
  [ -f "$file" ] || { echo "跳过（不存在）：$file"; return; }
  local n; n=$(grep -cE "^[[:space:]]*${key}:[[:space:]]*" "$file" || true)
  if [ "${n:-0}" -eq 0 ]; then echo "⚠️ 未找到 [$key]，跳过：$file"; return; fi
  [ "$n" -gt 1 ] && echo "⚠️ [$key] 在 $file 出现 ${n} 次，仅替换第一处，请人工复核其余"
  # 用 awk 而非 sed：跨 BSD/GNU/mawk 一致；值按字面打印，不受 sed 的 & \ / 等元字符干扰
  # 值经 ENVIRON 传入（不能用 -v val=，-v 会做转义序列解释、吃掉密码里的反斜杠）
  SET_YAML_VAL="$val" awk -v key="$key" '
    !done && $0 ~ ("^[ \t]*" key ":") {
      match($0, "^[ \t]*" key ":")
      print substr($0, 1, RLENGTH) " " ENVIRON["SET_YAML_VAL"]
      done = 1
      next
    }
    { print }
  ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
  echo "patched: $file  [$key]"
}

echo "== OpenIM 密码/secret =="
set_yaml "$OPENIM_CONFIG/mongodb.yml" "password" "$MONGO_OPENIM_PASSWORD"
set_yaml "$OPENIM_CONFIG/redis.yml"   "password" "$REDIS_PASSWORD"
set_yaml "$OPENIM_CONFIG/share.yml"   "secret"   "$OPENIM_SECRET"

echo "== OpenIM 基础件地址（容器网络服务名）=="
set_yaml "$OPENIM_CONFIG/mongodb.yml"   "address" "[ $MONGO_ADDR ]"
set_yaml "$OPENIM_CONFIG/redis.yml"     "address" "[ $REDIS_ADDR ]"
set_yaml "$OPENIM_CONFIG/kafka.yml"     "address" "[ $KAFKA_ADDR ]"
set_yaml "$OPENIM_CONFIG/discovery.yml" "address" "[ $ETCD_ADDR ]"

echo "== OpenIM_Chat 密码/secret =="
set_yaml "$CHAT_CONFIG/mongodb.yml" "password" "$MONGO_OPENIM_PASSWORD"
set_yaml "$CHAT_CONFIG/redis.yml"   "password" "$REDIS_PASSWORD"
set_yaml "$CHAT_CONFIG/share.yml"   "secret"   "$OPENIM_SECRET"

echo "== OpenIM_Chat 地址（容器网络服务名 + OpenIM api）=="
set_yaml "$CHAT_CONFIG/mongodb.yml"   "address" "[ $MONGO_ADDR ]"
set_yaml "$CHAT_CONFIG/redis.yml"     "address" "[ $REDIS_ADDR ]"
set_yaml "$CHAT_CONFIG/discovery.yml" "address" "[ $ETCD_ADDR ]"
set_yaml "$CHAT_CONFIG/share.yml"     "apiURL"  "$OPENIM_API_URL"

echo ""
echo "完成。请人工复核："
echo "  1) $OPENIM_CONFIG/share.yml   secret        == DLServer application-us.yml imserver.secret"
echo "  2) $CHAT_CONFIG/share.yml     openIM.secret == 上面同一个值"
echo "  3) mongo/redis 密码需与 deploy-us/.env 完全相同"
echo "  4) discovery.yml 若有多个 address 块，确认改到的是 etcd 那处（脚本只改第一处并会告警）"
