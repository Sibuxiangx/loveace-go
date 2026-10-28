# LoveACE 前端接入指南

给前端同学的开工文档。接口真源:OpenAPI spec(已导入 API 客户端即可看到
全部 27 个 huma 路径的请求/响应 schema):

- `loveace-openapi-3.1.json`(OpenAPI 3.1,推荐)
- `loveace-openapi-3.0.json`(3.0.3,工具不认 3.1 时用)
- 在线版:https://goace.linota.cn/docs (Stoplight Elements)

---

## 0. 基址与全局约定

- **BaseURL**:`https://goace.linota.cn`(spec 的 `servers` 已写死)。
- 成功响应体除说明外都是 `application/json`;**一切错误**都是
  RFC 9457 `application/problem+json`:

```json
{
  "title": "Conflict",
  "status": 409,
  "code": "school.captcha_required",
  "detail": "学校网关需要验证码，请取图后提交",
  "challenge_id": "ab12…",
  "retry_after": 30
}
```

**永远先判 `code`(稳定枚举,可写分支),`detail` 只给用户看**,别解析
中文文案。最常用的 code:

| code | HTTP | 前端该做什么 |
|---|---|---|
| `auth.invalid_token` / `auth.token_expired` | 401 | access 缺失/过期 → 静默刷新一次;刷新也 401 → 登录页 |
| `auth.school_login_required` / `school.credentials_invalid` | 401 | 学校会话没了/学校密码失效 → 登录页(后者提示重输密码) |
| `school.captcha_required` | 409 | 进入验证码流程(§3) |
| `auth.account_cooling` | 429 | 密码错太多次,展示 `retry_after` 秒倒计时 |
| `rate_limited` | 429 | 登录接口按 IP 限流,同样展示 `retry_after` |
| `school.busy` | 503 | 学校派生通道排队满,提示稍后重试;读可先用缓存数据 |
| `school.outcome_unknown` | 409 | **写操作对账**(§4),绝不可换 key 重发 |
| `auth.session_limit` | 409 | 该账号 5 个会话满了;`sessions[]` 里有活跃设备,引导去吊销 |
| `idempotency_in_progress` | 409 | 同 key 正在执行,轮询/稍后重试 |
| `school.unavailable` / `school.parse_changed` / `school.server_error` | 503/502 | 学校侧问题,展示友好错误 |

**第二课堂/劳育(Second Classroom/Labor) 的 ticket 故障自愈**:这两类操作
走学校 CAS 票据,票据是乐观使用的——缓存优先、基本不主动刷新。若
`aac.*`/`labor.*` 操作**连续**返回 502 `school.http_error`(而其它功能
正常),大概率是票据过期:依次调用本地控制操作
`aac.reset_ticket` / `labor.reset_ticket`(写操作,带 Idempotency-Key,
但不产生学校侧写入),然后重试原操作一次。不要在失败时自动反复重试。

## 1. 认证

### 登录

`POST /v1/auth/sessions`

**多设备共享一条学校会话**:如果你的账号已有一台设备在线,且提交的
密码与上次一致,新设备的登录**完全不触碰学校**(零验证码面、瞬时
完成);所有设备内部共用同一条学校会话。改过学校密码后首次登录会
走完整验证并刷新服务端保存的凭据。

`POST /v1/auth/sessions` 的请求体:

```json
{
  "account": "2024xxxx",
  "password": "学校密码",
  "gateway_password": "",          // 可选,留空 = 同学校密码
  "device_name": "小王的MacBook",   // 用户可见,会出现在会话列表
  "platform": "web"                 // web|ios|android|desktop
}
```

200 → `TokenPair`:

```json
{
  "access_token": "…",    // 15 分钟
  "refresh_token": "…",   // 30 天,一次性(轮换)
  "token_type": "Bearer",
  "expires_in": 900,
  "user": { … }            // 掩码后的账号信息
}
```

错误分支:
- 409 `school.captcha_required`(带 `challenge_id`)→ 验证码流程(§3),
  用户体验上登录框进入"输验证码"状态;
- 401 `school.credentials_invalid` → 密码错,**一次即停**(后端会进入
  冷却阶梯:0/30s/2m/10m/30m,重试只会更久);
- 409 `auth.session_limit`(带 `sessions[]`)→ 该账号已在 5 台设备在线,
  引导用户先登出别的设备(或调 §2 的设备吊销)。

