# loveace-go — 工作说明与现状(AGENTS.md)

最后更新:2026-10-26。本文件用于会话压缩后恢复上下文,**如实记录已知缺陷**,
不含任何凭据与服务器地址——**私有环境信息(服务器、SSH、密码类)全部在
`AGENTS.local.md`(已 gitignore,可含密钥),会话恢复时优先读它**。

## 1. 这是什么

LoveACE 的 Go 版核心库,从 Rust 的 `../LoveACE-core`(只读,不要改)迁移而来,
目标是承载至多 2000 人同时在线的后端。**当前状态:M0~M6 + M5 全部完成**:

- 核心库 `core/`(认证、会话、73 项学校操作、操作注册表、会话池、
  **验证码 ONNX 推理 `core/captcha`**)
- CLI `cmd/loveace`(call / shell / operations / self-test / bench)
- 后端 `server/` 全功能:auth(Ed25519+刷新轮换+设备)、academic 读接口 +
  读穿缓存、**通用操作面 `POST /v1/me/ops`(写操作强制幂等键)**、公告、
  校历(工作流)、课表×校历合并视图(含考试叠加)、分层健康 + L0 零凭据
  探测 + 分钟桶、用户冷却、账号冷静期 + IP 限流、**直连会话模型 +
  验证码三循环**(M4:代理池/借位验证已删;自动解环+熔断+人工挑战;
  判卷结果 zstd 日切沉淀)、admin 端点。
- 部署:`deploy/`(Containerfile、compose、Caddy 站点);维护手册
  `docs/maintenance.md`。**已部署上线(2026-10-15),`https://goace.linota.cn`**。

**2026-10-14 重大决策:放弃 CF Worker 代理出口方案**(E1 实测 CF 出口为全体
Worker 共享的单 IPv6,无法按出口轮换)。反封禁改为 **EC 会话池**(依据 §5
实测:验证码与失败额度只在 EC 网关登录层;一个 EC 会话可派生多账号 UAAP)。
设计记录在 `server/DESIGN.md` §7。相关代码已删除,两个线上 Worker 已拆除。

**2026-10-17 重大决策:EC 会话池整体退场,改为 ProxyPool 1:1 模型**(E-A
实测 TWFID 绑死出生出口 IP,共享槽模型在多出口世界不成立,见 §5)。
设计记录在 `server/DESIGN.md` §7。一刀切不留双路径(用户确认,尚未大规模
宣传)。新增 `server/internal/proxypool`(出口租约/轮换/冷却账本)与
压测工具(真库用户,未随开源仓库发布)。

**2026-10-26 自动评教(后台任务池,DESIGN §15)**:引擎 `server/internal/teachereval` 与 `/v1/me/teacher-eval/*`、`/v1/admin/teacher-eval/*` 已实现,逻辑通过假学校测试;功能默认关闭(`LOVEACE_TEACHER_EVAL_ENABLED=1` 才启用),教评开放前未对真实学校提交。客户端既有的评教 ops(`jwc.teacher_eval_*`)保持不变。

**2026-10-21 重大决策(M4):ProxyPool 与借位验证整体退场,全部会话直连 +
验证码三循环**(E-D 实测:直连 100 并发 burst 99/100 ok、p50 4.3s 比池快
一倍;错 UAAP 密码死在 CAS 层不烧 EC 额度,借位验证失去意义)。设计重写
在 `server/DESIGN.md` §7。`server/internal/proxypool/` 已删除;借位验证
(`validateUAAP`/宿主预算/validation stamp/derive 闸)与全局 EC 闸保留、
端口账本删除;`/v1/admin/pool` 改为 `/v1/admin/sessions`(会话组 +
CaptchaStats)。验证码原生化三件套同期落地:`core/captcha`(Go 原生
ONNX 推理)、`school` 三循环+熔断、`server/internal/captchastore`
(zstd 日切沉淀)。**2026-10-21 已部署上线(glibc 版 onnxruntime .so 需要
debian 系镜像);2026-10-23 代理供应商订阅已注销,服务器直连出口成为
唯一出口(不再有付费回退),`.env` 里的代理残留行已清。**

Go 工具链不在 PATH,每条命令前加:`export PATH=$HOME/sdk/go/bin:$PATH`(go1.27.1,GOPROXY=goproxy.cn)。

## 2. 目录结构

