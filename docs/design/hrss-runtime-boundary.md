# HRSS 与独立 EinoLoop 的解耦方案（分阶段实施）

日期：2026-10-09。范围：独立应用与 HRSS 适配并行实现，用户已授权本地部署验收；生产接入不在本轮发布范围。

## 目标

将 `/Users/yangyin/myself/vsjin-ai` 建为独立 Go 项目，基于 CloudWeGo ChatWithEino 的 Eino ADK 骨架实现自己的 EinoLoop 专家引擎。HRSS 的专家模式可以选择现有 CLI Loop 或 EinoLoop，快速模式保留前置授权检索和通常一次模型推理的策略。

产品名为 VSJin AI，服务/仓库为 vsjin-ai，执行引擎 ID 为 eino_loop。cli_loop 继续指现有 CLI App Server。模式 fast/expert 表示交互策略；引擎表示执行实现；模型表示推理资源，三者不能再互相代替。

## 已核对的源码事实

上游固定提交：`a6dbd95ab51fe9896a2bafa2e5a468e3bed01161`。
入口：https://github.com/cloudwego/eino-examples/tree/a6dbd95ab51fe9896a2bafa2e5a468e3bed01161/quickstart/chatwitheino

- agent.go 使用 `adk/prebuilt/deep`、`adk/middlewares/skill`，默认提供本地文件系统与 StreamingShell，MaxIteration 为 50。
- main.go 使用内存 checkpoint 与 JSONL 会话存储，可选 CozeLoop tracing，提供独立示例 Web。
- server/server.go 使用 TurnLoop 处理抢占、中止、事件消费和恢复；同时注入“读源码、运行构建”等教学用途指令。
- rag/rag.go 的 answer_from_document 读取本地文件、切为约 800 字的段落，再用模型评分和合成。这不是 HRSS 当前的向量检索服务。
- 示例的审批中间件会在 answer_from_document 调用前中断，不能直接保留为每次查询都要求审批。
- 上游 go.mod 依赖 examples 仓库根目录的 replace，不能直接将子目录复制后视为独立可构建项目。
- 已读取上游 LICENSE-APACHE。业务名称、提示词、界面可重做，复用的代码仍保留 Apache 许可、版权与来源说明。
- HRSS 当前依赖 Eino v0.9.13；本机安装包已确认具备 DeepAgent、Skill middleware 和 TurnLoop 泛型 API。用户本地 checkout 已将示例升级至 Eino v0.9.21，独立项目沿用此版本；HRSS 依赖保持不变。

HRSS 现状：

- server/plugin/scai/service/assistant_runtime_policy.go：ResolveAssistantModePolicy 将 expert 与 cli_loop 绑定，拒绝专家 API 模型。
- server/plugin/scai/service/session.go：专家深度分析/重试分支直接进入 streamCliLoop，后续通用分支才按 Runtime 选择。
- server/plugin/chat_agent/service/model_config.go：专家模型保存与解析均禁止 API 模型。
- web/src/plugin/chat-agent/view/providerPurposeBindings.js：专家引擎下拉仅 CLI Loop，API 模型兼容检查直接返回 false。
- server/plugin/chat_agent/service/eino_runtime.go：目前已经有嵌入 HRSS 的 Eino ReAct 实现；它不等于上游的独立 ADK 应用。
- server/ai/skillhttp：Skill 目录、声明接口、授权读取范围和用户身份绑定已经存在，但直接依赖 HRSS global、数据库与 JWT，不能原样导入独立项目。
- Skill 当前路径 deploy/cli-loop-app-server/policy/skills 虽带 cli-loop 名字，已由快速与专家共用。第一轮保持同一权威目录，避免再维护一套重复技能。

## 三种实现方案

