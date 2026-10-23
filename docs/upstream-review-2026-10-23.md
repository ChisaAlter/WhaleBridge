# Magpie 上游审查与鲸桥适配 · 2026-10-23

本轮基于鲸桥 `504f668d308eab7d8f21c14bd7189be5620c5386`，保留已有工作区修改。完整供应商、目录、搜索与 Agent 来源仍为 `a75de4c21ac28be98af8334643077d8b0bf47df7`；本轮按功能依赖局部适配，没有把全部功能基线前移。

核查快照为 [`e00fbdbbc74f9829027a78769cbeb265d6271fb4`](https://github.com/yetone/magpie/commit/e00fbdbbc74f9829027a78769cbeb265d6271fb4)，提交时间 2026-10-23 10:54:27（UTC+8），源码归档 SHA256 为 `b1584c47cc2e2a5d8e42d41f3f987b070de308b56b7df0e056db8c0a7663e891`。相对原基线 **ahead 961 / behind 0**：[完整比较](https://github.com/yetone/magpie/compare/a75de4c21ac28be98af8334643077d8b0bf47df7...e00fbdbbc74f9829027a78769cbeb265d6271fb4)。核查时最新发布为 [`v0.1.1330`](https://github.com/yetone/magpie/releases/tag/v0.1.1330)，北京时间 11:57:41 发布，主要是查看者时区和 GitHub 大备份分片。

[来源清单](../upstream.json) 的 `reviewedCommit` 与归档哈希标识核查范围，`patches` 记录 50 个已局部适配的来源提交（前轮 30 个，服务与备份续轮新增 12 个，媒体授权轮新增 2 个，免费优先轮新增 1 个，网关注册架构轮新增 5 个）。[构建脚本](../scripts/build.mjs) 将这些信息写入组件清单。这里的条目不表示上游提交被逐字 cherry-pick，也不表示全部 961 个提交已同步。

## 退休供应商与插件交接

已贯通 11 个订阅族、13 个供应商 ID：`zed`、`kiro`、`cursor`、`qoder`、`qoder-cn`、`workbuddy`、`workbuddy-ai`、`mimo-app`、`zcode`、`factory`、`grok`、`devin`、`commandcode-plan`。来源为迁移基础 [0f1666b3](https://github.com/yetone/magpie/commit/0f1666b3875eecb3dec3729d701b3b950fa81358) 和退休提交 [9fcf6a6e](https://github.com/yetone/magpie/commit/9fcf6a6e7fd18601ed08622c19d39e24eb08dc0c)，配合核查快照中的插件协议和最低版本。

- `BuiltinRetired` 与实际 `Moved` 分开。退休后，请求、登录、模型及用量通过插件；迁移账本、原账号清理和恢复仍以实际迁移状态为准。账号尚未交接时，页面显示原因和交接入口，不再落回旧内置请求路径。
- 自动交接检查整个账号批次，只接管组件拥有的账号；出现原生或借用账号时跳过整批并显示诊断。用户可显式交接。自动操作不把原生账号擅自标成组件拥有。
- 保留账号顺序、首选身份、开关与暂停信息。过期或历史模型不再服务的账号也可保留交接状态及诊断；提交前重新检查完整源批次。失败回滚使用独立取消语义，若无法恢复则保留迁移中状态和插件凭据，避免删除已轮换的令牌。
- 退休来源不能迁回内置。停用保持停用；移除记为 `removed`，不由后台重新安装。保留插件登录及模型选择，显式重装后恢复。交接检查实际获选插件及其最低版本，不能被同 ID 的另一插件误代替。
- 旧 `*-plugin` 引用可迁到退休后的规范 ID，覆盖模型设置、路由组、fallback、Agent 名单和鲸屿默认模型／兼容配置。已有规范值优先；任一拼写上存在真实自定义端点时不做别名迁移。
- 插件认证按运行时报告的 method 类型分派。`keyless` API 认证可显式读取插件支持的身份，页面不再强制填写密钥；OAuth 不因请求里夹带 key 而被错误转为 API 登录。插件单账号模式和认证方法往返经过本地 fixture 验证。

实现主要在 [迁移核心](../internal/provider/migrate.go)、[鲸桥所有权检查](../internal/provider/whalebridge_migrate.go)、[移除与重装记录](../internal/provider/plugin_left.go)、[插件协议](../internal/plugin/providers.go)、[插件宿主](../internal/plugin/host.js)、[组件认证入口](../internal/whalebridge/accounts.go) 和 [引用迁移](../internal/whalebridge/migrations.go)。

## 模型目录、协议与鲸屿投影

| 适配 | 鲸桥行为 | 上游来源 |
| --- | --- | --- |
| 协议可用性 | 保留不可用模型的目录及显式选择；缺少兼容端点时不向 Agent 提供，也不挤占默认前 24 个候选。页面计算当前端点与协议草稿，显式单协议覆盖同样有效。 | [644ad1ed](https://github.com/yetone/magpie/commit/644ad1ed7af808ee4130f30e21dfc0f0d9bc8533) |
| 暂时下架 | 保留等待选择，上架时恢复；全部选中项下架时提供零个模型。恢复与新下架发生于同次刷新也正确合并。保存无关配置保留等待状态，手动清空、默认或定向取消有独立语义。 | [8febe3a5](https://github.com/yetone/magpie/commit/8febe3a50c1b5a51b53d7c2e6a3223cc66e6ccfe) |
| 多账号目录 | 第一身份与其他开启身份的模型取并集，实际请求仍按每个账号的模型路由。`nil` 表示未知／全部，`[]` 表示已知零个；落盘重读保持区别。一账号列目录失败保留该账号旧表，其他成功账号照常更新。 | [72109ea0](https://github.com/yetone/magpie/commit/72109ea0eb32a3ae695db11b361edba816f45bdf) |
| 服务等级与 Fast | 保留插件明确报告的 `priority`、`ultrafast` 等等级；Fast 只对声明 `priority` 的模型开放。转译保留有效等级，被上游拒绝后按已有回退路径去掉并记住拒绝。没有引入新的三态 Agent 加速架构。 | [e733b072](https://github.com/yetone/magpie/commit/e733b0722e31c824b6952a2c41f000ae83122011)、[8233eada](https://github.com/yetone/magpie/commit/8233eadaec5498741166a74ad7510c1318af9739) |
| 上下文覆盖 | 单模型值优先；`*` 只由本供应商已知的窗口／最大窗口限制，不借用其他供应商同名模型的上限。 | [273ab9c2](https://github.com/yetone/magpie/commit/273ab9c2359bad30ad6c01b69086f56cbadce9ad)、[7aad6a50](https://github.com/yetone/magpie/commit/7aad6a50ea981fcfef0c7182c193cd6e6d7fb61d) |
| 下架模型的旧限制 | 输出和压缩值未变时允许随其他设置保存；新填或更改未知模型的限制仍拒绝。组件先验证，再保存供应商，避免无效限制导致名称／密钥部分写入。 | [f985d6bd](https://github.com/yetone/magpie/commit/f985d6bdb68ed9359b35124f98b1fac5085886b3) |
| 不发送思考字段 | 模型级 `reasonless` 随改名、路由和备份保留，在协议转译及 ExtraBody 合并之后省略思考字段；组里其他成员的等级保留。页面明确该选项控制请求字段，模型内部是否思考仍由供应商决定。 | [f074996d](https://github.com/yetone/magpie/commit/f074996d93b87b17ea682c4b5951d52794bb28cb) |
| 刷新数量 | 使用合并、去重后的可用目录数量。有效插件目录后台变化调用 `catalog.Touched()`，触发鲸屿及已明确接入 Agent 的模型文件更新。 | [06c3370b](https://github.com/yetone/magpie/commit/06c3370b749763f3ff73f29dd50ff45abdad7ee0) |
| 输出字段及重试 | DeepSeek Chat 使用兼容的 token 字段；普通供应商请求遵守显式输出上限。上游明确报告上下文不足且至少剩 8192 tokens 时，至多缩输出重试一次。 | [62020b9c](https://github.com/yetone/magpie/commit/62020b9c264dad63707f654c81d72b4168d51c63)、[b9c589eb](https://github.com/yetone/magpie/commit/b9c589ebb1b0d6d5e80dda733173147755107a46)、[b749cffb](https://github.com/yetone/magpie/commit/b749cffbb1b6b1ce986a5480cb9a323b1a12f590) |
| 默认鲸屿输出预算 | 在鲸桥自己的 writer 中，默认输出为 `min(published, max(1, W/4, W/2 - 65536))`。显式输出优先，路由组继承最大输出的显式标记，同值时显式标记获保留；仍投影原有压缩阈值。 | [ca4a1cde](https://github.com/yetone/magpie/commit/ca4a1cde0dca3f3d4a1d6ef551da33ab1f41b2cf) |
| 用户 compat | 同渠道、同模型、同协议刷新保留用户兼容字段，新的托管值优先；协议变化不挪用旧 compat，非鲸桥拥有的凭据项拒绝覆盖。旧插件别名兼容两个 profile 层次。 | [51adf969](https://github.com/yetone/magpie/commit/51adf96958805e06a51e989ee571c6036bc49c24) |

主要实现为 [目录与窗口](../internal/provider/models.go)、[选择保存](../internal/provider/provider.go)、[插件目录](../internal/provider/plugins.go)、[后台刷新](../internal/provider/plugin_relist.go)、[模型偏好](../internal/provider/modelprefs.go)、[输出限制](../internal/gateway/maxoutput.go)、[思考字段处理](../internal/gateway/quiet.go)、[鲸屿 writer](../internal/whalebridge/dsh.go) 和 [profile 合并](../internal/whalebridge/profile.go)。

## 维护、隐私与可靠性

| 适配 | 鲸桥行为 | 上游来源 |
| --- | --- | --- |
| 关闭／隐藏来源 | 后台续期、保活、暖机、到期重置、签到、用量与目录调度检查来源状态；显式操作保留原行为。保留静态本地凭据读取；插件宿主的全局列目录仍可能调用插件，不声称关闭一个来源就禁止全部插件网络。 | [75cbc7e1](https://github.com/yetone/magpie/commit/75cbc7e1e5fdd0aa54d0b1ce39d7d626395c3081)、[62fc1b4b](https://github.com/yetone/magpie/commit/62fc1b4b57b29fba8ea000c3328b34c9b3825b1e) |
| 取消登录 | 内置 Codex、ChatGPT API、Gemini、Copilot、Claude 流程取消或被新流程替代后，晚到凭据不能再新增／替换账号。保存成功与取消共锁，晚到取消不能反转成功。未声称本轮证明了插件内部 OAuth 的取消行为。 | [40df6dbd](https://github.com/yetone/magpie/commit/40df6dbd3829d77c634e185b821a2af9a445c028) |
| 损坏签到账本 | JSON 损坏、空、`null`、错误类型和读失败停止请求及覆盖，保留最后有效显示，定时重试。解析诊断只含路径和固定说明，不回显字段值。 | [a8ecf402](https://github.com/yetone/magpie/commit/a8ecf4020e3b9fedd1ff5cc1de64c0bfc4f5810b) |
| 搜索脱敏 | 六家已有搜索 API、Kimi 和 WorkBuddy 在出站前脱敏，尊重目标供应商自己的本地例外；只在最终返回 Agent 时恢复标题／正文／URL，内部搜索 hits 继续用占位符。Alpha 私有鉴权及现有 MCP 入口关闭保持有效。 | [88ce2cdd](https://github.com/yetone/magpie/commit/88ce2cdd4c5ecdeb3da99222d70177e19594d5ad) |
| 决策脱敏（局部） | 用现有 `redact.Mask` 保护 `askJev` 当前 `text`；路由组成员允许原文不会自动把该例外授予决策模型。没有复制上游新的 `before.User/Reply` 或 gwmw 架构。 | [48aae83b](https://github.com/yetone/magpie/commit/48aae83b82973e35bb6caa0057c6a8fe6aad065d) |
| HTTP/2 与 Recent | 默认静默 20 秒后 ping，10 秒等候超时，保留自定义 HTTP/2 配置和既有 HTTP/1 例外；最近请求容量满时移位并替换末项，释放被移出请求正文的底层引用。 | [94919109](https://github.com/yetone/magpie/commit/94919109cb3b9d4241bb957c9e571c6f4c236d2f)、[5090f321](https://github.com/yetone/magpie/commit/5090f3215b806fec0ad55dd1a4a449bd45ae12e7) |

前一轮五个补丁全部保留：

| 来源 | 鲸桥中的行为 |
| --- | --- |
| [88b4c00e](https://github.com/yetone/magpie/commit/88b4c00ea11c8653cada22e11edf20ab1594de43) | Codex 续期开始前检查调用者是否取消；已发送的续期保留上下文中的代理选择，以独立的 30 秒期限等待轮换凭据。 |
| [d5847f19](https://github.com/yetone/magpie/commit/d5847f19854dd609dc2c6b38001c9ef11da8d926) | 现有原生 Codex／Claude 凭据写入使用已存在的 `writePrivate`，临时文件 flush 后替换，没有增加原生账号写入入口；持续磁盘故障仍可能无法保存轮换结果。 |
| [e297a8ab](https://github.com/yetone/magpie/commit/e297a8ab27461ed32b03ce8be0f9bf957a2e754d) | 保全损坏自动重置账本，从最近使用历史重建，丢弃部分解析结果；历史也不可读时持久暂停每周耗尽自动重置七天。缺失历史按原规则为空；独立的即将过期重置另行处理。 |
| [707fbb24](https://github.com/yetone/magpie/commit/707fbb249df755423d3b2b77c6f001dd376d75c4) | 设置读取／Stat 错误阻止备份和同步，不把损坏设置封装成默认值上传；保留鲸桥对其他 Agent、字体和本机授权的同步过滤。 |
| [0dc643fd](https://github.com/yetone/magpie/commit/0dc643fdb7f1faf1031bd8e986f5d6795cd308e4) | npm cmd／PowerShell／sh shim 直接读旁边包版本，不运行 CLI；实际 Cline 版本探测设置 `CLINE_NO_AUTO_UPDATE=1`。 |

## 供应商模型页

[管理页面](../internal/whalebridge/assets/app.js) 与 [样式](../internal/whalebridge/assets/style.css) 保留上一轮逐行目录：名称、完整 ID、上下文、能力和输入／输出参考价分层，勾选与参数编辑分开；提供名称／ID 搜索、已选／未选／免费筛选和批量选择。限定目录滚动高度，手工 ID 与维护项折叠收纳，窄窗口保持单位与参数按钮可读。

本轮新增协议不可用与暂时下架说明、服务等级、不发送思考字段开关、退休来源交接通知及 keyless 登录反馈。刷新目录保留参数和选择草稿：用户取消的模型不会因刷新时下架而重新进入等待；完整选中兼容目录时继续接纳真正新上架的兼容模型，部分选择保留原意。搜索、选择和价格草稿仍不互相覆盖。

独立只读复核发现并已补回归的五项问题：单端点协议覆盖被忽略、取消最后一个可用模型丢失等待、取消后刷新复活等待、完整选择刷新漏新模型、路由组丢显式输出标记。最新限定范围复核没有剩余明确 P1／P2；这不等于审过全部 961 个提交。

## 前轮验证记录

- Go `provider` 整包通过，最终 37.975 秒；`whalebridge` 整包通过，最终 106.325 秒。包含真实 HTTP 管理入口、目录下架／恢复、草稿保存、keyless 登录派发、别名、后台 DSH 更新及路由组显式输出投影。
- `catalog`、`settings`、`backup`、`davsync`、`proc`、`netproxy` 六包整包通过。provider／gateway／plugin／whalebridge／settings／catalog／backup／davsync／proc／netproxy 十包 `go vet` 通过。最后追加的定向删除等待项 HTTP 回归通过（4.092 秒），所有本轮 Go 文件格式与 `git diff --check` 通过。
- 五组 Electron 页面交互回归全部通过，最终 24.494 秒；查看浅色、深色与窄窗口模型目录及参数区。新测试覆盖退休交接／keyless、端点草稿、下架等待、取消后刷新、完整选择接纳新模型与 reasonless。
- 插件包除 `TestPluginHTTP2TakesTheProxy` 外整包通过，9.269 秒。唯一排除项在本机 Bun 1.3.14 抛 `Response(status: 0)` RangeError，已用未修改 HEAD 宿主复现；项目要求 Bun >= 1.4.2，不能据此声称插件全量测试通过。新增协议 fixture 在当前运行时通过。
- HTTP/2 验证活连接长等待正确响应 ping、HTTP/1 例外与自定义配置保留。新增失活 TCP 中继测试确认静默连接在客户端超时前被放弃、后续请求新连线恢复，路由组及时换下一成员；两项新增测试一起通过，4.519 秒，并再跑 gateway vet。Recent 用弱引用与 GC 验证丢弃正文释放。
- 网关旧 Factory／WorkBuddy 内置夹具已迁为实际本地插件路径，四项定向回归通过，11.384 秒；未新增 skip 或恢复内置入口。最终 `gateway` 整包通过，152.944 秒；之后新增的失活 HTTP/2 两项一起定向通过，无产品代码追加修改。
- `usage` 整包通过，4.641 秒；七项 Windows 可运行的 Agent 配置恢复案例通过，11.952 秒；九项组件镜像协议测试全部通过，3.471 秒。
- 新构建为 1.2.0，36,963,328 字节，SHA256 `d3fd7a455d0363fe47be2d023c15c1b4fdeb238c2b823db50e490d8b2b5457b7`，清单含 30 个来源条目和核查快照。已用该二进制在隔离目录通过原生启动、嵌入管理页、Agent API、私有访问及默认鲸屿配置保留检查，临时进程清理完成。产物为 [Windows 组件](../.tmp/upstream-adaptation-package/WhaleBridge-win32-x64.exe) 和 [构建清单](../.tmp/upstream-adaptation-package/WhaleBridge-component.json)。

验证使用隔离用户／组件目录、假 HTTP 服务和本地测试插件，没有真实账号授权、付费模型调用或远端备份写入。未执行真实 Keychain、断电、Windows CreateFile 持锁专项、race 或跨平台验收。没有替换当前安装的组件，没有提交、推送或发布。

## 续轮：按依赖推进剩余更新

先完成 [依赖与优先级梳理](upstream-remaining-dependencies-2026-10-23.md)，再实施以下独立闭包。所有服务仍是用户显式配置的 API 供应商，不增加订阅凭据发现或 Agent 接管。

| 闭包 | 鲸桥行为与边界 | 上游来源 |
| --- | --- | --- |
| GitHub 原子大备份 | 先迁 Git Data blobs/tree/commit/ref、非强制 ref 更新和并发检查；再接 16MiB 分片、每片 SHA/大小/总量校验、同一提交清理旧片。备份本身并发变化拒绝覆盖，其他文件变化有限重试且复用已上传 blob。旧单文件可读，64MiB 上限及私有仓库、Token 域名规则保留。 | [93b87d00](https://github.com/yetone/magpie/commit/93b87d00044db93d739db0ca2bc58c52f9f34cfc)、[a6efe07c](https://github.com/yetone/magpie/commit/a6efe07cd7977bd9d3136dcc0044765ff5f3ef1e) |
| 搜索服务管理 | 以保存顺序尝试服务，可停用而保留密钥；页面上下移动、显式启停，备份恢复保顺序/Off，缺省密钥保本机原值。多密钥默认轮换，可改顺序；仅密钥拒绝换下一个，网络/服务器/格式错误交给后续服务。 | [a5d72a09](https://github.com/yetone/magpie/commit/a5d72a095b3bab14d6369d48d21fa12a900920f2)、[ec13d116](https://github.com/yetone/magpie/commit/ec13d1162a757b4dc7201e6d1aa75fe56271124b) |
| 密钥冷却与 Tavily 额度 | 运行时用散列标识密钥；拒绝按类型休息并尊重 Retry-After，成功恢复。额度在用户主动读取时查询并缓存两分钟；未知/非法计数不呈现成功零用量，充值后可解除额度暂停。普通页面状态不触发额度查询。错误和套餐回显按整池最长密钥优先遮盖。 | 同上 a5d72a09 |
| DeepSeek Search | 接现有私有搜索链，POST /v1/tools/search，按 128 个 Unicode 字符限制查询，解析摘要/snippet/日期。出站前脱敏、最终向 Agent 恢复及 MCP 入口关闭沿用前轮规则。 | [d432920f](https://github.com/yetone/magpie/commit/d432920f02c197a41db7faf0a958a681f7905769) |
| 自定义 POST 余额 | 可选择 GET 或空请求体 POST。保存与请求前都拒绝其他方法；只作用于自定义 BalanceURL，原 Header/代理/专用余额令牌/表达式保持，字段随备份保留。 | [dc8dcc85](https://github.com/yetone/magpie/commit/dc8dcc85d117c2c8ad5ff422733b9e125b0e1909) |
| Nous / SiliconFlow 国际站 | Nous Portal 只配置统一 Chat 端点并实时列模型，避免把仅支持部分模型的 Messages 端点用于所有模型。SF 默认保持中国站，可切国际站并同步网站/密钥页；两站账号和密钥独立。 | [591ed58b](https://github.com/yetone/magpie/commit/591ed58b536b17162a0715aa7210d9b7d0db4c09)、[430871d6](https://github.com/yetone/magpie/commit/430871d632e6e0aa1ea0cd2bc9f69665115be520) |
| Bedrock Mantle | 17 地区、实时模型列表、沿 Bedrock API key 认证；按模型选 /v1 或 /openai/v1，明确 wrong-route 时只改试一次。Messages 固定 x-api-key、Chat/Responses 使用 Bearer，页面同步说明；CanFast 不提供自动 Priority，同协议调用者显式 tier 沿原 runtime 行为。 | [c5f15cae](https://github.com/yetone/magpie/commit/c5f15cae7ccc8a67ea944f4b8d2c2fdf801611a8) |
| Codex 图片预算 | 现有 Responses 内联图路径内加入单次遍历/去重、最多四 worker、512MiB raw+解码估计批预算与单图 320MiB 预检；压缩图片缓存为 256MiB/8192 项/闲置一小时 LRU。流式读配置穿过长 EXIF；补乘法溢出与透明灰度 PNG tRNS 的色深估值。保留其他账号、API-key、协议、low/detail、URL 等门控。 | [11173c8a](https://github.com/yetone/magpie/commit/11173c8a20bf8e74b10ca08b509c7675c62dadb3)、[3eee19b0](https://github.com/yetone/magpie/commit/3eee19b08be25423d5bb0a9fa41fe0a3ccfc76a2)、[a9304efe](https://github.com/yetone/magpie/commit/a9304efe016c832560885fc88a10018f136bdcc5) |

独立复核补齐两项 P2：Mantle 冷缓存并发请求各自按实际发送前缀决定单次重试，不因另一请求已学会路由而漏重试；错误响应在格式化／截断前先遮盖整池密钥，避免纯文本跨截断边界时残留密钥片段。图片头估算另修正索引色 PNG 必须读取 PLTE/tRNS 的兼容性，真实透明与不透明调色板图均覆盖。最后复核补齐第三项 P2：配置头估算失败的图片在完整 base64 分配前就停止处理，避免其零估值与合法解码任务同批时漏算原始数据内存。

图片预算没有覆盖所有 resize 中间缓冲、输出正文或跨请求并发，512MiB 不是全进程 heap 上限；本轮未复现真实并发内存事故。灰度透明 PNG 的头扫描固定 1MiB 上限且不按 chunk 声明分配内存，超过上限按原图发送。

## 续轮验证记录

- `provider` 整包通过，56.918 秒；`whalebridge` 整包通过，129.004 秒。之后新增 Nous／SiliconFlow／Mantle 保存、空 key 保留、目录刷新与安全 DTO 往返三项子案例再次通过，3.934 秒。
- `gateway` 在最后图片预检修复后整包再次通过，156.533 秒（前一次 164.088 秒），覆盖 Mantle 串行／并发、图片预算和现有私有网关、协议、插件、搜索脱敏回归。DeepSeek 加入 Alpha 真实鉴权搜索隐私夹具。
- `davsync`、`backup`、`settings`、`catalog`、`netproxy`、`proc` 六包整包通过，分别 20.610／5.640／1.581／2.614／1.877／1.798 秒。包含大备份两机设置同步、旧单文件兼容、CAS、分片完整性与坏设置不上传。
- 错误截断边界六子案例独立定向通过，4.860 秒；Mantle 四种确定性并发时序通过，3.870 秒；图片最终 14 组轻量夹具全部通过，5.366 秒；含透明／不透明调色板、小内存分配回归，验证不可读头和已满足尺寸的图片不会先分配完整源。原始图片 JSON 字节保持、重复去重、跨轮缓存、并发 cold-cache、LRU/idle、批预算、8/16 位色深、长 EXIF 与恶意大尺寸头均有覆盖。
- provider／gateway／whalebridge／davsync／backup／settings／catalog／netproxy／proc 九包 `go vet` 通过；全部已改 Go 格式与 `git diff --check` 通过。没有新增依赖。图片并发 `-race` 在运行前因 `CGO_ENABLED=0` 且本机无 C 编译器被工具链拒绝，未执行；没有下载编译器或以并发夹具代称 race 通过。

- 六组 Electron 页面交互最终全部通过，0 skipped，20.528 秒。新覆盖 GET/POST 保存重开、预设与区域选择、Mantle 固定鉴权说明、搜索启停/排序/轮换/DeepSeek/显式额度读取、未知值及文本转义；390/1120 浅深主题均检查页面与编辑器，窄屏额度按字段纵向呈现、每字段全宽且无横向溢出。旧页面断言随新增管理控件更新，等待实际保存刷新完成后重开，未使用固定 sleep 掩盖状态竞争。
- 最终构建为 1.2.0，37,052,928 字节，SHA256 `c66075533d6fd5d82bf364d47ba422081a72deb3d48280a82b663027a89ceda5`，清单含 42 个局部适配来源及同一核查快照。新二进制在隔离目录通过原生启动、私有管理页 Cookie、Agent API 和默认鲸屿配置保留验收，临时进程已清理。产物为 [Windows 组件](../.tmp/remaining-adaptation-package/WhaleBridge-win32-x64.exe) 与 [组件清单](../.tmp/remaining-adaptation-package/WhaleBridge-component.json)。
- 本轮使用假服务与本地 fixture，无真实供应商、付费模型或 GitHub 写入；没有替换安装、提交、推送或发布。前轮 Bun 1.3.14 的单项插件 HTTP/2 环境限制仍见前轮记录，本轮未改变插件宿主或声称解除该限制。

## 媒体轮：显式本机授权及受约束 fake-IP

先按 [媒体授权与依赖说明](upstream-media-boundaries-2026-10-23.md) 梳理来源、配置和消费路径，再实施两个独立来源提交。此闭包不依赖 gwmw／pluginrt，也不增加当前鲸桥没有的 web_fetch 工具。

- [`ea3dee96`](https://github.com/yetone/magpie/commit/ea3dee96a89cfb4710326342e840d58811eaa79a)：删除 loopback 调用的隐式私网例外，新增设置文件专属 `fetchPrivate`；普通页面不能修改或读出它，正常保存和加密备份导入保留本机值。`KeepOwn` 复制本机清单，恢复不继承外机授权，同步 hash 忽略本机清单变化。备份仍可序列化此字段，不把授权不生效描述为字段不外发。
- 图片输入统一走受保护客户端；供应商输出图片／视频额外允许自己已配置端点的精确主机及有效端口，跨端口和重定向不继承。MCP 的 URL 下载兜底同样受保护。图片下载失败继续返回已生成 URL，视频失败保持 502，MCP 说明原 URL 且不落盘失败内容。媒体请求不附带供应商／网关认证或 Cookie，供应商代理 `Via` 保留。
- [`240f3eb4`](https://github.com/yetone/magpie/commit/240f3eb4c5faca86fed15b48ee984ec38c49860c)：地址分类抽到 `netguard`，保留所有 DNS 答案检查、固定 IP、最后拨号重检与最多五跳 redirect。命名主机在剔除配对 Loon AAAA 后全为 fake-IP 才自动兼容：TUN 只拨本次地址，本机 loopback／localhost 代理使用已检查名称，远端代理不获自动豁免；独立显式授权仍可使用其代理。
- 图标接共享分类后收紧原来误放的 0/8、240/4、部分 IPv6 特殊和 NAT64 私网地址；6to4 从全部拒绝改为按嵌入 IPv4 判断。图标仍沿旧独立 fake-IP、iconproxy 与代理路径，不继承媒体授权，明确记录行为差异。
- 本轮复核期间补严本机 HTTP 代理的 foreign Host、命名例外的最终固定地址、TLS 自定义根与 SNI 克隆。测试覆盖这些边界；独立只读复核最终无未解决的来源级 P1／P2，随后完成其提出的 Host 与两机 DAV 验证要求。

### 媒体轮验证记录

- `gateway` 最终整包通过，161.516 秒；`netguard`、`provider`、`whalebridge`、`imagemcp` 四包整包通过，分别 0.662／33.874／101.230／1.661 秒。私有管理 HTTP 测试另外验证伪造清单不授权、正常设置保存／备份导入保留、文件撤销后立即停止下载，3.859 秒。
- `settings`、`backup`、`davsync` 三包整包通过，分别 1.222／4.654／12.860 秒。包含六个新主测试；实际 Collect→Seal→Open→Restore、两台隔离机器的假 WebDAV 双向同步，以及仅授权增删时零额外 PUT、远端字节不变。
- 下载 guard 14 组定向通过，4.872 秒；包括 HTTP、CONNECT、SOCKS、本机 TUN 替身、foreign Host、全部 DNS 答案、IPv6 zone、重绑定、精确端口、坏设置、重定向与 TLS 根／SNI。图片／视频私有网关和 MCP 真实 JSON-RPC 协议的定向回归分别通过，4.838／2.155 秒；旧 Ark、自定义视频、Responses 视频和 OpenRouter 图像邻近回归一起通过。
- netguard／netproxy／provider／gateway／imagemcp／settings／backup／davsync／whalebridge 九包 `go vet` 通过，全部已改 Go 文件格式与 `git diff --check` 通过。页面没有新增授权控件；普通设置保存和安全 DTO 的实际 HTTP 回归通过。
- 新媒体构建为 1.2.0，37,061,632 字节，SHA256 `7625c781ce7579571e4d6d58bfedd437d616efd437992eaf340f2b5f83ef9d1e`，清单含 44 条局部适配来源及原核查快照。已通过隔离原生启动、嵌入管理页、私有 Cookie、Agent API 与鲸屿原供应商／默认模型保留验收。产物为 [Windows 组件](../.tmp/media-adaptation-package/WhaleBridge-win32-x64.exe) 和 [组件清单](../.tmp/media-adaptation-package/WhaleBridge-component.json)。
- 本轮只用本地假服务、假解析器与隔离设置；没有真实供应商调用、外部备份写入或真实 Clash／Surge／Loon 验收。没有替换已安装组件或更改真实授权，没有提交、推送或发布。

## 免费优先轮：独立路由与注册架构范围

先完成 [Free first 与插件注册的依赖说明](upstream-routing-dependencies-2026-10-23.md)，再接入 [75e769a9](https://github.com/yetone/magpie/commit/75e769a9f9b419f805b925f147146eff179111b9) 的独立行为；不引入插件运行时。来源记录增至 45 条，完整基线和核查快照保持原值。

- 供应商及路由组接受并保存 `free`。排序使用当前候选账号/模型的免费事实：供应商明确标记免费，或已有 `ListPrice` 确认基础及所有上下文档位的全部计费字段为零。未知价不能证明免费，本地费用倍率、价格覆盖及另一供应商同名模型的标记不改变判断。普通 fetched `Model.Price` 不自动成为 `ListPrice`；保留原远程网关与目录价格来源合同。
- 内联 `weighRouted` 仅在采用免费优先时读取免费事实，稳定地把免费层放在其余之前，各层保持原顺序。协议适配、可用性、冷却、限流沉底、会话亲和、子组/显式另设策略以及主供应商/备用阶段保持原边界，没有全局重排展开后的候选。
- trace 按账号/密钥身份和模型记录，修正上游同账号多模型互相覆盖及单候选没有标记的解释缺口。页面显示“免费优先”、保存和重开保留，提示可能转付费及亲和/额度/冷却继续有效；其他策略不因父组免费优先被贴上付费标签。免费模型筛选同时检查原价的缓存和全部档位，不以本地零价覆盖冒充免费。独立复核另补齐普通抓取目录的零价显示与真实价来源不一致：筛选只接受明确免费标记或已知 `list` 原价，不把缺少 `list` 的显示价格自动当免费。
- 复核发现并补齐插件宿主的同 ID 跨账号问题：模型并集采用第一份元数据，会丢掉另一账号的免费差异。宿主现在同时输出当前账号的 `freeModels` 完整集合，缓存写入、重新组装账号及同账号失败保留都带上该集合。`nil` 是未知、`[]` 是明确没有免费模型；旧缓存即使只剩单账号也不借用历史合并标记，已知全零原价仍可成立，下次目录刷新会补齐精确事实。展示目录并集和账号模型 `nil/[]` 语义保持。
- 相对来源，有意加强一小时缓存/所有价格档位检查、逐账号免费事实和按模型 trace。新测试使用本地假服务、真实私钥 Handler、本地插件宿主、真实组件管理 HTTP 与 Electron；没有调用付费供应商。

注册架构的范围已拆清：Go 内置 `pluginrt` 与现有 Bun 供应商宿主是不同机制；策略、Observer 和 Masker 类型上可分别接入，P1 原 GUI 内核不是必要前置。鲸桥实际 [Run](../internal/whalebridge/web.go) 直接启动 HTTP 服务，不经过上游监听函数；只复制上游监听函数的注册和刷新代码不会进入产品路径。完整提取策略要统一权重与亲和状态、未注册时的行为；Masker 要贯通所有要求脱敏出口的失败关闭。后续均须闭合实际入口的首次同步应用、热更、worker 停止、请求排空及反向注销。

| 状态 | 方向 | 后续边界 |
| --- | --- | --- |
| 本轮落地 | Free first | 按当前内联路由独立交付；判断、保存、回退、trace 和鲸桥页面共同验收。 |
| 后续网关注册轮已完成 | 网关插件注册 | 真实入口有限装配、同步首次注册、排空式热更、关闭后反向注销和共享权重已落地；[依赖梳理](upstream-routing-dependencies-2026-10-23.md) 保留为历史范围，实际能力以 [生命周期说明](gateway-plugin-runtime-2026-10-23.md) 及下方最终验收为准。 |

### 免费优先轮验证记录

- `provider` 在逐账号免费事实修正后整包通过，34.517 秒；最终 8 组 FreeFirst 定向及 provider vet 通过，5.447 秒。覆盖五项基础价格、各价格档位、一小时缓存、未知价、实际账号、旧缓存单/多账号、原价与本地覆盖及保存往返。
- `whalebridge` 整包通过，113.068 秒；随后追加普通 fetched 零价的真实保存→管理 DTO 来源回归，和真实 `Run` 私有 Cookie、供应商/组保存、无凭据备份往返一起通过，4.989 秒，组件 vet 再次通过。没有为测试重新实现生产组处理器。
- `settings`、`catalog`、`backup`、`davsync` 四包整包通过，分别 1.797／5.086／8.460／16.891 秒。免费优先保存在既有供应商/路由组配置中，复用原备份语义。
- `gateway` 最终整包通过，153.530 秒。新 9 组网关 FreeFirst 定向全部通过，8.637 秒，无跳过。含私钥 Handler、实际跨协议回退、严格亲和、子组/另设策略、沉底、同密钥多模型 trace、已用额度不重排免费层；追加真实本地插件宿主 RPC→逐账号免费集合→账号展开→Handler，验证同模型账号免费状态反转及免费账号 503 后付费接续，实际认证和 trace 一致。
- 插件包除既有 `TestPluginHTTP2TakesTheProxy` 外整包通过，9.110 秒；该项仍因本机 Bun 1.3.14 与项目 >=1.4.2 要求的既知差异排除，未新增 skip 或声称插件全量通过。新五组逐账号免费事实和原协议六组一起通过，5.254 秒，覆盖真实本地宿主、单/双账号、旧缓存、明确空集合、缓存落盘、失败保留和切片不共用。
- 七组 Electron 页面回归最终全部通过，19.316 秒，零跳过。免费优先新增用例覆盖供应商/组六次策略保存、整页刷新、草稿保留、真实免费标记与中文解释、1120/390 宽度及浅深主题；16 个价格事实和实际筛选/“只选免费”排除缺 `list` 的显示零价、缓存/档位收费及本地价格覆盖。旧模型页夹具只给明确零价的目录项补齐真实 DTO 的 `list`，保留原价格恢复断言。
- provider／gateway／plugin／whalebridge／settings／catalog／backup／davsync 八包 vet、全部已改 Go 文件格式及 diff 检查通过。独立复核报告的同名模型跨账号免费标记和页面零价来源两项 P2 均已修正并加回归。
- 新构建为 1.2.0，37,076,480 字节，SHA256 `0daf2c90f7a3765a431ef8cffb1e2a8a9bc52ca8bc897d54261c656d25160562`，组件清单含 45 个局部适配来源及原核查快照。隔离原生启动、嵌入管理页、私有 Cookie、Agent API 与鲸屿原供应商/默认模型保留通过。产物为 [Windows 组件](../.tmp/routing-adaptation-package/WhaleBridge-win32-x64.exe) 与 [组件清单](../.tmp/routing-adaptation-package/WhaleBridge-component.json)。
- 验证使用本地假服务/插件和隔离目录，没有真实付费调用、外部备份写入、组件安装替换、提交、推送或发布。免费优先轮当时没有执行 race 或跨平台验收；当时网关插件生命周期只完成范围梳理，现已在下方网关注册轮接入。

原 CLI/TUI/Wails／菜单栏界面、会话扫描、客户端安装与代启动、原生账号自动切换、远程监听和公开 CallerKeys 继续排除。鲸桥模型入口使用私有密钥认证。基线已有 Vertex、多协议目录、分页、NoModels、RPM、价格覆盖、账号维护、GitHub 同步及受控 Agent 接入，不重复列为新能力。

## 网关注册架构轮：实际入口与最终验收

本轮继续保留开始前的 173 个 dirty 文件。开始前 diff 和未跟踪文件清单另存隔离目录；用临时 Git index 对照既有 diff，未改变用户真实 index，确认本轮改动范围与全部原未跟踪文件均保留。没有提交、推送或安装替换。

生产入口现在同步装配 rotation、middleware、redact、otel；实现 [Host](../internal/gatewayplugins/host.go) 的内容 hash 检测、可取消刷新等待、内部回调准入及排空窗口。[RunContext](../internal/whalebridge/web.go) 先停止配置维护、排空 HTTP，再回收闲置 Claude、JS worker 及反向注销。父 context 停止不会提前取消已接收 SSE；长流会延后刷新和关闭，正常新请求在热更期间等待。不是无等待代际快照，不承诺跨所有注册点原子发布。

组/供应商/亲和共用 routes 权重状态，采用独立、单次补偿凭证，避免并发请求互相覆盖最后一次选择。JS gateway 模块接入策略与白名单 onUsage，更新和停用时注销旧名称并等待 worker；页面保留暂未加载的选择。要求脱敏的所有当前出口均在 Masker 缺失/异常、设置不可读时失败关闭，原本机例外与后续历史视频恢复继续有效。完整范围和关闭顺序见 [生命周期说明](gateway-plugin-runtime-2026-10-23.md)。

来源增加 5 个局部记录：运行时、策略注册、最小 Ledger observer、gwmw/Masker，以及 [3f913b3d](https://github.com/yetone/magpie/commit/3f913b3de6b1ad51c88016c605321a02aab6dee4) 的 JS 策略/onUsage。来源总数 50，完整功能基线及核查快照不变。没有 P1 GUI/schema、全量 Begin/Watch 遥测或 `plugins/all`。

最终验证（所有后台任务均已收取）：

| 检查 | 结果与实际边界 |
| --- | --- |
| gateway 最终整包 | PASS，157.961 秒，包含全部既有 Free first、权重、协议/亲和/回退、隐私与媒体回归；设置实际本地 Bun，插件账号 fixture 没有跳过。 |
| whalebridge 最终整包 | PASS，102.236 秒。独立子进程真实 RunContext 验证首次启用、原配置 InUse、损坏 YAML、持流刷新与退出、ready 移除、监听关闭及全部资源注销。 |
| gatewayplugins 最终整包 | PASS，2.907 秒。6 组 Host 回归及 1 组实际 middleware manifest：等长/同修改时间 JS 编辑、新请求等待、旧请求/辅助回调仍用旧 hook/策略、完成后改名生效、卸载与关闭后不复活。 |
| 注册基础 | 最终 pluginrt PASS 0.536 秒（15 组，含新配置失败恢复旧依赖链和恢复失败有界报告）、gwmw PASS 0.799 秒；routes PASS 0.599 秒、middleware 整包 PASS 2.555 秒、usage 整包 PASS 2.180 秒。masking 无独立测试文件，由网关出口回归覆盖。 |
| 隐私与子进程 | 新 4 组隐私测试 PASS 3.474 秒，三种失败模式下 8 个实际 Handler 及搜索/决策/Codex 出口零供应商请求，另验证本机例外、视频隐私关闭后恢复、捕获实例。已 abort 移出 map、尚未 reaped 的 Claude run 由独立 WaitGroup 等待，定向及最终网关整包均通过。 |
| provider / plugin | provider 整包 PASS 33.626 秒；plugin 除原 `TestPluginHTTP2TakesTheProxy` 外整包 PASS 6.517 秒。该 H2 项仍受本机 Bun 1.3.14 与项目 >=1.4.2 要求的已知差异影响；未新增产品跳过，不称插件整包全部通过。 |
| 设置与备份 | settings PASS 0.746 秒、backup PASS 4.005 秒、davsync PASS 11.537 秒；没有真实远端写入。 |
| 页面 | 8 组 Electron 全部通过，19.980 秒，零跳过；新增策略组验证供应商/组六次真实保存、未加载/已加载/卸载后的选择保留与 HTML 字符安全显示。 |
| 静态与复核 | 11 个相关包 vet、全部已改 Go 格式、diff 空白与 50 条唯一来源检查通过；最终网关/管理入口/Host vet 复跑通过。两项独立读审：路由无确认 P1/P2；生命周期指出的取消后准入/提交和已 abort reaper 漏等均修复并补回归。 |
| race / 跨平台 | 尝试 routes race 被环境拒绝：CGO_ENABLED=0 且未找到 gcc/clang，没有安装工具链；不称 race 通过。普通并发凭证测试通过。本轮仅 Windows/amd64 原生验收。 |
| 构建与隔离原生 QA | 1.2.0、37,289,984 字节，SHA256 `13428b8c169b2e2faec469ea542827a2b02099ed2ad80e432d38b3e9e1b6bb23`。清单来源/size/hash 与上游记录一致，构建后无更新的产品源码；隔离启动、嵌入管理页、私有 Cookie、Agent API、鲸屿原供应商/默认模型保留全部通过。 |

新产物为 [Windows 组件](../.tmp/runtime-adaptation-package/WhaleBridge-win32-x64.exe) 与 [组件清单](../.tmp/runtime-adaptation-package/WhaleBridge-component.json)。本轮没有付费供应商调用、公开第三方插件执行、真实远端备份写入或已安装组件替换。
