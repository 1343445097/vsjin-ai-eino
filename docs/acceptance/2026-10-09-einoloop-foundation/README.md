# VSJin AI EinoLoop 独立应用验收

日期：2026-10-09。入口：`http://127.0.0.1:18900`（独立工作台）；运行协议另见本地 `http://127.0.0.1:18903`。账号：本地单用户工作台；未使用 HRSS 账号。

本报告验收 VSJin AI 独立应用本轮实现。工作台不包含 HRSS 账号、业务数据或权限系统。HRSS 本轮已接入并完成本地验收；快速图片、快速文本、专家技能目录和真实授权知识检索均通过：[查看 HRSS 本轮验收记录](/Users/yangyin/myself/hrss/docs/acceptance/2026-10-09-einoloop-runtime-and-fast-images/README.md)。

## 用户需求

| 需求 | 完成 |
| --- | --- |
| 将 EinoLoop 作为独立 Go 应用运行，不依赖 HRSS 模块、数据库或原 examples 本地路径 | ✅ |
| 展示本轮授权 Skill；未配置业务 HTTP 服务时如实说明无法查询真实企业资料 | ✅ |
| 使用真实模型 HTTP 请求完成技能读取、DWG 查询和有证据的回答 | ✅（DWG 内容为虚构 fixture） |
| 对现场安全问题整理草稿、列出缺失信息，并明确未提交业务系统 | ✅ |
| 回答工程计算问题，并说明假设与现场核查边界 | ✅ |
| 验证工作台、会话历史、移动端布局、取消和运行协议鉴权 | ✅ |
| 保留 CloudWeGo Eino 示例代码的许可证、版权和来源记录 | ✅ |
| 完成 HRSS 页面接入、权限绑定及真实业务端到端验收 | ✅（详见 HRSS 本轮报告） |

## 结论

**本地闭环通过。** VSJin AI 独立应用完成了工作台、真实模型 HTTP 对话、授权 Skill 调用、会话回读、取消、移动端和运行协议鉴权的本轮验收；DWG 查询使用的是明确标记为虚构的 fixture，不能视为生产 DWG 验收。HRSS 真实权限、Skill/HTTP 检索和最终答复另由上述联调报告覆盖，生产环境与 CLI 上游本轮未部署/实测。

## 场景材料

| 文件 | 用途 | 文字证据 |
| --- | --- | --- |
| ![原始静态页面](assets/00-before-static.png) | 仅作原始页面状态参考 | 截图是未选会话的静态 Chat With Eino 页面；没有原模型运行、原模型回答或端到端基线证据，不能据此比较模型行为。 |
| ![独立应用运行界面](assets/01-workbench.png) | VSJin AI 工作台 | 页面品牌为 VSJin AI，右上显示“服务已连接”，左侧显示本地会话列表。 |
| ![授权技能目录](assets/02-skill-catalog.png) | 技能清单和无业务服务时的能力边界 | 回答列出 `drawing-analysis`、`scai-document-rag`、`site-issue-reporting`，并说明未配置业务 HTTP 服务时不能查询真实企业文件。 |
| ![DWG fixture 查询](assets/03-drawing-http.png) | 检索、列页、读取完整页并给出证据回答 | 对话显示来源检索、`cad.list_sheets`、`cad.get_sheet` 及 `MC-FL-01` 的读取结果；页面明确提示 `testFixture: true`。 |
| ![现场问题整理](assets/04-site-report.png) | 现场问题草稿和未提交说明 | 回答列出待补信息，并明确当前没有现场问题上报接口、尚未提交业务系统。 |
| ![工程计算与核查](assets/05-engineering.png) | 条件计算及图纸现场核查 | 回答区分筏板顶面和底面假设，并列出标高基准、垫层、防水、局部降板、测量基准和图纸版本核对项。 |
| ![移动端页面](assets/06-mobile.png) | 390px 移动视口下查看长回答和工具结果 | 截图中工具 JSON 与正文在窄屏纵向排布；browser replay 记录 `mobileOverflow: false`。 |
| ![取消运行](assets/07-cancel.png) | 模型生成中的显式取消 | 页面显示“已停止本次生成。”，与 browser replay 的 `cancel: true` 一致。 |
| ![运行协议配置状态](assets/08-runtime-auth.png) | 运行模式能力配置提示 | 页面显示“部分能力未配置”；browser replay 的 `auth: true` 记录协议认证场景完成。截图不包含令牌值。 |