| 方案 | 优点 | 代价 | 结论 |
| --- | --- | --- | --- |
| 独立 EinoLoop 服务，HRSS 使用中立运行协议接入 | 独立构建与升级；业务权限留在 HRSS；CLI 可继续使用 | 增加一个服务和协议适配 | 推荐 |
| 将 ChatWithEino 继续内嵌在 HRSS | 部署简单，开发起点短 | 难满足独立项目、引擎可替换的目标 | 不选 |
| 在独立项目中复制 HRSS 数据库、检索、Skill 授权和对话 UI | 独立界面直观 | 两套权限、会话与数据模型会持续分叉 | 不选 |

## 职责边界

HRSS 负责：用户/单位/项目权限，助手配置，资料选择，Skill 权威目录，对话与附件，前置知识检索，业务 HTTP 接口，确认草稿，审计及既有页面。

VSJin AI 负责：ADK Agent、模型迭代、Skill 按需读取、HTTP 工具调度、上下文预算、流式事件、执行取消和运行状态。不得直接依赖 HRSS Go 模块或业务数据库，不另建 NAS 读取或向量库连接。

两个专家适配器仅对外实现同一 AgentRuntime 能力。CLI 特有的 thread、channel、native task、App Server WS 仅保留在 CLI 适配器内，不能进入通用协议或 EinoLoop 数据类型。

| 用户模式 | 执行路径 | 检索策略 |
| --- | --- | --- |
| 快速 | 保留 HRSS 当前快速路径 | 先做授权向量检索；足够即一次推理，必要时 Skill HTTP |
| 专家 + CLI Loop | HRSS → CLI 适配器 → 现有 App Server | Agent 自主使用同一授权 Skill HTTP |
| 专家 + EinoLoop | HRSS → Eino 适配器 → 独立 vsjin-ai 服务 | ADK 自主规划、检索、增量读取 CAD、再形成最终回复 |

同一对话可逐轮选择模式；每轮固定引擎、模型和资料范围，运行中不能被后台配置变化切换。已有历史按原有呈现协议回放，不依赖原引擎仍在线。切换引擎时由 HRSS 的权威历史生成上下文，不直接混用原生运行状态。

## 项目与协议

拟定独立目录：

- cmd/vsjin-ai：服务入口；独立启动与健康检查。
- internal/agent：DeepAgent 构建、中文业务无关运行规则、预算和中间件。
- internal/skills：授权目录与正文的受控 backend，支持按需加载；不扫描个人技能或全磁盘。
- internal/tools：skills_read / skill_http_request 桥接，按本轮清单验证。
- internal/runtime：TurnLoop、并发、取消、事件序号及终态。
- internal/transport：有版本号的 HTTP/SSE 协议。
- internal/store：运行事件/恢复状态；不另存一套完整 HRSS 会话。
- docs、tests、Dockerfile、go.mod、THIRD_PARTY_NOTICES：独立文档、构建和来源追踪。

HRSS 的中立接口至少提供 Run、Cancel、Capabilities；需要 ADK 暂停时增加 Resume，状态采用 awaiting_input，不能继续占用实际运行额度。统一事件使用 run.started、reasoning.summary、tool.started、tool.completed、message.delta、run.awaiting_input、run.completed、run.failed、run.cancelled，带 runId、sequence、时间戳。reasoning.summary 只承载可公开执行摘要，不要求输出私有思维链。

协议传入：受信任的 runId、会话标识、消息/授权附件、HRSS 组装的系统指令、模型选择句柄、授权 Skill 名称/描述与工具 schema、资料范围快照、迭代/上下文/耗时预算。协议不传用户自报身份作为权限依据，不把密钥放入消息内容。

工具调用：EinoLoop 通过受控 HTTP 回调到 HRSS 的本轮工具网关；HRSS 验证运行授权，再复用 skills_read 和 skill_http_request 的现有执行逻辑。业务工具仍是 HTTP，不引入 MCP。运行授权由服务端与用户/项目/会话/本轮消息/助手许可/所选资料绑定，过期或取消后撤销。领域调用需要的短期 JWT 每次由 HRSS 创建，不把 JWT 签名密钥交给独立服务。

