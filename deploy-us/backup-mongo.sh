#!/usr/bin/env bash
# 【在美服生产机上跑】—— mongodump 备份 openim_v3 到 ${MONGO_BACKUP_DIR}/<时间戳>/，保留最近 KEEP 份。
# 备份目录已由 compose 挂进 mongo 容器（/data/backup），dump 在容器内执行、产物落宿主。
# cron 示例（每天美东 4 点，crontab -e）：
#   0 4 * * * cd /dl/deploy-us && bash backup-mongo.sh >> /var/log/openim-backup.log 2>&1
set -euo pipefail
cd "$(dirname "$0")"

# 读 .env 拿 MONGO_ROOT_PASSWORD / DATA_DIR / MONGO_BACKUP_DIR（密码别含 $ 空格 引号，shell 与 compose 解析会不一致）
set -a; . ./.env; set +a

KEEP="${KEEP:-7}"
STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="${MONGO_BACKUP_DIR%/}"

echo "== mongodump → $BACKUP_DIR/$STAMP =="
docker exec mongo mongodump \
  -u root -p "$MONGO_ROOT_PASSWORD" --authenticationDatabase admin \
  --db openim_v3 --out "/data/backup/$STAMP" --quiet
echo "备份完成: $BACKUP_DIR/$STAMP"

# 只保留最近 KEEP 份（按目录 mtime 排序）
ls -1dt "$BACKUP_DIR"/*/ 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -rf
echo "清理完成，保留最近 $KEEP 份"
