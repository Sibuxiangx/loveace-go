# server/ — loveaced 后端

loveaced 是 LoveACE 的 HTTP 后端：账号体系、凭据保险库、访问令牌、读穿缓存，
以及建立在 `core/` 连接器之上的 API。

- 部署与运维：见仓库根目录的 [README.md](../README.md)（自部署教程）与 [docs/maintenance.md](../docs/maintenance.md)。
- 设计原理与边界清单：[DESIGN.md](DESIGN.md)。
- 工程现状与已知限制：[../AGENTS.md](../AGENTS.md)。

## 目录

```text
server/
├── cmd/loveaced/     入口：读取配置、连接数据库、启动 HTTP 服务、优雅退出
└── internal/
    ├── api/          HTTP 路由与处理器（Huma v2 + chi，RFC 9457 错误）
    ├── cache/        读穿缓存与单飞
    ├── captchastore/ 验证码判卷结果沉淀（zstd 日切）
    ├── config/       环境变量配置与失败关闭检查
    ├── gate/         密码格式预校验
    ├── school/       学校会话管理：登录、验证码三段循环、熔断、恢复
    ├── store/        存储接口；mem/ 为内存实现，pg/ 为 PostgreSQL 实现（内嵌迁移）
    ├── teachereval/  自动评教后台任务池（默认关闭）
    ├── token/        Ed25519 访问令牌与刷新令牌轮换
    └── vault/        AES-256-GCM 信封加密与指纹
```

## 本地运行

缺少密钥或数据库时服务拒绝启动。本地一次性运行请设置 `LOVEACED_DEV=1`，
此时使用临时密钥与内存存储，重启即丢失数据。

```sh
LOVEACED_DEV=1 go run ./cmd/loveaced
```

## 测试

```sh
go test ./server/...
```

`server/internal/store/pg` 的集成测试需要真实的 PostgreSQL，通过环境变量 `LOVEACE_TEST_PG` 指定连接串；未设置时自动跳过。
