# 美服 OpenIM 部署手册（deploy-us · build/run 分离）

美服（Lumiscape）独立部署一套 OpenIM，物理隔离国服，一步解决 **ID 撞车 + 合规（数据不落中国）+ 跨太平洋延迟 + 推送可达**（推送需启用 FCM，见「离线推送」小节）。DLServer 侧零代码，只改 `application-us.yml` 的 `imserver` 三项。

> **build/run 分离（save/load）形态**：镜像在 **build 机**构建好、`docker save` 成 tar，scp 到 **生产机** `docker load` 直接跑。
> **生产机零源码、零 Go、零编译**——只要 `compose + .env + config + 镜像 tar`。

## 两个角色

| 角色 | 机器 | 装什么 | 干什么 |
|---|---|---|---|
| **build 机** | 有源码的任意机器（开发机即可） | docker（含 buildx） | `build-and-save.sh`：build amd64 镜像 + save tar + 打包 config |
| **生产机** | 美服 VM（amd64） | docker + swap | 收部署包 → `patch-config.sh` → `deploy.sh`（load + up） |

> **架构**：美服是 **amd64**，镜像必须 build 成 amd64。Dockerfile 的 builder 阶段用 `--platform=$BUILDPLATFORM` + Go 交叉编译：重活（依赖下载、编译）在 build 机**原生架构**跑，arm64 mac 上构建 amd64 镜像也只要几分钟；仅最终镜像的轻量 RUN（apk、go get gomake）走 QEMU。

## 组件范围

| 组件 | 部署 | 说明 |
|---|---|---|
| mongo / redis / kafka / etcd | docker（生产机 pull 公共镜像） | 端口**绑 127.0.0.1**、TZ 美西、换默认密码 |
| OpenIM server（12 服务） | docker 单容器（**预置镜像**，build 机构建） | api/msggateway/msgtransfer/push/crontask + 7 rpc；`restart:always` + autoheal |
| OpenIM_Chat（chat + admin 后台） | docker 单容器（预置镜像） | 运营后台 |
| autoheal | docker | healthcheck unhealthy 时自动重启容器 |
| 反代 | Caddy 自动证书 | 替代国服 acme.sh + nginx |
| MinIO / livekit / v2ray | ❌ 不部署 | 对象存储由 DLServer 自管；离线推送见「离线推送（FCM）」小节（默认未启用） |

## VM 规格（生产机）

- **4 核 / 8G / ≥100G SSD，美西（amd64）**——几百在线的新区轻载配置。
  - 全 docker 内存硬顶：basics ≈ 3.9g（mongo 1.5g/cache 0.4、redis 1g、kafka 1g/heap 448m、etcd 320m）**+ server 1.5g + chat 640m ≈ 6.1g**，留 ~2g。轻载实际远低于硬顶。
  - **必须配 ≥4G swap 兜底**（防峰值 OOM 杀 mongo/kafka）。生产机不 build，无编译内存压力，比本地 build 形态更宽裕。
  - 在线涨到上千 / 数据变大再扩 16G，把 compose 的 mem_limit 调回。
- 安全组：**只放通 80 / 443**（Caddy）。其余端口全绑 127.0.0.1。

---

## 首次部署

### A. 在 build 机（有源码）

```bash
# 1) 装 docker（含 buildx，Docker Desktop / docker-ce 自带）。不用装 Go/mage——都在镜像 builder 层。
# 2) 源码就位：OpenIM 和 OpenIM_Chat（或 OpenIMChat）放同一父目录下即可，
#    脚本自动探测（deploy-us 的父目录 = OpenIM，兄弟目录 = Chat）；不同布局用 OPENIM_DIR/CHAT_DIR 覆盖。

# 3) build amd64 镜像 + save + 打包 config（开发机直接跑即可）
cd <OpenIM 仓库>/deploy-us
bash build-and-save.sh          # 产出 openim-images-us.tar.gz + openim-config/ chat-config/

# 4) 把整个 deploy-us/ scp 到美服（仅首次全量；日常更新只传 tar，见下文）
rsync -avz deploy-us/ <美服>:/dl/deploy-us/
```

### B. 在生产机（美服 amd64）