## 逐张判定

| 截图 | 闭环标准 | 判定 |
| --- | --- | --- |
| ![工作台](assets/01-workbench.png) | 本地入口加载 VSJin AI 工作台；页面明确显示服务已连接。 | PASS |
| ![技能目录](assets/02-skill-catalog.png) | 三个授权 Skill 均有名称和用途；回答明确表示未配置业务 HTTP 时无法检索企业资料。 | PASS |
| ![DWG 查询](assets/03-drawing-http.png) | 实际调用来源检索、列出 `MC-FL-01`、读取完整页；回答适用标高为 45.000m 及以上，并拒绝推断其他标高无需防雷。来源明确标记为虚构 `testFixture`，本项只验收工具编排与证据边界，不代表生产 DWG 验收。 | PASS |
| ![现场问题整理](assets/04-site-report.png) | 草稿包含事实、建议和缺失信息，并明确当前只整理对话文本，没有创建或提交业务记录。 | PASS |
| ![工程问答](assets/05-engineering.png) | 在用户给定“−19.850m 是筏板顶面”的假设下计算底面 −21.050m，并说明需核对原图标高对象；没有读取真实工程图纸。 | PASS |
| ![移动端](assets/06-mobile.png) | 390px 视口下页面可读，browser replay 记录 `mobileOverflow: false`。 | PASS |
| ![取消](assets/07-cancel.png) | 用户中止后页面呈现已停止状态；运行协议发送明确的 `run.cancelled` 终态。 | PASS |
| ![运行协议](assets/08-runtime-auth.png) | 运行配置页面提示部分能力未配置；独立协议鉴权 replay 成功。本项不覆盖 HRSS 用户/项目权限接入。 | PASS |

`00-before-static.png` 是静态旧页面截图，没有原模型运行或回答。它只作为场景材料，不作为模型前后对比的通过证据。

## 机器侧对照（无截图）

| 项 | 结果 |
| --- | --- |
| 真实模型对话 | 最新代码下已重跑真实模型 3-case，`3/3 completed`。`.runtime/cases.json` 记录工程问答 `8.401s`、DWG fixture 查询 `20.457s`、现场问题整理 `23.845s`；三条均 `completed: true`、`errors: []`、`historyReadable: true`。DWG 会话还记录 `skillRead: true`、`httpCall: true`。模型凭据和模型标识不在本报告记录。 |
| Skill 目录 | 最新 `.runtime/catalog.json` 记录 `complete: true`，并列出三个 Skill；未配置业务 HTTP 服务时明确回答无法查询真实企业文件。 |
| Go 测试 | `.verification-final.log` 记录 `go test ./...` 各包通过，包括 `runtimewire`、`server`、`skills` 和 `static`。 |
| Go 静态检查 | `.vet-final.log` 为空，没有 vet 诊断输出。 |
| 最新构建 | 本轮最新代码已重编译后运行三条真实模型用例；最终归档未包含单独 `go build` 命令的 stdout。 |
| HTTP 信封与认证头 | `skills/tools.go` 严格要求响应恰含 `code`、`data`、`msg` 三字段并检查类型；`skills/skills_test.go` 覆盖成功、业务失败、字段类型错误、缺失信封，以及令牌通过 `X-Token` 发送且不放入 `Authorization`。HTTP 令牌值不记录。 |
| DWG 工具 HTTP | 会话记录按顺序调用来源检索、`cad.list_sheets` 和 `cad.get_sheet`；来源为 `fixture-dwg-001`，各结果均声明 `testFixture: true`。这是实际 HTTP 工具调用，但后端资料是验收虚构夹具，不是真实生产 DWG。 |
| 运行协议 | `docs/design/einoloop-wire-v1.md` 定义 `/v1/runs`、响应回传和取消接口；browser replay 的 `auth: true` 及服务启动路由日志佐证独立运行协议鉴权检查。HRSS 端到端适配尚未验收。 |
| 来源与许可证 | `LICENSE`、`THIRD_PARTY_NOTICES.md` 和 `docs/provenance.json` 保留 Apache-2.0 许可证、CloudWeGo Authors 版权及固定上游来源信息。 |

