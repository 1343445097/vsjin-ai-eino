# VSJin AI EinoLoop 独立应用实施计划

**Goal:** 将用户本地 ChatWithEino 代码改造成独立可构建、具有自有品牌和 Skill HTTP 能力的专家 Agent；本轮同步改造 HRSS adapter 并在本机验收。
**Architecture:** Eino ADK DeepAgent + TurnLoop，独立 Go module。VSJin AI 拥有应用配置、品牌界面、受控技能目录和 HTTP 工具，用户业务与权限仍由HRSS 等调用方业务系统负责。
**Tech Stack:** Go 1.24.9+、Eino v0.9.21、Hertz、嵌入式原生 HTML/CSS/JS。
**Spec:** docs/design/hrss-runtime-boundary.md；用户追加授权后，HRSS adapter 与后台引擎选择并行开发、本地验收；生产发布不在本轮范围。

## 约束

- 来源为用户指定本地 checkout；保留 Apache 许可证、原版权和来源记录，界面/提示词用自有品牌，不冒称从零实现框架。
- 不导入 HRSS Go 包、数据库或 MCP；不依赖原 examples 仓库的 replace。
- 默认 localhost:18900、Chat Completions message；可显式选择 agentic Responses。
- 业务仅 skills_read + skill_http_request；HTTP 接口只按声明清单调用。无业务服务配置时如实说明不可执行。
- 不默认授予 Shell、主机文件系统、通用子 Agent；不保留“学习 Eino、读源码”的系统指令。
- 会话本地持久化、可取消；读取/工具完成后必须形成非空最终答复；空输出不得显示完成。
- 用户追加授权后并行适配 HRSS 与修复快速图片，允许本机18889部署；生产保持不变。测试密钥/客户资料不入 Git。

## Task 1: 独立构建和配置（主 Agent）

Files: go.mod、main.go、agent.go、internal/checkpoint、internal/approval、chatmodel、msgops、Dockerfile、Makefile、README.md。

- [x] 复制源码、记录哈希、排除 data/.tmux-ai、保留 LICENSE 与来源。
- [x] 将模块改为 github.com/vsjin/vsjin-ai，内部 approval/checkpoint 本地化，删除教学章节和评分 RAG。
- [x] 为配置缺失/非法监听、默认消息格式与 Agent prompt 编写失败测试。
- [x] 实现 VSJIN_* 配置、独立启动、受控 DeepAgent、自有身份和可解释错误。
- [x] go test ./chatmodel ./msgops ./internal/...；go build ./...。

## Task 2: Skill HTTP（并行 worker）

Files: skills/*.go、policy/skills/*。
Consumes: Eino tool.BaseTool，标准 HTTP 服务；不依赖主项目其他包。
Produces: Load(root,groups) (Catalog,error)、Catalog.Prompt/Read、NewTools(Catalog,ClientConfig) []tool.BaseTool。

- [x] 先以 httptest 验证目录授权、未声明路径拒绝、令牌隔离、响应上限和真实 HTTP 返回。
- [x] 实现名称描述首轮注入、正文按需加载、方法/路径白名单、固定 base URL、令牌 server-only、取消/超时。
- [x] 提供资料、图纸、现场问题的技能包，声明当前契约；服务未配置时明确不可执行。
- [x] go test ./skills。

## Task 3: 品牌化交互界面（并行 worker）

Files: static/index.html、static/embed.go、static/web_test.go。
Consumes: 既有 /sessions、/sessions/:id/chat/render/abort/approve；新增 /v1/runtime 返回 product/runtime/skills/configured。
Produces: 可嵌入 HTML，保留 A2UI 流式事件消费。

- [x] 改为中文 VSJin AI 工作台，去掉教学入口与开源演示品牌；会话/最终回复/过程阅读清晰。
- [x] 不用外网 CDN，禁止直接注入模型 HTML，脚本错误可见、发送/中止正常、移动端可用。
- [x] 真正浏览器发送与截屏（集成阶段主 Agent 完成）。

## Task 4: 独立 HTTP 服务与验收（主 Agent）

Files: server/*、mem/*、a2ui/*、docs/acceptance/*。

- [x] 测试 localhost 默认、认证/跨源、路径穿越、空输出不完成、上下文无教学指令。
- [x] 接入嵌入页面、healthz/runtime、配置化监听，关闭无工具支持的文件上传。
- [x] 保留 TurnLoop 取消与多轮，补上有限运行预算和确定终态。
- [x] 全量 gofmt/vet/test/build（适合并发包补 race），真实模型技能查询、工程问答与工具 HTTP 验证。
- [x] 产出截图报告、独立启动18900并合回本地develop。无remote不虚构PR；任务worktree由最终收尾命令清理。
