# LoveACE Go — 迁移状态

> **注意(2026-10-15)**:本文件为迁移期快照,**部分表述已过时**。权威
> 现状以 `../AGENTS.md`(§6 诚实清单、§7 验证状态)为准:全部 73 项操作
> 已完成 fixture 测试,其中 60+ 项已实网只读验证;已知语义澄清见
> AGENTS §6(如 `jwc.score_detail` 只支持当学期)。

对照 Rust `LoveACE-core` 的逐操作状态。三栏含义:

- **fixture-verified**:离线 mock 测试通过
- **live-verified**:2026-10-12 实网(真实账号,只读)验证通过
- **compile-only**:仅编译,未单独测试

门卡/门禁(door)全部 **EXCLUDED**(用户指令),不含任何 HTTP/BLE 端点。

## 基础设施

| Rust | Go | 状态 |
| --- | --- | --- |
| `config.rs` | `config` | fixture-verified |
| `error.rs` | `errs` | fixture-verified |
| `crypto.rs` (RSA PKCS#1 v1.5 手写、3DES ECB、MD5) | `crypto` | fixture-verified(stdlib 交叉解密) |
| `transport.rs` (单跳、Content-Length: 0、GBK) | `transport` | fixture-verified(httptest 线上行为) |
| `cookies.rs` (RFC6265、scope 清除) | `cookies` | fixture-verified |
| `detector.rs` (Sangfor/CAS 结构信号) | `detector` | fixture-verified |
| `session.rs` (状态机、single-flight 恢复、读写语义) | `session` | fixture-verified(-race) |
| `auth.rs` (EC/UAAP/验证码/心跳) | `session`(auth.go) | fixture-verified + live-verified |
| `retry.rs` | `retry` | fixture-verified |
| `events.rs` | `events` | fixture-verified |
| `diagnostics.rs` (Redactor、Report v1) | `diagnostics` | fixture-verified |
| `operations.rs` (catalog 73 项 + dispatch) | `ops` | fixture-verified |
| —(服务器预留) | `sessionpool`(分片/TTL/单飞)、`loveace.Client`(连接池/限速) | fixture-verified |

## Authentication

| 操作 | live | fixture |
| --- | --- | --- |
| `auth.login` | ✅ authenticated(gateway + UAAP) | ✅ |
| `auth.status` | ✅ | ✅ |
| `auth.heartbeat` | ✅ alive | ✅ |
| `auth.ec_login` / `ec_captcha` / `uaap_login` | (随 login 验证) | ✅(outcome 矩阵) |
| `auth.logout` | ✅ | ✅ |
| `auth.cancel` | — | ✅ |

## JWC

| 操作 | live | fixture |
| --- | --- | --- |
| `jwc.academic_info` | ✅(gpa 4.05, term 2026-2027-1-1) | ✅ |
| `jwc.training_plan` | ✅(2024级网络与新媒体) | ✅ |
| `jwc.terms` | ✅ | ✅ |
| `jwc.term_scores` | ✅(23 条) | ✅ |
| `jwc.all_term_scores` | — | compile-only(复用 term_scores 解析器) |
| `jwc.this_term_scores` | ✅(本学期未出分,空列表为正确形态) | ✅ |
| `jwc.score_detail` | — | ✅ |
| `jwc.exams` | ✅(当前无考试,空列表) | ✅ |
| `jwc.seat_info` | — | ✅(解析器随 exams 走) |
| `jwc.other_exams` | ✅(随 exams 验证) | ✅ |
| `jwc.plan_completion` | ✅(kind=info,分类树) | ✅ |
| `jwc.plan_options` | — | ✅ |
| `jwc.student_schedule` | ✅(17.5 学分,xkxx) | ✅ |
| `jwc.course_schedule_terms` | ✅ | ✅ |
| `jwc.course_schedule` / `_all` / `_term` | — | ✅ |
| `jwc.class_terms` / `class_departments` / `class_subjects` / `class_list` / `class_curriculum` | — | compile-only |
| `jwc.teacher_eval_courses` | — | ✅ |
| `jwc.teacher_eval_prepare` / `submit` / `verify` | **不做实网验证(写操作)** | ✅(含 seed 确定性) |

## YKT

| 操作 | live | fixture |
| --- | --- | --- |
| `ykt.balance` | ✅(10.68元) | ✅ |
| `ykt.transactions` / `recent` / `monthly` | ✅ recent(区间内无记录) | ✅ |
| `ykt.utility_page_info` / `utility_options` | — | ✅(options) |
| `ykt.utility_pay` | **不做实网验证(写操作)** | compile-only |
| `ykt.purchase_history` / `recent_purchase_history` | — | ✅(history) |

## AAC / Labor

| 操作 | live | fixture |
| --- | --- | --- |
| `aac.ticket` | ✅(随 credit_info) | ✅ |
| `aac.credit_info` | ✅(total 29,已认定) | ✅ |
| `aac.credit_list` | — | ✅ |
| `labor.progress` | ✅(sum 0.28, progress 70) | ✅ |
| `labor.joined_activities` / `joined_clubs` / `club_activities` | — | ✅(activities) |
| `labor.apply_activity` / `scan_sign_in` | **不做实网验证(写操作)** | compile-only |
| `labor.sign_list` / `activity_detail` | — | ✅(detail) |
| `aac.reset_ticket` / `labor.reset_ticket` | ✅(expire_service_ticket 路径) | ✅ |

## ISIM / Competition / Repair

| 操作 | live | fixture |
| --- | --- | --- |
| `isim.buildings` | ✅(北苑公寓列表) | ✅(floors/rooms 解析器) |
| `isim.floors` / `rooms` | — | ✅ |
| `isim.electricity` | — | ✅ |
| `competition.info` | ✅(7 项获奖,学分汇总) | ✅ |
| `repair.orders` | ✅(历史订单解析) | ✅ |
| `repair.order_detail` / `form_data` | — | ✅(detail/form) |
| `repair.upload_image` / `submit` | **不做实网验证(写操作)** | ✅(submit 结果分类) |

## Manifest

| 操作 | live | fixture |
| --- | --- | --- |
| `manifest.v2` | ✅(schema 2, revision dfe412c3) | ✅ |
| `announcements.list` | — | ✅(过滤器) |

## 维护控制

| 操作 | live | fixture |
| --- | --- | --- |
| `session.clear_cookies` | ✅(清空后自动恢复验证) | ✅ |
| `session.expire_service_ticket` | ✅(ykt.balance 随后正常) | ✅ |
| `diagnostics.export_report` | — | ✅ |

## 未覆盖 / 说明

- 写操作(评教提交、电费缴纳、活动报名、扫码签到、报修提交、图片上传)按计划只做离线 fixture 验证,不做实网验证。
- `jwc.all_term_scores`、`jwc.score_detail`、`jwc.course_schedule*`、`jwc.class_*`、`ykt.utility_page_info`、`isim.floors/rooms/electricity`、`repair.order_detail/form_data`、`labor.sign_list`、`announcements.list` 为 compile-only 或 fixture 级,待需要时补实网抽查。
- Rust 的 debug-tools 故障注入(`faults.*`、`debug.request`)未迁移(计划标记为低优先,build tag `debugtools`)。