```bash
# 1) 系统准备
sudo timedatectl set-timezone America/Los_Angeles
#    装 docker（不用 Go/mage）；开机自启 → 容器 restart:always 随之拉起（取代 systemd unit）
sudo systemctl enable --now docker
#    ★ 8G 机必配 swap 兜底
sudo fallocate -l 4G /swapfile && sudo chmod 600 /swapfile
sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
echo 'vm.swappiness=10' | sudo tee /etc/sysctl.d/99-swappiness.conf && sudo sysctl -p /etc/sysctl.d/99-swappiness.conf

# 2) 部署包已在 /dl/deploy-us（上面 rsync 过来的），进目录
cd /dl/deploy-us
cp .env.example .env
# ★ 编辑 .env：MONGO_ROOT_PASSWORD / MONGO_OPENIM_PASSWORD / REDIS_PASSWORD 改强密码；DATA_DIR 默认 /dl/openim-data

# 3) patch config（改 openim-config/ chat-config/ 的密码+容器地址）
export MONGO_OPENIM_PASSWORD='...' REDIS_PASSWORD='...' OPENIM_SECRET='...'   # 与 .env 一致
bash patch-config.sh

# 4) load 镜像 + 起全部服务（basics 首次 pull 公共镜像）
bash deploy.sh
```
> `deploy.sh` 会先做**前置校验**（.env 无 CHANGE_ME、config 已 patch）再 `docker load` → `docker compose up -d` → 等 healthy → 健康检查。
> ★ 校验的意义：mongo 只在**首次、空数据目录**时按 .env 初始化用户，用占位密码起过一次后再改 .env 不会更新已建用户，只能 mongosh 手改或清数据重来。
> **三处 secret 必须一致且不同于国服**：`openim-config/share.yml` secret == `chat-config/share.yml` 的 `openIM.secret` == DLServer `application-us.yml` 的 `imserver.secret`。沿用国服 secret 会导致两环境 token 互通（patch-config.sh 检测到会告警）。

### C. 反代 + 证书（Caddy，生产机）
```bash
# 装 Caddy（https://caddyserver.com/docs/install）
# ★ 编辑 Caddyfile：im-us.lumiscape.xxx → 真实域名；tls 邮箱换成你的；DNS A 记录指向本 VM，放通 80/443
sudo cp /dl/deploy-us/Caddyfile /etc/caddy/Caddyfile && sudo systemctl restart caddy
```
Caddy 反代宿主 `127.0.0.1` 的 10001/10002/10008，自动签发/续期 Let's Encrypt。
> **运营后台（admin-api 10009）默认不公网暴露**（chatAdmin 是众所周知的默认账号）。日常用 SSH 隧道访问：
> `ssh -L 10009:127.0.0.1:10009 <美服>` 后本地打开 `http://127.0.0.1:10009`；确需公网时在 Caddyfile 取消 `/complete_admin` 注释，且先改默认密码。

### D. 接入 DLServer
- **AI Bot：美服暂不开启**。DLServer 并没有启动时注册 bot 的逻辑（国服 botid=10 是历史手工注册的），美服空库里没有该用户，直接开会导致 AI 回复发送失败。后续要开时先手工注册一次（botid/botname 与 `application-us.yml` 的 `AIBot` 配置一致）：
  ```bash
  curl -X POST https://im-us.lumiscape.xxx/api/user/user_register \
    -H 'operationID: reg-ai-bot' -H 'token: <美服 imAdmin token>' \
    -d '{"users":[{"userID":"10","nickname":"AI 生成内容仅供参考"}]}'
  ```
- DLServer `application-us.yml` 的 `imserver`：
  ```yaml
  imserver:
    host: https://im-us.lumiscape.xxx/api    # 指向美服 IM
    secret: <patch-config 用的 OPENIM_SECRET>
    admin: imAdmin
    token: <用美服新实例 imAdmin 重新签发的 admin token>   # 国服 token 对新实例无效
  ```

---

## 离线推送（FCM，可选但建议）

**现状**：`openim-config/openim-push.yml` 的 `enable:` 为空 → OpenIM 走 dummy pusher，**离线推送是空操作**（在线消息收发不受影响，和国服现状一致）。要兑现"美服推送可达"，需启用 FCM：

