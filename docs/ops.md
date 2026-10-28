# LoveACE 操作参考(ops catalog)

> 由 `core/ops` 注册表生成(`./bin/loveace operations` 的 JSON 数据),共 73 项。
> 最后生成:2026-10-24。目录改动后请重新生成本节。

## 调用方式

所有非认证操作经一个端点执行(Huma 文档见 `/docs`):

```
POST /v1/me/ops
Authorization: Bearer <access_token>
Content-Type: application/json
Idempotency-Key: <唯一键>        # 仅写操作必需

{"op": "jwc.term_scores", "args": {"term_id": "2024-2025-1"}}
```

规则:

- **读操作**直接执行;**写操作**(`写` 列为 ✔)必须带 `Idempotency-Key` 头——
  同键重试**重放已存结果,绝不二次执行**(对缴费/报名/签到这类操作是安全底线)。
- `auth.*` 不可经此端点调用:服务端独占登录生命周期(会话组/验证码三循环),
  登录走 `POST /v1/auth/login`,验证码挑战走 `/v1/auth/challenges/{id}/image`
  与 `/v1/auth/challenges/{id}/solution`。
- 错误一律 RFC 9457 problem+json(`school.*` 代码);学校侧失败会如实透传,
  网关/学校不可用为 `school.unavailable`(503,可重试)。
- 读请求遇会话过期会自动恢复并重放一次;**写请求不重放**,网络歧义返回
  `outcome_unknown`(409)——以学校侧实际结果对账,不要盲目重试。

## 目录

