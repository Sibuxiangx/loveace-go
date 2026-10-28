# LoveACE (Go)

安徽财经大学校园服务的一体化 Go 后端:一个核心库、一个 CLI、一个面向
至多 2000 人同时在线的后端服务。从 Rust 版 LoveACE-core 迁移而来。

> **状态(2026-10-15)**:核心库 + CLI + 后端 M0~M6 全部实现并通过
> `go test -race ./...`(31 包 / 213 测试)。准备开源中(见文末)。

## 它是什么

学校的服务(教务、一卡通、第二课堂、后勤电费、报修……)没有公开 API,
客户端要自己过学校的认证链。LoveACE 做了三件事:

1. **`core/` — 学校连接器**:实现完整的认证链(EasyConnect 网关 RSA +
   UAAP/CAS 3DES)与全部 73 个操作的抓取/解析,统一暴露为带类型参数的
   操作目录(`core/ops`)。读请求带会话过期自愈;写请求绝不重放
   (`outcome_unknown` 语义)。
2. **`cmd/loveace` — CLI**:`call` / `shell` / `operations` / `self-test`
   / `bench`,人在终端里直接用全部能力。
3. **`server/` — loveaced 后端**:在连接器之上加了产品化的部分——
   账号体系(信封加密的凭据保险库)、Ed25519 访问令牌 + 刷新轮换、
   读穿缓存、公告、校历、监测与管理端点。

## 关键设计:EC 会话池

验证码与失败额度只存在于 **EasyConnect 网关登录层**。所以:

- 登录成功的网关会话(TWFID)进**池**复用;
- 任何用户的 UAAP 身份登录都从池里**派生**一个网关会话来完成,
  不再逐用户硬冲网关登录;
- 池有惰性验证(并发 ≤2 + 抖动)、反 pinning(最少在途 + 随机
  tie-break)、per-slot 速率上限、加密落库与重启恢复;
- **全局闸门**:EC 登录 ≤2 次/分钟(带抖动)、CAS 派生并发 ≤8(排队
  超时 503 `school.busy`)——用户数与封禁风险解耦,绝不硬冲。
- 压测:2000 虚拟用户登录 + 查成绩,32 个池槽全部派生完成,
  **网关凭据提交 0 次**,无 goroutine 泄漏。

## 快速开始

### 后端(dev 模式,零配置)

```sh
go run ./server/cmd/loveaced          # :8080,GET /healthz 探活
# 缺密钥时自动进 dev 模式:临时密钥 + 内存存储,重启即失,别用于生产
```

### 后端(生产,podman)

```sh
cp deploy/.env.example deploy/.env    # 填好密钥,见 docs/maintenance.md
podman compose -f deploy/compose.yml up -d --build
# TLS 由宿主机 Caddy 终结:deploy/caddy/goace.linota.cn
```

### CLI

```sh
go build -o bin/loveace ./cmd/loveace
./bin/loveace operations          # 73 个操作
printf 'auth.login\njwc.academic_info\n' | \
  ./bin/loveace shell --env-file .env.test --non-interactive
```

## 接口一览

| 面 | 端点 |
|---|---|
| 认证 | `POST /v1/auth/sessions`(登录/刷新/验证码挑战)、设备管理 |
| 学术 | `/v1/me/academic/*`(summary/terms/scores/plan/exams/timetable + **校历合并视图**) |
| 全操作 | `POST /v1/me/ops`(73 项操作;写操作强制 `Idempotency-Key`) |
| 公开 | `/v1/public/status`(分层健康 + 网关探测 + 分钟桶)、`/v1/public/calendar`、`/v1/public/announcements` |
| 管理 | `/v1/admin/pool|health|maintenance|stats|pool/evict|drain`(LOVEACED_ADMIN_TOKEN 守卫) |

错误统一为 RFC 9457 problem+json,带稳定 `code` 扩展。

## 文档

- `server/DESIGN.md` — 后端设计记录(EC 池方案、边界清单、实验记录)
- `docs/maintenance.md` — **维护手册**:部署、密钥、备份、事故处理
- `docs/STATUS.md` — 逐操作迁移状态(部分过时,以 AGENTS.md 为准)
- `AGENTS.md` — 面向 AI agent 的工程现状与硬性约束

## 安全模型(摘要)

- 学校密码:信封加密(AES-256-GCM,KEK 走环境变量,`KeyProvider` 预留
  KMS);泄露面 = 单机环境变量。
- 池内 cookie:只捕 TWFID(绝不含 CAS 票据),独立 AAD 域加密落库,
  重启后一律恢复为 unverified(不信任磁盘上的"健康"声明)。
- 响应消毒:`/v1/auth` 任何错误响应绝不回显请求里的密码。
- 管理端点:令牌未配置 = 路由不存在(404),不存在"裸奔的管理面"。

## 开源前的待办

- [ ] 确定许可证(LICENSE)与版权声明
- [ ] 品牌与措辞审查(README/文档是否暴露内部主机名——`*.aufe.edu.cn`
      为学校真实服务,开源时保留代码、文档中只保留"示例校园"措辞或按需保留)
- [ ] 移除/保留 `AGENTS.md` 的取舍(它如实记录了实测结论,是核心知识)
- [ ] 私有文件(`.env*`、`AGENTS.local.md`、`deploy/.env`)已确认 gitignore

## 许可证

开源许可待定(见上)。在此之前,本仓库仅限内部使用。