模型调用：HRSS 管理现有 Provider 配置，独立服务经本轮受控模型代理调用；模型/地址由 HRSS 配置，不能由普通用户或模型通过正文指定。运行令牌由 transport 持有，禁止进入提示词、工具文本和日志。单机开发可用独立本地环境配置直连模型验收，但 HRSS 接入验收走同一受控模型路径。

生命周期：同一用户会话同时一轮，clientTurnId 幂等，事件序号可续读。浏览器断开不等于主动取消；显式取消才中止，进程异常必须留下 failed 或 interrupted 状态，不能一直 running。工具成功而模型失败时不自动换引擎重放写操作；重试使用幂等键并核对实际草稿。没有非空最终回复不能标 completed。预算耗尽需生成有证据的收敛回复，无法生成则明确 failed。

DeepAgent 的文件系统、Shell、默认通用子 Agent 等原生能力采用显式配置，HRSS 业务接入默认仅提供授权 Skill HTTP，不能因复用示例自动得到主机访问能力。需要内部规划时使用独立内部事件，不把默认 write_todos 混作 HRSS 正式待办。

## 最终接入范围（第 2—6 项属于后续阶段）

1. 将上游骨架改造成真正可独立构建的 vsjin-ai；去掉 examples 根目录 replace、Eino 教学身份/提示词、教学 RAG 和默认教学 UI，保留许可证。
2. 新增中立执行协议及 HRSS 的 CLI/Eino 适配器；将模式与引擎解耦，修正原问题深挖、重试、取消和审计路径。
3. 后台专家配置增加 CLI Loop / EinoLoop；EinoLoop 使用启用的 API Chat 模型，CLI 沿用现有运行资源。默认配置不强制迁移。
4. 共用同一 Skill 与 HTTP 业务执行器；授予同一用户/助手时权限相同，不复制授权代码到独立服务。
5. 本地独立启动新服务并接入 hrss-dev，保留当前快速/专家用户界面及现有卡片呈现。
6. 取得真实模型、页面与边界验收证据，再创建对应仓库提交/PR；本轮不自动发布生产。

## 验收清单

- 独立项目不导入 HRSS global/GORM/model；离开 HRSS 源码目录也可 go test / go build，已有上游原始目录缺失仍能运行。
- 快速模式普通资料问答仍保留一次前置检索、一次推理，名称/描述首轮可见。
- CLI 与 Eino 专家在相同账号、同一助手下列出同一授权 Skill；无个人开发技能和未授权接口。
- 真正通过 EinoLoop 的技能读取、知识检索、资料原文增量读取、DWG 对象查询生成有出处的最终回答；仅工具执行不算通过。
- 按已有 DWG 原文构造工程专业问题和答案，不只问框架或工具名称；同时验证资料不足时不编造。
- 安全/质量上报真实 HTTP 准备草稿、正常自然语言回复、页面卡片与待补参交互；不越过用户确认。
- 同一会话从快速深入到 Eino 专家、从 CLI 切到 Eino、重试原问题均保留授权资料选择与用户本轮附件。
- 用户 A/B、不同项目、选择文件 A 却请求 B、目录空交集等均拒绝扩大授权。
- 文本先于 tool_call 的模型流不会提前结束；正文正常流式输出；非空最终文本后才完成。
- 模型/HTTP 超时、取消、断线重连、重复 clientTurnId、重复写工具均有确定终态和幂等结果。
- 本地测试统一 test_owner 和测试前缀，截图报告覆盖快速、两种专家及后台选择；测试会话/草稿清理，不改变演示数据。

## 状态

用户随后追加授权并行适配 HRSS。本轮目录沿用 main.go、agent.go、server、skills、policy，并增加中立 runtimewire 协议；HRSS 提供每轮已授权消息、工具定义及模型/工具响应，不把业务数据库或模型密钥交给独立引擎。独立应用的验证结果见 docs/acceptance；HRSS 页面与快速图片的端到端结果见 HRSS docs/acceptance/2026-10-09-einoloop-runtime-and-fast-images。
