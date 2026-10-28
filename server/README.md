# server/ — LoveACE Go 后端服务器(预留)

独立的 HTTP API 服务器。设计见 `DESIGN.md`(2026-10-14 修订版:EC 会话池方案)。

## 规划

- 目标:至多 2000 人同时在线,单实例(`loveaced`)+ PostgreSQL 起步
- 学校连接:复用 `core/`(登录、抓取、解析),`ops` 目录 CLI 与服务器共用
- 会话模型:**EC 会话池**——登录成功的 EC 网关会话入池复用,UAAP 登录从池
  派生,单账号最多 5 个活跃 UAAP 会话;出口为服务器直连
  (CF Worker 出口方案已于 2026-10-14 放弃,实测不可行,见 DESIGN §1)
- 技术栈:Huma v2 + chi + pgx v5/sqlc/goose + River(见 DESIGN §0)

## 目录约定(开发时)

```
server/
├── cmd/loveaced/   main.go
└── internal/
    ├── api/        HTTP 路由/handler
    ├── ecpool/     EC 网关会话池
    └── auth/       后端自身鉴权(与学校凭据无关)
```

状态:未开始,本目录仅占位。