### 后续请求

```
Authorization: Bearer <access_token>
```

**静默刷新**(401 拦截器标准动作):

1. 请求收到 401(`auth.invalid_token`/`auth.token_expired`)→ 调
   `POST /v1/auth/tokens/refresh` {refresh_token} → 新的 TokenPair →
   重放原请求(仅一次);
2. 刷新收到 401 → refresh 已失效/**被复用**(复用会吊销该设备全部令牌,
   防泄漏)→ 清本地,回登录页;
3. **并发保护**:多个 401 同时到达时只放行一个刷新请求,其余挂起等结果;
4. refresh_token 是**轮换制**——每次刷新旧 token 立即作废;复用一个已用
   的 refresh 会被判定为令牌泄漏,整个设备会话作废(这是特性不是 bug)。

### 登出

`DELETE /v1/auth/sessions/current` → 只注销当前设备的绑定(所有设备
内部共用一条学校会话,登出一台设备不影响其他设备);**最后一台设备
登出时**才整体拆除学校会话。远程登出别的设备用
`DELETE /v1/me/devices/{id}`,同样按引用计数处理。

### 设备管理

- `GET /v1/me/devices` → 活跃设备列表(含每台的学校会话);
- `DELETE /v1/me/devices/{id}` → 登出该设备并释放其 UAAP 会话额度
  (同账号最多 5 个并发会话,§session_limit)。

## 2. 学术数据(Envelope 语义)

`/v1/me/academic/*` 全部返回:

```json
{ "data": { … }, "meta": { "source": "live", "fetched_at": "…", "stale": false } }
```

`meta.source`: `live`(现取) | `cache`(缓存命中) | `stale`(学校故障时
的兜底旧数据——**可以渲染,但要给用户标注"数据可能不是最新"**)。

| 端点 | 缓存 TTL | 说明 |
|---|---|---|
| `GET /v1/me/academic/summary` | 60s | 学业概览(GPA/学分) |
| `GET /v1/me/academic/terms` | 1h | 可选学期列表(其他端点的 term 参数从这里来) |
| `GET /v1/me/academic/terms/{term}/scores` | 5m | 单学期成绩 |
| `GET /v1/me/academic/plan` / `plan/completion` | 1h | 培养方案树 |
| `GET /v1/me/academic/exams` | — | 考试聚合(统考+其他) |
| `GET /v1/me/academic/timetable?term=` | — | 原始课表 |
| `GET /v1/me/academic/timetable/merged?term&from&to` | — | **课表×校历合并视图**(见下) |

### 合并课表(课表 × 校历 × 考试)

`GET /v1/me/academic/timetable/merged?term=2026-2027-1-1&from=2026-12-01&to=2026-12-31`

返回逐日:

```json
{
  "date": "2027-01-10",
  "kind": "teaching",          // teaching | holiday | makeup | vacation
  "week_no": 18, "weekday": 7,
  "follows": { "week": 4, "weekday": 5 },   // 仅 makeup:按第4周周五的课上
  "classes": [ { "course_name": "…", "teacher": "…", "location": "…",
                 "start_session": 1, "end_session": 2 } ],
  "exams": [ { "course_name": "大学英语（期末）", "exam_type": "…",
               "time": "09:00-11:00", "location": "二教 203", "note": "…" } ]
}
```

- 假期日(`holiday`/`vacation`)`classes` 为空;`makeup` 日按 `follows`
  指向的周次带课;
- **考试按自己的日期出现,无视 kind**——教学 16~18 周结束后、寒假里的
  考试都会在 `exams` 字段,前端渲染时别按 kind 过滤考试;
- `exams`/`classes` 空时字段省略(注意 `undefined` 判断)。

## 3. 验证码挑战流

登录(或刷新/操作)返回 409 `school.captcha_required` + `challenge_id` 后:

1. `GET /v1/auth/challenges/{id}/image` → GIF 字节(`image/gif`),直接
   `URL.createObjectURL` 展示;
2. `POST /v1/auth/challenges/{id}/solution` {code: "ab12"} →
   - 200 → TokenPair(登录完成);
   - 409 `school.captcha_required`(新 challenge_id)→ 码错了,取新图重输
     (challenge 被消费,必须用新的 id);
   - 挑战 TTL 3 分钟,`404` = 过期/不存在 → 回登录态重开。

## 4. 通用操作面 `POST /v1/me/ops`

一个端点通吃其余全部能力(一卡通/第二课堂/后勤/报修/会话控制),与 CLI
同一套操作目录。参数 schema 不在 OpenAPI 里(通用端点),操作名录与参数
用这两个来源:`core/ops/ops.go` 的注册表(权威)或 `./bin/loveace operations`。

```json
POST /v1/me/ops
{ "op": "ykt.balance", "args": {} }
```

- 读操作直接返回结果 JSON;
- **写操作**(缴费/报名/签到/报修提交/图片上传)**必须**带
  `Idempotency-Key: <uuid v4>`(缺键 428):
  - 生成一次,**重试时用同一个 key**(系统保证绝不重复执行,重试返回
    第一次的结果);
  - 409 `school.outcome_unknown`:操作已发出但结果不明 → **对账**——
    缴费对 `ykt.recent_purchase_history`,报名对活动列表,报修对
    `repair.orders`;同 key 重试永远返回同一个 unknown(不会二次扣费);
  - 422 `idempotency_key_mismatch`:同 key 不同参数,是前端 bug;
  - 崩溃恢复:执行器中断的 key 等价于 unknown,同样对账。
- 常用操作速查:`ykt.balance` / `ykt.recent_transactions` /
  `ykt.utility_pay`(写)/ `aac.credit_list` / `labor.apply_activity`(写) /
  `labor.scan_sign_in`(写)/ `isim.electricity` / `repair.orders` /
  `repair.submit`(写)。

## 5. 公开数据(无需登录,注意缓存)

- `GET /v1/public/status` → 分层健康 + `gateway_probe`(alive/polluted/
  down)+ 最近 1 小时逐分钟样本;登录页可以拿它展示"学校服务状态";
- `GET /v1/public/calendar?term_id&from&to` → 公开校历(只渲染已发布集);
  带 `ETag`,重访带 `If-None-Match` 可拿 304;
- `GET /v1/public/announcements` → 公告(60s 缓存 + SWR)。

这些端点在 huma spec 之外(plain 路由),字段同 `DayInfo`/公告 schema。

## 6. CORS 与本地开发

服务器已配置的允许来源(**精确匹配**):
`http://localhost:5173`(Vite 默认)、`http://localhost:3000`、
`http://localhost:4173`、`http://127.0.0.1:5173`。

- 用其他端口/源,联系后端把 origin 加进服务器的
  `LOVEACED_CORS_ORIGINS`(重启生效);
- 预检允许的头:`Authorization`、`Content-Type`、`Idempotency-Key`;
- **不带 cookie/credentials**(token 全在头里),fetch 不要开
  `credentials: "include"`。

## 7. 最小 TypeScript 封装(建议)

```ts
const BASE = "https://goace.linota.cn";
let access = "", refresh = "";

async function api<T>(path: string, init: RequestInit = {}, retry = true): Promise<T> {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(access && { Authorization: `Bearer ${access}` }),
      ...init.headers,
    },
  });
  if (res.status === 401 && retry) {          // 静默刷新一次
    const r = await fetch(`${BASE}/v1/auth/tokens/refresh`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: refresh }),
    });
    if (!r.ok) { location.href = "/login"; throw new Error("relogin"); }
    const t = await r.json();
    access = t.access_token; refresh = t.refresh_token;
    return api<T>(path, init, false);
  }
  if (!res.ok) throw await res.json();         // problem+json,判 .code
  return res.json();
}

// 写操作:同一次逻辑操作的所有重试共用一个 key
const runOp = (op: string, args: object, key: string) =>
  api("/v1/me/ops", {
    method: "POST",
    headers: { "Idempotency-Key": key },
    body: JSON.stringify({ op, args }),
  });
```

## 8. 联调自检清单

- [ ] 登录 → 拿到 TokenPair → `GET /v1/me` 正常;
- [ ] 15 分钟后请求 401 → 自动刷新 → 重放成功;
- [ ] 刷新后用旧 refresh_token → 401 → 回登录页(复用检测生效);
- [ ] summary 的 `meta.source` 在两次请求间 live→cache;
- [ ] merged 课表选一个考试周区间能看到 `exams`;
- [ ] 写操作:断网重试同 key → 同一结果;未知结果 → 对账路径;
- [ ] `rate_limited`/`auth.account_cooling` 的倒计时 UI。
