# LoveACE 后端维护手册

面向运维/接手开发者。设计原理见 `server/DESIGN.md`;工程现状与诚实缺陷
清单见 `AGENTS.md`。本手册讲**怎么跑、怎么救火、怎么换血**。

---

## 1. 部署架构

```
用户 ──HTTPS──> Caddy(宿主机,自动证书)
                 └─> 127.0.0.1:8080  loveaced 容器
                        └─> db 容器(postgres:16,仅 compose 内网)
```

- **域名**:`goace.linota.cn`(A 记录已指向服务器)。
- **Caddy**:宿主机已安装。站点配置在仓库 `deploy/caddy/goace.linota.cn`,
  放到 `/etc/caddy/caddy.d/`(主 Caddyfile `import caddy.d/*.conf`)后
  `systemctl reload caddy`。证书自动签发,无需手动。
- **不要在代理层加缓存**:公开端点已带标准 HTTP 缓存头(30s~300s),
  `/v1/me/*` 永不缓存;代理层再缓存会出正确性问题。
- loveaced 端口只绑 `127.0.0.1`——数据库端口**不**对外。

## 2. 首次部署

> **2026-10-15 已按本节完成首次部署**(Debian 12 + podman 4.3.1 +
> podman-compose 1.6.0 via pip)。以下流程即实况;重启栈直接看 §5。

```sh
ssh <服务器>                      # 连接方式见 AGENTS.local.md
git archive HEAD | ssh ... 'tar -xf - -C /root/goace'   # 或 git clone
cd /root/goace/deploy
cp .env.example .env
vi .env                            # 生成并填写密钥(下节)

# 镜像手动构建(podman-compose 1.x 的 build: 不可靠)
cd /root/goace
podman build -f deploy/Containerfile -t localhost/loveaced:latest .

# 起栈:podman-compose 会把 podman run 命令(含 env_file 读出的密钥)
# 回显到 stdout——丢弃输出,别让密钥进任何日志
/usr/local/bin/podman-compose -p goace -f deploy/compose.yml up -d > /dev/null 2>&1
curl -s http://127.0.0.1:8080/healthz   # → ok
```

注意:
- db 容器用 **trust 认证 + 不发布端口**(仅 compose 内网可达)——单机
  内网里密码不提供额外防线,反而会泄进命令行;主机侧管理走
  `podman exec goace_db_1 psql -U loveace loveace`。
- podman-compose 的 `depends_on: service_healthy` 不生效:loveaced 先于
  db 就绪启动时,pg 层自动重试到 db 健康(实测自愈),日志开头几条
  connection refused 属正常,末行应为 `listening on :8080 (dev=false)`。

上线前检查:
- [ ] `curl -sI https://goace.linota.cn/healthz` → 200,TLS 证书有效
- [ ] 启动日志**没有** `DEV MODE` 字样(有 = 密钥没读上,凭据不持久)
- [ ] `LOVEACED_ADMIN_TOKEN` 已设(不设 = 管理面整体 404,无告警)

## 3. 密钥管理(全部在 `deploy/.env`)

| 变量 | 用途 | 丢失后果 |
|---|---|---|
| `LOVEACED_KEK_HEX` | 凭据保险库主密钥(AES-256-GCM) | **所有已存学校密码报废**,用户需重新登录 |
| `LOVEACED_PEPPER_HEX` | 账号指纹 HMAC | 指纹对不上,账号体系失联(可重建) |
| `LOVEACED_JWT_SEED_HEX` | Ed25519 访问令牌 | 全部令牌失效,用户重新登录(无损) |
| `LOVEACED_ADMIN_TOKEN` | 管理端点 | 换掉即可,旧令牌失效 |

- 生成:`openssl rand -hex 32`(每个独立)。
- **KEK 必须库外备份**(密码管理器/离线介质)。数据库快照里的凭据 blob
  都是 KEK 加密的——"有备份没钥匙"等于没有备份。