```
loveace-go/
├── core/                  库代码(import 前缀 github.com/Sibuxiangx/loveace-go/core/<包>)
│   ├── config cookies crypto detector diagnostics errs events retry timeutil transport session
│   ├── services/          aac common competition isim jwc labor manifest repair ykt
│   ├── ops/               操作目录(73 项)+ JSON 分发,CLI 与服务器共用
│   ├── sessionpool/       分片 + TTL + 单飞的会话池
│   ├── internal/testutil/ 登录 fixture(MockTransport + 动态 RSA;core 内部,server 不可导入)
│   └── loveace.go         门面:Client(共享连接池、每主机并发限制、登录限速)
├── cmd/loveace/           CLI
├── server/                后端
│   ├── cmd/loveaced/      入口:配置、迁移、HTTP 服务、优雅退出
│   ├── internal/
│   │   ├── api/           Huma v2 + chi;auth/me/academic;problem+json;消毒中间件
│   │   ├── cache/         读穿缓存 + 单飞 + stale-if-error
│   │   ├── captchastore/  判卷结果沉淀:金标/负样本 zstd 日切 + 保留期清理
│   │   ├── teachereval/   自动评教后台任务池(DESIGN §15)
│   │   ├── config/        环境变量配置(缺钥匙自动进 dev 模式)
│   │   ├── dto/           自有数据格式 + 唯一映射点(mapper.go)
│   │   ├── gate/          §5.1 密码格式预校验(纯函数)
│   │   ├── school/        学校会话管理器(直连登录/验证码三循环/挑战/恢复)
│   │   ├── store/         接口 + mem 实现;pg/(pgx v5 + 内嵌迁移 + 集成测试)
│   │   ├── token/         Ed25519 访问令牌 + 轮换刷新令牌
│   │   └── vault/         AES-256-GCM 信封加密 + HMAC 指纹
│   ├── DESIGN.md          后端设计稿(直连会话模型 + 验证码三循环,§7 有边界清单)
│   └── README.md
├── scripts/prompt-credentials.sh   osascript 弹窗录入凭据,写 .env.test
├── deploy/                部署:Containerfile、compose.yml、Caddy 站点、
│                          .env.example(真实 .env 已 gitignore)
├── docs/STATUS.md         逐操作迁移状态(**部分内容已过时,见 §6**)
├── docs/ops.md            操作参考(73 项,由 core/ops 注册表生成;目录改动后重新生成)
├── docs/maintenance.md    维护手册(部署/密钥/备份/事故处理)
├── AGENTS.local.md        私有环境与凭据(**gitignore**,可含密钥)
└── bin/                   编译产物(gitignore)
```

被 gitignore 的敏感文件(不要提交、不要在输出里回显):`.env.test`。

## 3. 硬性约束(来自 ../LoveACE-core/AGENTS.md 与用户指令)

- 不修改 `../LoveACE-core`、`../LoveACE-work`、其他客户端;不读 `.env`、密钥、钥匙串、用户日志、本地数据库(本项目自己的 `.env.test` 由用户授权使用,但不要打印内容)。
- 门卡/门禁功能全部排除(HTTP、BLE、模型、CLI)。报修和 manifest 在范围内。
- 本目录是独立 git 仓库(`main`,首个提交 `32dac18`);**未经用户明确要求不 commit、不 push**。
  **无远程**(2026-10-15 确认,用户选择暂不推送):全部提交只在本地
  `main`;要推送时先配 remote(gh 已登录,账号见 AGENTS.local.md)。
  **开源准备中**:隐私内容一律进 `AGENTS.local.md`(已 gitignore),
  本文件与 README/docs 保持可公开。
- 不记录密码和 cookie。CLI 的 stdout 每条命令只输出一个 JSON,提示信息走 stderr。
- 不允许全局可变的账号/会话单例;一个 `Session` 拥有一个账号。
- 写操作(评教提交、电费缴费、活动报名、扫码签到、报修提交、图片上传)默认禁止,必须显式 `--allow-write`。
  **报修提交 `repair.submit` 用户明确要求不测。** 其余写操作只在用户逐项授权后才做实网测试。
- 本机跑 `go build` / `go test` 是允许的。

## 4. 关键设计与易错点