1. [Firebase 控制台](https://console.firebase.google.com) 建项目 → 项目设置 → 服务账号 → 生成新的私钥，下载 serviceAccount JSON
2. 放进部署包 config（volume 挂载自动进容器）：`cp <下载的>.json /dl/deploy-us/openim-config/fcm-service-account.json`
3. 改 `openim-config/openim-push.yml`：
   ```yaml
   enable: fcm
   fcm:
     filePath: fcm-service-account.json   # 相对 config/ 目录
   iosPush:
     production: true                     # App Store 正式包为 true，TestFlight/开发包为 false
   ```
4. `docker compose --env-file .env restart openim-server` 生效
> 客户端侧需集成 FCM SDK 并向 OpenIM 上报 token，iOS 的 APNs 经 FCM 转发。

## 验证清单

1. `curl https://im-us.lumiscape.xxx/api/` → 连通（TLS 生效）
2. 客户端登录 → DLServer `LoginIM` 拿 token → WS `wss://im-us.../msg_gateway` 连上
3. 私信收发、群组建/入/退均正常；运营后台走 SSH 隧道 `http://127.0.0.1:10009` 可登录（AI Bot 美服暂不开启，见 D 步骤）
4. （启用 FCM 后）杀掉 App 进程发消息 → 手机收到系统推送

## 日常更新
```bash
# build 机：拉新代码 → 重 build+save → 只传镜像 tar
cd <OpenIM> && git pull && cd <OpenIM_Chat> && git pull
cd <OpenIM>/deploy-us && bash build-and-save.sh
rsync -avz openim-images-us.tar.gz <美服>:/dl/deploy-us/

# 生产机：load 新镜像 + 滚动重启
cd /dl/deploy-us && bash deploy.sh
```
> ⚠️ **日常更新只传 tar，千万别再全量 rsync `deploy-us/`** —— 会把 build 机上的原始 config 和模板 `.env` 盖掉生产机上**已 patch 的 config、真实密码 `.env`、FCM 凭证**。若源码 config 有新增配置项，人工 diff 后单独同步。

## 运维注意

- **崩溃自愈**：docker `restart:always` + autoheal。容器整体退出自动重启；开机自启由 `systemctl enable docker` 保证。
  - healthcheck = 端口探活 + **进程计数**（server 12 / chat 6）：任一 rpc 单崩或被 OOM 杀 → 计数不足 → unhealthy → autoheal 整容器重启，堵住"端口在、消息链路死"的盲区。
  - 若改动 start-config.yml 的服务数，同步改 compose 里 healthcheck 的计数阈值。
- **密码/secret/地址一致性**：mongo/redis 密码 `.env` 与 config 相同；三处 secret 一致且**不同于国服**；地址用容器服务名（patch-config 已处理，deploy.sh 会校验）。
- **数据备份**：mongo 数据在 `${DATA_DIR}/components/mongodb/data`（默认 `/dl/openim-data`，git 仓库外）。
  用 `backup-mongo.sh` 做 mongodump（默认保留 7 份，`KEEP` 可调），配 cron：
  ```
  0 4 * * * cd /dl/deploy-us && bash backup-mongo.sh >> /var/log/openim-backup.log 2>&1
  ```
  异地容灾再把 `${DATA_DIR}/components/backup/mongo/` rsync 到别的机器。
- **和国服彻底隔离**：独立 mongo/redis/etcd/kafka + 独立 secret，国服零影响。

## 排障 & 回滚

**起不来先看：**
```bash
docker compose --env-file .env ps                    # 谁没 Up / unhealthy
docker logs --tail=100 openim-server                 # server 12 进程启动日志（panic 一般在这）
docker logs --tail=100 openim-chat
```
- **server/chat 一直 unhealthy**：多半连不上 basics —— 查 ① mongo/redis 密码 `.env` 与 config 一致 ② config 地址是否已是容器服务名（`patch-config.sh` 跑过没）③ basics 四容器是否都起 ④ 进程计数不足：`docker exec openim-server sh -c "pgrep -f '[o]penim-' | wc -l"` 应为 12（chat 用 `pgrep -f '[_]output'` 应为 6），少了说明某进程崩了，看容器内日志定位。
- **`docker load` 失败/镜像架构不符**：确认 build 机 build 的是 amd64（`docker inspect openim-server:us --format '{{.Architecture}}'` 应为 amd64）。
- **改了 config 不生效**：config 走 volume 挂载，`docker compose restart openim-server` 即可，无需重新 load。

**回滚（更新后出问题）：**
```bash
# save/load 形态回滚很简单：留着上一版 tar，load 回去即可
docker load < openim-images-us-<上一版>.tar.gz   # 建议 build 机每次给 tar 带日期名留存
docker compose --env-file .env up -d
```
> 想留多版本：`build-and-save.sh` 里把 `IMAGE_TAG` 设成日期（如 `20260717`），tar 自然带版本名，回滚 load 对应 tar + 改 `.env` 的 `IMAGE_TAG` 即可。