- 轮换:KEK 支持 `LOVEACED_KEK_VERSION` 版本号但**重加密流程未实现**,
  现阶段 KEK 不可轮换(已知限制);JWT seed 可随时换(踢全部登录)。
- 事故"凭据全失效":检查 KEK 是否被改 → 改回原值重启即可。

## 4. 数据库

- 迁移自动应用(内嵌,启动时按版本执行,已到 **0006**)。
- 备份:`podman exec goace_db_1 pg_dump -U loveace loveace | gzip > backup.sql.gz`
  (建议 cron 每日;blob 已加密,备份文件可放普通存储)。
- 恢复:起空库 → `gunzip -c backup.sql.gz | podman exec -i goace_db_1 psql -U loveace loveace`
  → 启动 loveaced(迁移会跳过已应用版本)。
- 表属主问题:若曾用临时角色跑集成测试,迁移可能报"非属主"——
  `REASSIGN OWNED` 后删角色(见 AGENTS.local.md 的历史记录)。

## 5. 升级

```sh
git -C /root/goace pull   # 或重新 git archive 传输
cd /root/goace
podman build -f deploy/Containerfile -t localhost/loveaced:latest .
/usr/local/bin/podman-compose -p goace -f deploy/compose.yml up -d > /dev/null 2>&1
```

- **换镜像必须强制重建容器(2026-10-17 实测)**:podman-compose 1.6 的
  `up -d` **不检测镜像变化**——镜像 tag 名字相同就跳过重建,容器继续跑
  旧二进制。先 `podman rm -f goace_loveaced_1` 再 `up -d`,再用
  压测工具未随开源仓库发布,版本核对见私有记录。
- **真库用户压测**(工具未随开源仓库发布)——默认最多
  100 库内用户同时登录(库不够就用库里全部),不限速。只读不写库,不打印
  凭据。工具会先对全部出口做一轮延迟探测(与线上同款探测),再按带宽
  租借端口,结果才代表线上形态。**注意:压测会为每个用户真实登录一次
  (烧各自端口的失败额度);节奏跑(-rate 60)在宿主少时会触发借位验证
  挤兑,结果里大量假判 invalid 属预期(2026-10-17 实测),以爆发跑为准。**
  2026-10-20 复测:100 用户同时爆发 98 ok,p50 7.7s / p95 20.5s。

- 本机交叉编译兜底(不用容器):
  `GOOS=linux GOARCH=amd64 go build -o bin/loveaced-linux ./server/cmd/loveaced`
- 回滚:`git checkout <旧tag> && up -d --build`。迁移向前兼容设计,
  未做降级迁移;回滚前看新迁移是否已应用(0005 目前只加表,安全)。
- **`-p goace` 不能漏**(2026-10-15 事故):podman-compose 1.6 按 compose
  文件所在目录命名项目,漏掉 `-p` 会另起一套 `deploy_*` 栈并挂上**空的数据卷**
  (`deploy_pgdata`),线上流量静默打到空库。恢复:`down` 掉误建栈、
  `podman volume rm deploy_pgdata`、带 `-p goace` 重新 up。
- SIGTERM 优雅退出(在途请求排空),重启前不需要手动摘流量。

## 6. 日常巡检(全部只读)

| 看什么 | 命令/端点 | 健康值 |
|---|---|---|
| 存活 | `GET /healthz` | 200 `ok` |
| 分层健康 | `GET /v1/public/status` | 各层 healthy/unknown;`gateway_probe` alive |
| 会话组+验证码 | `GET /v1/admin/sessions`(Bearer ADMIN_TOKEN) | captcha 熔断 closed;rejected 率无持续攀升 |
| 用量/成功率 | `GET /v1/admin/stats` | success_rate 无持续下滑 |
| 修复器漂移 | 同上 stats 的 parse_errors | 某操作 parse_error 上升 = 学校改版 |
| 冷却中账号 | `GET /v1/admin/health` | 无长期 cooling |

`/v1/public/status` 的 `history` 是最近 1 小时逐分钟样本,直接看趋势。

## 7. 事故处理