- **登录**:EC 网关(RSA PKCS#1v1.5,手写填充,因为 Go 1.24+ 的 `crypto/rsa` 拒绝小于 1024 位的密钥)+ UAAP/CAS(3DES-ECB,密钥取 `lt` 前 8 字节重复 3 次)。JWC 回调是尽力而为,失败不应使登录失败。
- **网关密码**:`.env.test` 里 `LOVEACE_GATEWAY_PASSWORD` 为空,约定默认等于学校密码(CLI 的 `resolveCredentials` 已处理)。
- **Send 语义**:读请求遇到会话过期会恢复并最多重放一次;写请求不重放,返回 `outcome_unknown`;读外层 3 次指数退避。
- **重定向**:最多 8 跳;303 一律 GET;307/308 保持方法;301/302 的 POST 降为 GET;跨源去掉 Cookie/Authorization。
- **非规范 header**(`ticket`、`sdp-app-session`)必须原样发送,transport 里用 `req.Header[name]` 直接写。
- **空 body 的 POST** 必须带 `Content-Length: 0`,否则网关返回 411(已有 httptest 覆盖)。
- **`MockTransport`**:按 URL 子串匹配,最长匹配优先;未匹配返回合成 404(与 Rust 一致)。写测试时常见坑:`LoginTransport` 里已有 `cas/login` 的 GET 路由,AAC/Labor 测试需要用更长的 `cas/login?service=...` 模式覆盖。
- **`session.Inspect()` 的 cookie 视图是脱敏的**(只有名称/域名/过期),不含值。要拿真实值用 `s.Jar().All()`。
- **网关 cookie 与业务 cookie 要分清**:`core/cookies` 按 scope 清理(`TestClearCookiesByScope`)。EC 池的 slot 只含网关 cookie;派生 UAAP 时克隆网关 cookie、清掉业务 cookie,再提交 CAS(见 §5)。
- **Go `flag` 包**遇到第一个位置参数就停止解析:`loveace call` 已改成先摘出操作名再解析 flag。`shell` 子命令从 stdin 读命令,所以凭据只能来自环境变量/`--env-file`,不能用 stdin JSON。
- `loveace call` 每次都是新会话;`labor.*`、`aac.*` 的 ticket 流程需要先 `auth.login`,所以多步测试用 `shell` 模式。

## 5. 会话与出口实测结论(2026-10-17 增订:ProxyPool 1:1 转向)

**2026-10-17 的三条新事实(E-A/E-B/E-C 前置),直接导致 EC 会话池退场:**

- **TWFID 绑死出生出口 IP(E-A 实测,ProxyPool 1:1 的核心依据)**:在代理
  IP 上出生的完整会话(EC+UAAP 通过),克隆全部 cookie 到直连会话后业务
  调用 500;同一代理会话原地业务调用 200。**粘性是强制的**。此前"学校
  会话不绑出口 IP"的实测只适用于 UAAP 层,不适用于经网关代理的业务会话。
  后果:多出口世界共享槽模型不成立,每用户独立 EC 会话、出生即走独立
  代理端口、终身绑定;恢复=换端口重登录(旧 TWFID 作废)。
- **代理 trial 100 端口=100 独立出口 IP(E-B 实测)**:端口=出口选择器,
  完整登录经代理成功(EC 3.3-5.7s + UAAP 3-17s)。满载压测(49 库内用户
  同时登录):47 成功,p50 8.9s/p95 9.8s,0 验证码、0 冷却——反封禁模型
  成立。首次登录成本 ~9s(代理往返),老用户快速通道 0.49s 不变。
- **借位验证会烧穿宿主 TWFID 的身份登录预算(2026-10-17 实测,假判根因)**:
  一个宿主被借出 ~15 次验证后,网关对它的 CAS POST 开始报错,错误页被
  UAAP 检测读成"密码错误"——节奏跑假判 45 用户、爆发跑只有 2(爆发时
  大家走全量路径,没人借)。已修:每宿主验证预算(默认 3,借满退出借池)
  + invalid 判定须两个不同宿主一致(无第二宿主=不采信,走全量 EC 路径
  给真判)+ EC 路径的 invalid 也触发 hook。教训:**E7 的身份登录预算不只
  属主自己花,借出去的每一次都在花**。

**2026-10-17 晚轮四条新事实(性能复盘,依据 3 天生产日志 5935 行):**

- **aac/labor 服务票长期固定(用户反馈,已利用)**:拿到 TWFID + 票即可
  直接请求业务数据;票实质永久(用户去年上报漏洞后学校才手动换过一次)。
  已把票随会话快照封存并在恢复/全量登录时回灌(`session.Tickets()` /
  `SeedTickets()`、`UserSnapshot.Tickets`),铸票 4 跳降为"每用户一次、
  跨重启存活"。**不要重新引入"每会话铸一次票"的行为。**
- **出口端口 RTT 方差 2-6×(实测)**:100 端口逐个计时到网关登页:快档
  ~1.19-1.45s(约 45 个)/ 中档 ~2.1-2.3s(约 30 个)/ 慢档 2.7-8.0s
  (约 25 个)。慢口是登录 p90=23.5s、课表 p90=12.5s 的主源。已加端口
  探测 + 带宽租借(`proxypool.Probe`,启动即测 + 每 15 分钟复测;只从
  "floor+max(2×floor, floor+800ms)"带内按 LRU 租,带外/未测仅兜底)。
- **验证记忆(validation stamp)**:6h 内**证明过正确**的 (账号,密码)
  对跳过借位验证(省 2 跳 ≈2.5s);指纹 = 每进程 HMAC,不存明文;密码
  变更自然失配重验。错密码/改密路径行为不变。
- **冷会话后首批数据走 SWR**:学术读接口的过期条目立即返回(元数据
  `source=cache`)+ 后台单飞刷新;首屏不再等网关往返。

以下为 2026-10-14~16 的实测结论(ecpool 相关条目保留史实,机制已随
ecpool 退场由 §7 的新模型替代):

- **学校会话不绑出口 IP**(实测):直连登录 → 同一份 cookie 换到另一台
  服务器出口 → 再换回直连,三段都成功。**注意:此结论仅适用 UAAP 层;
  经网关代理的业务层绑 IP(E-A),见上。**
- **网关 Location 改写与会话有效性绑定(2026-10-15 实测,生产 502 根因)**:
  会话有效时,vpn2 网关把 CAS 的 service 重定向**改写回 vpn2 命名空间**
  (铸票链 3 跳全是 `*-aufe-edu-cn.vpn2.aufe.edu.cn:8118`);网关会话失效
  后仍代理请求但**不再改写**——Location 泄出原始内网域
  (`api.dekt.ac.acxk.net` 等),跟随必然 i/o timeout。读路径有恢复机制
  (过期→重认证→重放),而 `RedirectLocations`(铸票链)此前是裸发送,
  导致 jwc 永远正常而 aac/labor 铸票 502/503 的分裂现象。已修:walk
  检测到 Location 逃出 vpn2 命名空间(或网关认证页)→ 按读语义单飞
  恢复一次并重试;fixture 同步改为改写后的 Location 形态。
  **ticket 仍是乐观设计**(用户 2026-10-15 确认):缓存优先不主动刷新,
  票据本身过期由前端调 `aac/labor.reset_ticket` 手动自愈
  (docs/frontend.md 有流程)。
- **池槽活性只有"改写自测"能证明(2026-10-16 实测,生产 502/503 风暴根因)**:
  网关对老化 TWFID **照样代理、身份登录照样"成功"、心跳一切正常**,
  只有 service 重定向的改写会停——死亡从池子内部原理上不可观测。
  (ecpool 机制已退场;会话组恢复的自测沿用该结论,见 `server/DESIGN.md` §7。)
- **sdp-app-session 只认域级 TWFID(2026-10-16 实测,派生会话 500 根因)**:
  网关每卡发**两颗** TWFID cookie(host-only + `.vpn2.aufe.edu.cn` 域级);
  `sdp-app-session` 头只认域级值。已修:`TWFID()` 回退优先域级 cookie,
  jar 的域级 cookie 是实时真相优先,内存 pin 只作兜底(2026-10-17 反转:
  网关在身份/铸票中途轮换 TWFID,内存 pin 反噬)。
  **教训:派生/克隆会话与全量会话的差异必须横向对比完整请求头。**
- **UAAP 层没有封禁,EC 网关才有(用户 2026-10-16 明确,反封禁模型的核心)**:
  错密码只死在 UAAP 层,绝不烧 EC 的失败额度(2026-10-17 起:借位验证
  前置于一切网关交互)。会话组收敛为**每用户一条 UAAP 会话,多设备共享**。
  (ecpool 反哺机制已退场;ProxyPool 下无需反哺。)
- **快照恢复必须带回真实凭据(2026-10-16 实测,409 风暴根因)**:
  `restoreSnapshot` 曾用 `session.New("", "", "")` 重建会话——cookie 有效
  期内一切正常,学校侧过期后恢复流程拿**空密码**重登,永远失败,读请求
  全部 409 `outcome_unknown`(核心 recover 曾把恢复失败的真实原因吞成
  它)。已修:Manager 增 `WithCredentialResolver`(loveaced 接 vault 解密),
  快照恢复即带回凭据;resolver 失效降级为不恢复(走保险库重登路径)。
  恢复撞网关验证码 = **会话失效,要求用户重新登录**(2026-10-27 起,由可重试临时态改):
  恢复路径没有 challenge id,读请求无法回答验证码,而重试不会自愈;返回
  `auth.school_login_required`(401),前端清会话跳登录页,登录页自带验证码流程。
  自动解环(模型 `LOVEACED_CAPTCHA_MODEL`)在恢复路径里仍先尝试,只有解不开才落到这里。
  密码错误照实透传 `invalid_credentials`。教训:**任何"无凭据可重建"的会话都是定时炸弹**。
- **单 EC 会话可派生多账号 UAAP(2026-10-14 实测)**:EC 登录成功后只有 2 个
  网关 cookie(TWFID 等),克隆到新会话后可用另一账号完成 UAAP/CAS 登录;
  从"已完整登录"(EC+UAAP+教务回调)的会话克隆时,清掉继承的身份与业务
  cookie、只留网关 cookie,再派生也成功。两账号的教务数据(GPA 3.98 /
  4.05)互不串号,证明真的切换了身份。
  **未验证**:共享网关会话的长时(≥24h)并存;单账号第 6 个 UAAP 会话的行为
  (server/DESIGN.md §12 E7/E9)。
- **CF Worker 出口不可用**(E1 实测,2026-10-12):100 次调用全部从同一个
  共享 IPv6(`2a06:98c0::/32`)出去,97% 落在西雅图机房;placement 提示
  (smart/APAC)无效。经 Worker 每请求约多 350ms、登录多 1.3~3.3s。
- 网关区分"密码格式错误"与"密码错误"(调试期间见过 `20026 bad password
  format`)——支撑 server/DESIGN.md §5.1 的本地预校验。
- 验证码污染约 3 分钟自动恢复(E2 中间数据,两次观测一致)。
- **验证码判卷 oracle(2026-10-20 实测,captcha-ocr-test/ecprobe 一次性工具)**:
  EC 登录接口(`login_psw.csp`)**先验码、后验凭据**,网关本身就是验证码判卷器。
  验证码模式下用假账号提交:`20023 characters are incorrect` = 码错;
  `20004 Invalid username or password` = **码已通过**(随后凭据被拒)。
  注意:`RndImg=0`(未污染)时**不校验码**——任意码都直接走凭据检查返回
  20004,判卷只在验证码模式下有效。实验全程不碰真实账号。
- **挑战一次性与重试环(后端适配要点;一次性已实测 2026-10-20:同一 TWFID
  第 2 次提交 24/24 返回 auth 页 XML——挑战上下文在首次提交后立即作废,
  "一张图多猜几次"不可行)**:判错(`ECCaptchaInvalid`)后必须重新 `EcCaptcha`
  (新 auth 页 + 新图)再提交,取 auth 页/图都是零额度 GET。登录撞验证码的
  正确姿势:**猜 → 提交 → 20023 就立刻取新挑战重猜重提**。core 已能区分
  两种失败(`ECCaptchaInvalid` vs `ECInvalidCredentials`),适配只差重试环
  策略;每次重试烧 1 次所在端口失败额度(污染自续,停手后 ~3 分钟自愈)。
- **批量囤积挑战会被网关整批失效(2026-10-20 run3 实测,根因待判定)**:
  同一 IP 并发取 250 张 auth 页/图囤着再集中提交:首批约一半提交被回了
  一份 auth 页(会话作废),后续批 100% 作废。疑似网关对同 IP 持有的未认证
  会话有逐出/上限。对策 = 边取边判流水线(挑战从取图到提交 <1s,任意时刻
  只持有 W 个上下文)——尚未实测(验证码数据采集已按用户决定暂停)。
  **教训:持有中的 TWFID 不是安全资产。**
- **ddddocr 实测(2026-10-20,oracle 判卷)**:beta(common.onnx)对本校验证码
  单猜准确率 **33.7%**(98 可判对 33);"输出满 4 字符"≠识别正确。实测猜测
  字符集 = 31 字符防混淆集 `23456789abcdefghjklmnpqrstuvwxyz`(无 0/1/i/l/o)。
  模型文件 54MB、CPU ~9ms/张。测试产物已按用户要求清理(仅存
  `~/captcha-ocr-test/common.onnx`);Go 原生推理方案 = onnxruntime_go +
  复刻 PIL LANCZOS 预处理(先缩放后灰度、(x/255-0.5)/0.5、输入 [1,1,64,W])+
  charset 导出,验收标准为与 Python 输出 100% 一致——待拍板。
- **武装前的挑战依然有效(2026-10-20 实测)**:`RndImg=0` 时取的验证码图,
  在 IP 进入验证码模式后提交照样被校验——挑战绑定 TWFID,不绑定取图时的
  IP 状态。武装阈值:同 IP 第 2 次失败登录后,下一次提交即被验码(与 §1
  "2 次失败"一致);此后每次提交(码对错都算)都是一次失败登录,持续刷新
  污染窗口。

**2026-10-21 E-D 直连实测四条新事实(验证码原生化 M1-M3 之后,服务器直连出口):**

- **错密码只死在 CAS 层,不烧 EC 额度**:只损坏 UAAP 密码时 EC 登录全部
  成功(网关密码未坏),CAS 对错密码回 28645B 静默登录表单,文案=「密码输入
  错误，您还有"N"次输入机会。」(账号级约 5 次计数,不含 errorMsg)——已补
  core marker `密码输入错误`。**"一批新人同时输错→全 IP 验证码"在现有架构
  下不成立**:全 IP 验证码只在 EC 网关密码错/vault 漂移时触发。
- **EC 验证码洪水语义(30 个错网关密码实测)**:洪水期间网关对同 IP 的验证码
  提交一律拒绝(Rounds:139/Rejected:139/Accepted:0,与 run3 囤积失效同族
  ——挑战存储被并发逐出),L2 环空转到上限,L3 熔断正确开闸回退人工;洪水
  停止后立即 **29/29 一次过闸**(Rounds:30/Accepted:29)。结论:洪水下不要
  硬解,熔断+等自愈,恢复后环自动带人过闸;60/min 全局闸下 30 用户恢复排队
  p50≈50s,可接受。
- **直连容量基线**:100 并发 burst 全量登录 99/100 ok,p50 4.3s / p95 4.5s,
  0 验证码 0 冷却——比代理池(E-B p50 8.9s)快一倍,M4 摘池直连化的依据。
- **工程坑**:glibc 版 onnxruntime .so 在 alpine/musl 运行时加载不了(缺
  ld-linux/libstdc++),生产镜像需 debian 系;macOS tar 会把 `._*`
  AppleDouble 打进 go:embed(`._0001_init.sql` → PG 08P01),传输用
  git archive 或 `--exclude='._*'`。
- 两个 CF Worker(`loveace-proxy`/`loveace-iptest`)已于 2026-10-14 拆除;
  zone 里的残留 DNS 记录需在 CF dashboard 手动删(见 AGENTS.local.md)。
- 历史教训(代理链路调试,记录备查):上游 URL 必须 QueryEscape(否则
  `&type=cs` 被截断,网关返回 20026);方法必须单独传(否则 GET 被当 POST,
  返回 405)。

## 6. 已知缺陷(诚实清单)

**已于 2026-10-14 修复**(原缺陷 1~3):
- goroutine 泄漏:根因是 `mergedContext` 是自定义 Context,子 context 退化成
  `propagateCancel` 看门狗 goroutine(实测每操作 2 个)。现在 `newMergedContext`
  直接用真 `cancelCtx` + 次父 AfterFunc,`operationContext` 返回 `(ctx, release)`
  并在全部调用点 defer;bench 1000 会话 goroutines 1 → 1。
- `CancelOperation`:现在保存并取消当前 opCtx(真取消在途操作),session 仍可用;
  `go vet ./...` 已干净。

**仍未修复**:

1. ~~验证码流程的 fixture 测试不完整~~ **2026-10-15 已补**:4 个 Rust fixture
   已移植(`core/session/captcha_fixture_test.go`)。**重要 parity 事实**:
   **2026-10-26 有意偏离 Rust**:20041 现为 `ECMaybeAttacked`(节流,不判卷、
   不是验证码,见 server/DESIGN.md §7.8),server 的 `errs.MaybeAttacked` → 429 已可达。
2. ~~并发恢复测试偏弱~~ **2026-10-15 已补**(`recovery_semantics_test.go`)。
3. `docs/STATUS.md` 的部分表述偏乐观。`score_detail` 已澄清:端点只支持
   当学期成绩,当学期未出分时 `items:null` 是正确行为(2026-10-15 实测)。
4. 其他与 Rust 的差距:`faults.*` / `debug.request`(debug-tools 故障注入)未迁移;`diagnostics` 只迁了 Redactor 和 Report,没有接入 session 的事件流。**`analytics` 遥测上报客户端不迁移(用户 2026-10-15 明确):Go 代码里没有任何 CF 上报路径。**
5. 代码风格债:`transport/internal_helpers.go`、各处手写的 `itoa`/`parseI64` 辅助函数大量重复,可以统一用 `strconv`。
6. server 的 huma 校验会回显请求体到验证错误 —— 已加 `/v1/auth` 响应消毒中间件兜底,但根治应迁移到自定义 ErrorModel(带 code 字段)并关掉 Value 回显;目前依赖消毒层,是安全关键路径,改动前先加防回显回归测试。
7. ~~ProxyPool 新缺陷(2026-10-17 实测,待修)~~ **2026-10-21 M4 整体消解**:
   借位验证与代理池已删除(假判/宿主预算烧穿/出口 IP 质量三类问题随
   之消失)。**2026-10-26 残留收口**:20026 单独映射为 `request_rejected`
   (计入 gateway 失败样本,落 temporarily_unavailable);验证码计数进
   `/v1/admin/health` 的 `captcha` 字段。单出口直连后,验证码污染影响全体
   登录(由三循环承受,~3min 自愈)。
8. ~~M1 尚未做~~ **已全部完成**(M2 收尾):账号冷静期阶梯、剩余读接口、
   设备/请求日志脱敏复查、跨设备会话组均已在 M2 落地。

## 7. 验证状态(2026-10-21,M4 直连化 + 验证码原生化后)

- `go build ./...` 通过;`gofmt -l .` 为空;**`go vet ./...` 干净**。
- `go test -count=1 -race ./...`:**32 个包全部 ok**(`core/captcha` 需
  拆出单跑——TSAN 与 onnxruntime 在进程退出时互掐,测试本身通过),含:
  - **验证码原生化(2026-10-20~21)**:`core/captcha` 单测(受限解码/
    ddddocr 塌缩语义/合成 logits)+ live/oracle 门控测试(oracle 实测
    接受率 34.8% ≥ Python 基线 33.7%,M1 验收);`school` 三循环混沌测试
    6 项(自动通过/判错重试/解码无效零提交/熔断开闸-恢复/人工金标/
    旧风暴回归);`captchastore` 5 测试(日切/分片/懒打开/-race)。
  - **M4 直连化(2026-10-21)**:借位验证与代理池整体删除——删除
    `TestWrongPasswordValidatedOnBorrowZeroGateway`/
    `TestWornOutHostVerdictIsWithheld`/`TestValidationBorrowsCapped`/
    stamp 与 relogin 跳过探针测试、`proxypool/pool_test.go`;改密检测
    fatal 测试并入直连路径(hook 不触发断言保留);admin 端点测试改为
    `/v1/admin/sessions`(会话组 + captcha)。
  - **性能轮(2026-10-17 晚)**:票回灌到全量登录与快照恢复、且**不触发
    铸票链**(`speed_test.go`,借位相关两测已随 M4 删除)、
    `cache/cache_test.go`(SWR 立返+后台刷新、刷新失败保旧值)。
  - **M6 加固(2026-10-15)**:全局 EC 登录闸(`school/gate.go`,默认
    60 次/分钟+750ms 抖动)。**混沌测试**(`school/gate_test.go`):闸门
    串行化、验证码风暴、学校宕机(temporarily_unavailable 可重试)、
    网关宕机、改密检测 fatal(`rewrite_selftest_test.go`)。
    **压测**(`api/bench_test.go`):**真库用户压测**——store 播 2000 个
    vault 凭据行,读回即用,64 并发登录全 200,180ms,**每账号恰好 1 次
    EC 凭据提交**,无 goroutine 泄漏。
  - **加固测试抓出的真缺陷(已修)**:① `admit()` 无锁并发读外层
    group map(register 写入 vs 不同用户并发登录)= 数据竞争,持锁 +
    网络登出移出锁外;② 池统计无锁数据竞争。
  - 课表×校历合并视图 E2E(假期/调休/**考试叠加**)、
    `jwc.WeekMatches` 单测、`TestCalendarMaterialisation` 修正;
  - goroutine 泄漏回归测试、`CancelOperation` 真取消测试;
  - server 单测:gate 全角/空白/零宽分类、vault 往返与错误用户 AAD 拒绝、
    token 过期/篡改拒绝与刷新唯一性、API 全流程(登录/掩码/设备/422 不触网/
    401 缓存拒付/刷新轮换与复用检测/academic 缓存 live→cache/匿名 401)。
- `server/internal/store/pg` 的集成测试 **已在真实 Postgres 上跑通**
  (`LOVEACE_TEST_PG` 门控;迁移 0001~0005 复跑通过)。**注意:pool_slots
  表(迁移 0002)已随 ecpool 退场而闲置,接口方法已删,表保留未 drop。**
- `./bin/loveace self-test` 通过;`./bin/loveaced` 可启动,
  `GET /healthz` 返回 200,SIGTERM 优雅退出。
- `bench`(2026-10-14 修复后):1000 会话 goroutines **1 → 1**,吞吐 ≈ 38k
  ops/s、p99 ≈ 22ms、堆增量 ≈ 10MB(内存 mock,不代表真实网络)。

### 实网(2026-10-17,ProxyPool 1:1 真库压测,用户授权)

**真库用户压测(工具未随开源仓库发布)**:49 库内用户(vault
解密)同时登录(并发=49,闸不限速):
- **47 ok**,p50 8.9s / p95 9.8s / max 11.7s——每人一条独立会话,49 个
  独立出口端口,0 验证码、0 冷却。**反封禁模型成立**(每 IP 失败额度
  只在 EC 登录层,轮换摊薄)。
- 2 个 invalid_credentials:最早到达、无宿主可借、走全量 EC 路径、密码
  在 EC 层被拒——各烧 1 次所在端口的失败额度(2/2,未触发冷却)。
- **对比**(60/min 节奏跑:3 ok / 45 invalid):假判根因 = 借位验证把
  少数宿主 TWFID 的身份登录预算(E7)烧穿 → 网关对磨损 TWFID 的 CAS
  POST 报错 → 错误页被读成"密码错误"。已修:每宿主验证预算(默认 3,
  借满退出借池)+ invalid 判定须两个不同宿主一致(无第二宿主=不采信,
  走全量 EC 路径给真判)+ EC 路径的 invalid 也触发 hook。
  **教训:借出去的每一次身份登录都在花宿主的 E7 预算;单宿主意见不可
  定罪。**

### 实网(2026-10-21, E-D 直连容量 + 错密码风暴, 用户授权)

压测工具新增 `-bad N`(错密码风暴,内存内损坏 UAAP+网关双密码,
不写 vault)、`-model`(启用自动解环)、`-tap`(cas/login_psw 裸响应落盘
tapdump/,只存响应不存请求)。服务器直连出口(无 LOVEACED_EC_PROXY_*):

- **直连基线**:100 并发 burst 全量登录 **99/100 ok**,p50 4.3s / p95 4.5s /
  max 4.5s,0 验证码 0 冷却;1 invalid_credentials(真实 vault 密码在 EC 层
  被拒,属数据陈旧)。
- **UAAP 错密码风暴**(只坏学校密码):EC 全成功,CAS 回静默表单落
  unknown_error(已修 core marker);正确密码对照此刻全通,证明静默表单是
  错密码专属响应。
- **EC 层风暴**(双密码损坏,30 用户):首两发 20004 干净判卷,第 3 发起
  IP 武进验证码模式;洪水期 139/139 提交被网关拒绝、L3 熔断开闸;洪水停止
  后恢复跑 **29/29 一次过闸**(p50 50s,闸排队主导)。**三循环语义实测成立**。
- 实验环境:golang:1.27(bookworm)容器内构建+运行(glibc 兼容 ORT),
  产物 压测二进制(未随仓库发布);污染自愈沿用 ~3min 结论,未再复测。

### 生产(2026-10-21 M4 上线,2026-10-23 两日复查,admin 只读)

- 容器稳定运行 2 天;**验证码三循环零触发**(AutoSolved/Rounds/Accepted/
  Rejected/熔断全 0)——正确密码流量不武装 IP,与架构结论一致。
- 业务读接口成功率 100%(aac.credit_info 42 次、aac.ticket 8 次等样本)。
- 沉淀目录为空:还没有有结论的验证码提交,首个真实挑战出现后金标开始
  积累。
- **代理供应商订阅已注销(2026-10-23,用户决定)**:服务器直连出口成为
  唯一出口,不再有付费回退;`.env` 的 LOVEACED_EC_PROXY_* 残留行已清。
  回滚镜像 `localhost/loveaced:pre-m4` 属池时代代码——如需回滚须同时
  恢复代理配置,实际上等于**不可回滚到池模型**(出问题走修复前进)。

### 实网(2026-10-12,真实账号,用户授权;2026-10-15 补测 23 项,见上)

只读且已成功:登录(EC+UAAP)、heartbeat、logout、`jwc.academic_info / training_plan / terms / term_scores / all_term_scores / this_term_scores(空) / exams(空) / plan_completion / student_schedule / course_schedule / course_schedule_terms / class_terms / class_departments / class_subjects / class_list / class_curriculum / teacher_eval_courses(开关已关闭)`、`ykt.balance / recent_transactions / utility_page_info / utility_options / purchase_history`、`aac.credit_info`、`labor.progress / joined_activities / joined_clubs / club_activities / sign_list / activity_detail`、`isim.buildings / electricity`、`competition.info`、`repair.orders`、`manifest.v2`、`announcements.list`、`session.clear_cookies` 后自动恢复、`session.expire_service_ticket`。

培养方案树解析已核对:4 个顶层分类、27 个节点、715 门课(95 通过 / 620 未修读 / 0 不及格),层级与学分统计正确。

经用户授权做过的写操作:电费 1 元(成功,余额 10.68→9.68,缴费记录确认)、图片上传(成功)、扫码签到随机串(返回 `code=1 "二维码不正确"`,被正确解析为业务失败)、申请已满活动(三次均返回 `server error code 1: 该课程人数已达上限,无法加入`,正确映射为 `server_error`,`retryable=false`)。

**2026-10-15 补测(实网,真实账号,用户授权)**:原"27 个未测"中的 **23 个已全部
只读实测通过**:
- 认证子流程:`auth.ec_login`、`auth.ec_captcha`(当前未要求验证码,
  `required=false`)、`auth.uaap_login`、`auth.cancel`(本地控制)。
- JWC:`jwc.score_detail`(**语义澄清,不是 bug**:端点固定走
  `thisTermScores/serchScoreDetail`,只支持**当学期**成绩记录;当学期
  尚未出成绩,`mx:[]`→`items:null` 是预期。参数已用真实成绩行核验:
  `term_id=学期码, kch=课程号, kxh=课头号`,当学期出分后即可用)、
  `seat_info`(空数据但解析通过)、`other_exams`(空)、`plan_options`
  (空 + hint)、`course_schedule_term`(全校 4319 行真实数据)、
  `course_schedule_all`(1 行)。
- YKT:`init_session`、`transactions`(2026-10-01~15,3 条)、
  `monthly_transactions`(6 条)、`recent_purchase_history`(1 条)。
- AAC/Labor:`aac.ticket`、`aac.credit_list`(4 项)、`labor.ticket`;
  `aac.reset_ticket`、`labor.reset_ticket`(`--allow-write`,本地控制)。
- ISIM:`floors`(楼 11 → 6 层)、`rooms`(层 1101 → 35 间)。
- 报修:`repair.order_detail`(完整详情:进度 3 步 + 结算 2 条)、
  `repair.form_data`(2 区域 + 6 项目)。

**仍未实测(4)**:`jwc.teacher_eval_prepare / submit / verify`——评教开关
关闭(`teacher_eval_courses` 返回关闭),且 prepare/submit 是写操作需逐项
授权;`repair.submit` 用户明确不测。

**CLI 易错点(新)**:`LOVEACE_GATEWAY_PASSWORD` 为空时,不带
`--non-interactive` 的 `shell` 会先弹网关密码提示并**吃掉管道 stdin 的首行**
(脚本被当密码消费,之后静默空跑)。管道喂脚本时务必带 `--non-interactive`
(网关密码按约定默认等于学校密码)。

## 8. 下一步建议(按优先级)

1. **已部署上线(2026-10-15;2026-10-17 性能轮;2026-10-21 M4 直连化+验证码
   原生化)**:`https://goace.linota.cn`——compose 栈(db+loveaced)+ 宿主机
   Caddy 终结 TLS。M4 部署实录:git archive 传码(免 `._*` 污染)→
   `podman build`(golang:1.27 CGO + debian:bookworm-slim 运行时,ORT .so
   与模型/沉淀卷挂载 `/root/goace/captcha/`)→ **`podman rm -f` 后再 up**
   (老坑依旧);回滚镜像 `localhost/loveaced:pre-m4`。上线即验:solver
   ready、store 挂载、healthz ok、`/v1/admin/sessions` 正常、新登录路径
   实网 2/2 ok(p50 2.3s)。部署细节与坑(podman-compose 1.x 的
   build:/env_file/回显/**up -d 不检测镜像变化**/restart always)记在
   `docs/maintenance.md` §2/§5。
2. **部署后观察项(代理订阅已注销 2026-10-23,直连单出口为最终形态)**:
   验证码率与熔断开闸(`/v1/admin/sessions`)、污染窗口的自愈节奏、CAS
   账号级错密码计数是否触发冷静期;客户端(小程序,独立项目)按 API 契约
   接 409 `school.captcha_required` → `GET /v1/auth/challenges/{id}/image`
   → `POST .../solution` 的人工流。
3. ~~下一轮性能候选~~ **2026-10-26 已做**:登录关键路径的 JWC 回调改为后台
   单飞(`core/session/service_callback.go`):登录不再等待它;JWC 请求与恢复
   重放前等待同一门;登出 join 未完成回调且不产生可用性观测。冷启动预热
   (登录后后台预取首屏资源)仍未做。
4. **浸泡校准**(DESIGN §12;需用户授权只读实网):直连单出口下 TWFID
   寿命与验证码污染窗口的长时间观察;M4 上线后 30 分钟线上观察
   (尤其冷启动高峰与验证码率)。
5. **打磨**:自定义 ErrorModel 根治 huma 回显(现在靠消毒中间件兜底,
   见缺陷 6);`/v1/auth` 设备/请求日志脱敏复查;`faults.*`/`debug.request`
   (低优先);strconv 风格债;KEK 轮换流程(当前 KEK 不可轮换);
   pool_slots 闲置表清理(新迁移 drop)。
6. **评教三操作**:等评教开关打开 + 用户逐项授权(写操作逻辑已 mock 验证)。

(历史里程碑 M1~M6 的实现细节以 `server/DESIGN.md` 的实现注记与 §7
验证清单为准,不再在此重复。)

## 9. 常用命令

```sh
export PATH=$HOME/sdk/go/bin:$PATH
cd <repo>
go build ./... && go vet ./... && go test -race ./...      # vet 现在是干净的
go build -o bin/loveace ./cmd/loveace
./bin/loveace operations                                   # 73 个操作
./bin/loveace self-test                                    # 离线自检
./bin/loveace bench --sessions 1000 --ops jwc.academic_info
# 实网(需要 .env.test;多步测试用 shell 模式,写操作加 --allow-write)
printf 'auth.login\njwc.academic_info\n' | ./bin/loveace shell --env-file .env.test --non-interactive
# 后端(dev 模式:无 PG/钥匙时自动用内存 store + 临时密钥)
go build -o bin/loveaced ./server/cmd/loveaced && ./bin/loveaced   # :8080, /healthz
# 真库用户压测工具(在容器里跑,env 由 compose 注入;只读不写库)
#   默认 = 最多 100 用户同时登录(库不够就用库里全部),不限速
# 压测工具未随开源仓库发布
# Postgres 集成测试(在测试服务器上起 podman PG 后)
LOVEACE_TEST_PG="postgres://loveace:test@HOST:5432/loveace" go test ./server/internal/store/pg/
```

## 10. 环境备注

**私有信息(服务器地址、SSH、密码类、账号)已全部移至
`AGENTS.local.md`(gitignore,可含密钥)**——本文件面向开源,只保留
通用工程习惯:调试工具(`cmd/iptest` 等)用完即删,`/tmp` 调试残留要清,
凭据由用户提供且不写入任何文件。
