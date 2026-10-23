# Magpie 剩余更新的依赖与优先级 · 2026-10-23

本轮承接前轮 30 条局部适配，以固定快照 [`e00fbdbbc74f9829027a78769cbeb265d6271fb4`](https://github.com/yetone/magpie/commit/e00fbdbbc74f9829027a78769cbeb265d6271fb4) 为范围；源码归档与完整功能基线见 [来源清单](../upstream.json)。不是将原基线之后全部提交整体同步。开始前保留已有未提交变更；结论来自当前鲸桥与两份固定源码快照的调用链对照，来源提交通过上游记录核对。

## 本轮执行顺序

| 优先级 | 独立待办 | 前置依赖与实际闭包 | 本轮安排 |
| --- | --- | --- | --- |
| P1 | GitHub 大备份 | 先以 Git Data blob/tree/commit/ref 和非强制 ref 更新实现原子写；再加入 16MiB 分片、清单 SHA/大小校验、旧片清理。仅做分片会丢失并发保护。保留私有仓库、API 域名令牌约束、64MiB 上限及旧单文件读取。 | 连同两机同步与损坏设置不上传一起验证。 |
| P1 | 搜索密钥管理 | Key 轮换/顺序、散列冷却记录、Retry-After 和错误脱敏先收口；服务 Off/Arrange 与备份顺序复用现有 SearchAPI；Tavily 额度只在显式读取时查询。 | 后端与鲸桥设置页共同落地；不引入上游插件工具点架构。 |
| P1 | 自定义 POST 余额 | Provider 字段→自定义查询方法校验→安全 DTO/保存→页面；沿用原认证、代理、Header、表达式、专用余额令牌及备份。 | 独立小闭包。只支持 GET 与空请求体 POST，内置端点沿旧行为。 |
| P2 | Nous / SiliconFlow 国际站 | 现有预设、区域选择、Bearer、模型列表与美元余额路径可复用，无新凭据字段。Nous 只给 Chat；其 Messages 只服务部分模型，不应给所有模型该端点。 | 预设、页面说明与 HTTP 发现回归。国际站/中国站是独立账号与密钥。 |
| P2 | DeepSeek Search | 依赖现有 SearchAPI 与前项的密钥池；新 POST /v1/tools/search、128 Unicode 字符查询限制、结果解析；保持既有出站脱敏及最终恢复。 | 和搜索管理共同落地，测试使用本地假服务。 |
| P2 | Bedrock Mantle | 预设 17 区域/live list；复用 Bedrock 的认证与字段规则；按模型选择 /v1 或 /openai/v1，明确 route refusal 时至多试一次另一前缀并记忆。 | 小范围修改 provider/preset/test 及 gateway 的独立 Mantle helper/两处调用；不依赖 gwmw。 |
| P2 | Codex 图片字节预算 | 11173 的压缩字节 LRU/4 workers、3eee 的字节预检/512MiB 估计批预算、a930 的长 EXIF 头估算一起移植。先只加并发 worker 会放大解码规模。已有 Codex Responses 内联图调用点可复用。 | 作为完整小闭包推进；低内存夹具验证，保留 API-key/其他账号/协议与 URL 等门控。 |
| P3 已完成 | 媒体 URL/fake-IP | 显式本机 FetchPrivate、KeepOwn、供应商 own-host 精确 host:port、图片/视频/MCP 下载出口、共享 netguard 和代理矩阵共同交付。 | 已独立于插件架构落地并验收；授权与验收边界见 [媒体说明](upstream-media-boundaries-2026-10-23.md)。 |
| P4 有限装配已完成 | gwmw/路由插件注册 | 实际 RunContext 同步 Apply，注册 rotation/middleware/redact/otel；内容 hash 触发排空式热更，等待在途请求及辅助回调、worker/子进程后反向注销。共享权重凭证贯通组、账号和亲和。 | 已完成真实入口与持流验收；JS 策略/onUsage、最小 Ledger observer、失败关闭 Masker 及页面选择共同落地。P1 GUI/schema 与全量 Observer 未移植；详见 [生命周期说明](gateway-plugin-runtime-2026-10-23.md)。 |
| P3 已完成 | Free first 路由 | 直接在现有 weighRouted 接入；补两处保存白名单、实际账号免费判断、全计费维度与档位零价、稳定分组/trace/页面。 | 与插件架构无前置关系；保留未知价不免费、协议/冷却/亲和、子组与备用边界，按 [路由说明](upstream-routing-dependencies-2026-10-23.md) 验收。 |

## 后续媒体闭包的验收条件

来源 [`ea3dee96`](https://github.com/yetone/magpie/commit/ea3dee96a89cfb4710326342e840d58811eaa79a) 与 [`240f3eb4`](https://github.com/yetone/magpie/commit/240f3eb4c5faca86fed15b48ee984ec38c49860c) 应一起理解。鲸桥已存在公共 DNS 答案检查、地址固定、直连/代理保护与逐跳重定向检查；本次新增闭包补齐本机显式授权和供应商输出下载保护。下列要求作为本轮验收清单，实际边界见 [媒体说明](upstream-media-boundaries-2026-10-23.md)，结果见 [审查记录](upstream-review-2026-10-23.md)。

1. `FetchPrivate` 保持本机设置文件专属；普通全局设置保存保留且不能通过提交伪造授权。加入 `KeepOwn`，恢复以本机为准，同步 hash 忽略本机授权。不要声称加密 bundle 一定不含序列化字段。
2. 删除“本机调用者自动允许私网下载”的推断。供应商已配置端点只授予精确 host:port；图片下载失败保留已生成 URL 的原行为，视频失败按原错误路径；MCP URL fallback 同样保护。
3. 命名主机 DNS 剔除配对 Loon AAAA 后必须全部 fake-IP 才进入兼容路径。纯 TUN 固定该次地址；只有 loopback/localhost 代理可按名称请求，远端代理拒绝。字面量、混合答案和未配对 Loon AAAA 不自动豁免，逐跳重判。
4. gateway 的 publicAddr 可等价抽取；图标原 publicIP 与共享规则不完全等价，涉及 0/8、240/4、嵌入 IPv4 的 IPv6 等行为变化，必须单独测试，不能称全部为无行为变化重构。
5. 用假 DNS、本地 HTTP/CONNECT/SOCKS、跨端口/跨主机 redirect、损坏 settings、MCP fallback 与实际私钥入口验证；不需要为此引入当前鲸桥没有的 web_fetch 工具。

## 架构顺序与边界

上游相关层次：[`3ccbe6bc`](https://github.com/yetone/magpie/commit/3ccbe6bc15c96a0cbb47ea574645fa5def3699fa)（pluginrt），[`2dcc5141`](https://github.com/yetone/magpie/commit/2dcc51417e9df99cda2c3e0a172041af743f2f08)（P1/schema），[`9916a946`](https://github.com/yetone/magpie/commit/9916a946080918074bc40e964613928d1a1b283f)（observer），[`af9bd6c3`](https://github.com/yetone/magpie/commit/af9bd6c3e227ed8a6baac5df8b33f6e934981005)（路由策略注册），[`4ac4d9ba`](https://github.com/yetone/magpie/commit/4ac4d9ba995057935e40664b1196938130ffa815)（gwmw/masker）。本轮已逐层核对，详细改动范围、可选依赖和后续验收见 [路由架构说明](upstream-routing-dependencies-2026-10-23.md)。

鲸桥实际通过 [RunContext](../internal/whalebridge/web.go) 直接创建 HTTP 服务，不经过上游 `gateway.ListenAndServe`。现在已接入 [有限 Host](../internal/gatewayplugins/host.go)：首次同步注册、管理刷新/内容检测、停止准入并等待在途请求后反向注销。组/账号权重和亲和补偿共用一次选择的凭证；当前脱敏出口均传播注册缺失及 Masker 异常。热更采用排空窗口，长流会延后切换，不承诺逐请求无等待的代际快照。P1 原 GUI/schema、完整 Watch 遥测和其他上游功能包继续留在该有限装配之外。

完整的 before.User/Reply 脱敏依赖上游 before 状态新字段；鲸桥前轮已用现有架构保护当前搜索/决策文本，不能仅复制最新 maskedState。原 CLI/TUI/Wails、会话扫描、客户端安装/代启动、原生账号自动切换、远程监听与公开 CallerKeys 继续不进入鲸桥范围。

图片预算是每请求原始字节与解码规模估算，不包括全部 resize 中间缓冲、输出正文或跨请求并发；256MiB 只约束全局压缩图片缓存。不能承诺全进程 heap 被 512MiB 严格封顶。本轮不以真实付费、外部账号或真实远端备份写入验证功能。