### 7.1 大面积 temporarily_unavailable(登录闸排队)
- 含义:全局 EC 登录闸(默认 60 次/分)排队超时,属**保护行为**。
- 处置:看 `/v1/admin/sessions` 的登录成功率与验证码计数;确认是否
  处于验证码污染窗口(7.2)。持续高载考虑调登录闸(需改代码,当前无 env)。

### 7.2 验证码污染(`gateway_probe: polluted`,时间线密集)
- 含义:服务器直连出口 IP 在 EC 网关侧进入验证码模式(同 IP 2 次失败
  登录触发),约 **3 分钟自愈**(实测多次一致)。
- 处置:**等待 + 停止无谓重试**。系统已自带:三循环自动解环带登录过闸,
  接受率崩塌时熔断开闸(10 分钟)回退人工挑战——`/v1/admin/sessions`
  的 captcha 计数可见。**洪水期(熔断打开)不要硬解**,等自愈。

### 7.3 学校某服务改版(parse_error 上升)
- `/v1/admin/stats` 找到 parse_error 漂移的操作 → 对应 `core/services/<x>`
  解析器对照真实页面修 → 正常发版。用户侧表现为该操作 502
  `school.parse_changed`,其余服务不受影响。

### 7.4 大面积 invalid_credentials
- 含义:vault 里的学校凭据被学校侧拒绝(改密/数据陈旧),不是故障。
- 处置:看 `/v1/admin/health` 的凭据状态分布;受影响用户会被
  invalid-credential hook 标记,前端直达密码提示,无需运维介入。
  若伴随验证码洪水 = 网关密码漂移面较大,等污染自愈后观察恢复。

### 7.5 用户报"写操作结果不确定"(409 outcome_unknown)
- 语义正确:写请求(缴费/报名等)发出后网络歧义,系统**不重放**。
- 对账:让用户重查对应记录(一卡通流水/报修单);同一 Idempotency-Key
  重试会拿到同样的 unknown(不会二次扣费)。新操作用新 key。

## 8. 数据与隐私

- 学校密码:AES-256-GCM 信封加密落库;池 cookie:TWFID-only + 独立 AAD
  域加密;日志/审计**不含**凭据与 cookie(设计红线,见 AGENTS §3)。
- 用户冷却(§9.1):hot/warm/cold/dormant;cold 30 分钟驱逐(快照加密
  落库 + 本地登出),dormant 7 天删快照。探测在闲置时完全停止。
- 注销(未来):硬删除 + 销毁 DEK(设计 §14,未实现)。

## 9. 已知未验证项(来自 DESIGN §12,上生产后按需校准)

- **E11**:TWFID-only 派生的实网验证(mock 已过)——池的安全根基,
  建议上线首周内完成一次只读实测。
- **E7**:第 6 个 UAAP 会话的行为(mock 拒绝 409,实网未测)。
- **E2**:验证码阈值/计数窗口细节;**E10**:TWFID 空闲 TTL(被动观察)。
- 评教三操作(评教开关关闭 + 写操作未授权,逻辑已按 mock 验证)。

## 10. 应急开关

| 场景 | 动作 |
|---|---|
| 管理令牌疑似泄露 | 换 `LOVEACED_ADMIN_TOKEN` 重启 |
| 验证码熔断长期打开 | 看接受率与污染窗口;确认模型路径/ORT 正常(solver 缺失会只降级不报错) |
| 某服务要下线维护 | `POST /v1/admin/maintenance {"layer":"jwc","minutes":60}` |
| 自动评教需要紧急关停 | `POST /v1/admin/teacher-eval/pause`,body `{"paused":true}`:拒绝新建、停止运行中任务(失败类型 `paused`)。或去掉 `LOVEACED_TEACHER_EVAL_ENABLED` 后重启。池状态:`GET /v1/admin/teacher-eval/pool` |
| 全站下线 | `podman-compose -p goace -f deploy/compose.yml down`(数据卷保留) |
| 灾难性 KEK 丢失 | 承认凭据报废:清 credentials 表,用户重新登录 |