- [auth](#ops-auth)(8)
- [jwc](#ops-jwc)(26)
- [ykt](#ops-ykt)(10)
- [aac](#ops-aac)(4)
- [labor](#ops-labor)(10)
- [isim](#ops-isim)(4)
- [competition](#ops-competition)(1)
- [repair](#ops-repair)(5)
- [manifest](#ops-manifest)(1)
- [announcements](#ops-announcements)(1)
- [session](#ops-session)(2)
- [diagnostics](#ops-diagnostics)(1)

## auth

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `auth.login` | Gateway + identity login | — |  |
| `auth.ec_login` | Gateway (EC) login only (optional CAPTCHA code) | `captcha` 字符串（可选） |  |
| `auth.ec_captcha` | Fetch the EC CAPTCHA requirement and image | — |  |
| `auth.uaap_login` | Identity (UAAP/CAS) login only | — |  |
| `auth.heartbeat` | Gateway heartbeat probe | — |  |
| `auth.status` | Redacted session inspection | — |  |
| `auth.logout` | Sign out and clear session state | — | ✔ |
| `auth.cancel` | Cancel in-flight work | — | ✔ |

## jwc

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `jwc.academic_info` | Academic overview | — |  |
| `jwc.training_plan` | Training-plan summary | — |  |
| `jwc.terms` | Term list | — |  |
| `jwc.term_scores` | Scores for one term | `term_code` 字符串（必填） |  |
| `jwc.all_term_scores` | Scores across terms | `term_codes` 字符串数组（必填） |  |
| `jwc.this_term_scores` | Current-term scores | — |  |
| `jwc.score_detail` | Score detail for a record | `term_id` 字符串（必填）、`course_code` 字符串（必填）、`course_class` 字符串（必填）、`exam_time` 字符串（可选） |  |
| `jwc.exams` | Aggregated exam schedule | — |  |
| `jwc.seat_info` | Exam seat information | — |  |
| `jwc.other_exams` | Other exam records | `term_code` 字符串（必填） |  |
| `jwc.plan_completion` | Plan completion or selection | `plan_id` 字符串（可选） |  |
| `jwc.plan_options` | Selectable plans | — |  |
| `jwc.student_schedule` | Student schedule for a term | `term_code` 字符串（必填） |  |
| `jwc.course_schedule_terms` | Course-query terms | — |  |
| `jwc.course_schedule` | Course offering by course code | `course_code` 字符串（必填）、`term_code` 字符串（必填）、`page_num` 整数（可选）、`page_size` 整数（可选） |  |
| `jwc.course_schedule_all` | All offerings for a course | `course_code` 字符串（必填）、`term_code` 字符串（必填） |  |
| `jwc.course_schedule_term` | All offerings for a term | `term_code` 字符串（必填） |  |
| `jwc.class_terms` | Class-curriculum terms | — |  |
| `jwc.class_departments` | Class-curriculum departments | — |  |
| `jwc.class_subjects` | Subjects for a department | `department_code` 字符串（必填） |  |
| `jwc.class_list` | Classes for a department/subject | `plan_code` 字符串（必填）、`department_code` 字符串（必填）、`subject_code` 字符串（可选） |  |
| `jwc.class_curriculum` | Class curriculum | `plan_code` 字符串（必填）、`class_code` 字符串（必填） |  |
| `jwc.teacher_eval_courses` | Teacher-evaluation courses | — |  |
| `jwc.teacher_eval_prepare` | Prepare an evaluation form | `course` 对象（必填）、`pending_count` 整数（必填）、`index_token` 字符串（必填）、`seed` 整数（可选） | ✔ |
| `jwc.teacher_eval_submit` | Submit an evaluation form | `course` 对象（必填）、`questionnaire_title` 字符串（必填）、`form_data` 对象（必填） | ✔ |
| `jwc.teacher_eval_verify` | Verify a course is evaluated | `course` 对象（必填） |  |

## ykt

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `ykt.init_session` | Establish campus-card session | — |  |
| `ykt.balance` | Campus-card balance | — |  |
| `ykt.transactions` | Transactions in a date range | `start_date` 字符串（必填）、`end_date` 字符串（必填） |  |
| `ykt.recent_transactions` | Transactions over 7 days | — |  |
| `ykt.monthly_transactions` | Transactions over 30 days | — |  |
| `ykt.utility_page_info` | Recharge page info | — |  |
| `ykt.utility_options` | Recharge options | `dorm_id` 字符串（必填）、`building_id` 字符串（必填）、`floor_id` 字符串（必填）、`dorm_name` 字符串（必填） |  |
| `ykt.utility_pay` | Electricity recharge | `room_id` 字符串（必填）、`dorm_id` 字符串（必填）、`dorm_name` 字符串（必填）、`build_name` 字符串（必填）、`floor_name` 字符串（必填）、`room_name` 字符串（必填）、`acc_id` 字符串（必填）、`balances` 字符串（必填）、`money` 整数（必填）、`pay_type` 字符串（可选）、`choose_pay_type` 字符串（可选） | ✔ |
| `ykt.purchase_history` | Electricity purchase history | `start_date` 字符串（必填）、`end_date` 字符串（必填） |  |
| `ykt.recent_purchase_history` | Purchase history over 7 days | — |  |

## aac

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `aac.ticket` | AAC ticket readiness | `force_refresh` 布尔（可选） |  |
| `aac.credit_info` | AAC total credit | — |  |
| `aac.credit_list` | AAC credit detail | — |  |
| `aac.reset_ticket` | Reset cached AAC ticket | — | ✔ |

## labor

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `labor.ticket` | Labor ticket readiness | `force_refresh` 布尔（可选） |  |
| `labor.progress` | Labor-course progress | — |  |
| `labor.joined_activities` | Joined activities | — |  |
| `labor.joined_clubs` | Joined clubs | — |  |
| `labor.club_activities` | Activities for a club | `club_id` 字符串（必填）、`page_index` 整数（可选）、`page_size` 整数（可选） |  |
| `labor.apply_activity` | Apply to an activity | `activity_id` 字符串（必填）、`reason` 字符串（可选） | ✔ |
| `labor.scan_sign_in` | QR sign-in | `content` 字符串（必填）、`location` 字符串（必填） | ✔ |
| `labor.sign_list` | Sign-in list | `activity_id` 字符串（必填）、`type` 整数（可选）、`page_index` 整数（可选）、`page_size` 整数（可选） |  |
| `labor.activity_detail` | Activity detail | `activity_id` 字符串（必填） |  |
| `labor.reset_ticket` | Reset cached labor ticket | — | ✔ |

## isim

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `isim.buildings` | Dormitory buildings | — |  |
| `isim.floors` | Floors for a building | `building_code` 字符串（必填） |  |
| `isim.rooms` | Rooms for a floor | `floor_code` 字符串（必填） |  |
| `isim.electricity` | Electricity balance and records for a room | `room_code` 字符串（必填）、`display_text` 字符串（可选） |  |

## competition

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `competition.info` | Competition awards and credits | — |  |

## repair

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `repair.orders` | Repair orders | — |  |
| `repair.order_detail` | Repair order detail | `task_id` 字符串（必填） |  |
| `repair.form_data` | Repair area/project data | — |  |
| `repair.upload_image` | Upload a repair image | `data` 字符串（必填） | ✔ |
| `repair.submit` | Submit a repair order | `area_id` 字符串（必填）、`area_name` 字符串（必填）、`project_id` 字符串（必填）、`project_name` 字符串（必填）、`phone` 字符串（必填）、`address` 字符串（必填）、`description` 字符串（必填）、`pic_urls` 字符串（可选） | ✔ |

## manifest

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `manifest.v2` | Fetch Manifest v2 | — |  |

## announcements

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `announcements.list` | Visible announcements | `platform` 字符串（可选） |  |

## session

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `session.clear_cookies` | Clear cookies by scope (preserves credentials) | `scope` 字符串（必填） |  |
| `session.expire_service_ticket` | Expire service-local tickets (preserves gateway login) | — |  |

## diagnostics

| 操作 | 说明 | 参数 | 写 |
|---|---|---|---|
| `diagnostics.export_report` | Assemble a sanitized local diagnostic report | — |  |