## 过程中修过的问题

1. **工具前文被误当最终答复，空白正文也可能进入完成路径。** 现在工具调用前文不计作最终正文，缺少非空最终答复时不能报告成功。回归：`a2ui/final_response_test.go` 的 `TestToolPreambleIsNotFinalResponse`、`TestPartialModelStreamErrorIsNotSuccess`；`server/runtime_test.go` 的 `TestEmptyFinalResponseFailsTurn`、`TestSSETerminalRequiresFinalReplyAndPersistence`。
2. **取消缺少明确的运行终态。** Agent 收到取消后现在发送 `run.cancelled`。回归：`server/runtime_test.go` 的 `TestAgentCancellationHasCancelledTerminal`。
3. **首轮授权目录和工具暴露需要收紧。** 首轮上下文包含授权的 3 个 Skill，Agent 仅注册 `skills_read` 与 `skill_http_request`，不暴露 Shell。回归：`agent_test.go` 的 `TestExpertCatalogToolAndFinalReply`。
4. **长会话标题可能撑宽侧栏。** 侧栏加入 `min-width: 0`，会话标题按容器截断；移动端回放检查 `mobileOverflow: false`。证据：`static/index.html` 的 `.sidebar` / `.session-select` 样式与 `.runtime/browser-replay.json`。
5. **HTTP 200 但业务 `code` 非 0 时不能视为成功。** 现在严格校验 `code/data/msg` 信封，并对业务失败或格式错误作拒绝处理。回归：`skills/skills_test.go` 的 `TestHTTPRequestUnwrapsHRSSResponseAndMasksBusinessFailure`，覆盖 HTTP 成功状态下的非零业务码、错误类型和缺失信封。
6. **GET 工具参数没有正确编码为查询参数。** 已将通过 schema 校验的请求参数编码到 URL query。回归：`skills/skills_test.go` 的 `TestGETHTTPRequestEncodesValidatedBodyAsQuery`，检查含空格和斜杠的查询值及整数参数。

上述 Go 回归测试包含在 `.verification-final.log` 记录的 `go test ./...` 全包结果中；移动端溢出证据来自 browser replay。

## 未闭环项与限制

- DWG 场景仅验证工具编排和证据约束，资料为虚构 fixture；没有真实生产 DWG、真实项目授权或现场核验，不能作为工程判断依据。
- 现场问题只生成对话文字草稿；没有接入 HRSS、没有创建整改/隐患记录，也没有提交动作。
- 筏板标高场景为用户给定数值的条件计算，没有读取实际图纸。
- HRSS 页面适配、用户/项目/会话权限绑定、HRSS 模型调用与业务 HTTP 工具闭环仍由主 Agent 处理，状态待 HRSS 最终验收报告；本报告不宣称 HRSS 端到端通过。
- 最新会话耗时以 `.runtime/cases.json` 为准；旧一轮耗时不作为本报告结果。最终归档有 Go 测试和 vet 日志，未保存单独 `go build` 命令的 stdout。

## 测试数据与资产

- 本轮测试会话及测试数据清理待主 Agent 处理，并由其在整合验收时确认。
- 截图位于被 `.gitignore` 忽略的 `docs/acceptance/2026-10-09-einoloop-foundation/assets/`，当前保留在本地 ignored folder，不纳入 Git。
