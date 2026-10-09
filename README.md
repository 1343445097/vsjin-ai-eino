# VSJin AI · EinoLoop

VSJin 自有的工程智能助手运行应用。通过授权 Skill 理解资料检索、图纸分析和现场问题整理，按需调用声明的 HTTP 工具，并输出有依据的中文答复。

该应用可作为独立专家工作台，也可作为 HRSS 的外部 EinoLoop 执行引擎。HRSS 快速模式、CLI 专家模式继续运行；独立工作台不包含 HRSS 的账户、业务数据或权限系统。

## 启动

需要 Go 1.24.9 或更新版本，无 Node/CDN 依赖。

```bash
cp .env.example .env
# 配置 .env 中的模型；不要提交真实凭据
set -a
source .env
set +a
make run
```

访问 http://127.0.0.1:18900 。不配置模型会在启动时清晰报错；不会切换到伪造回复。`VSJIN_MESSAGE_KIND=message` 使用 Chat Completions；模型服务只提供 Responses 时使用 `agentic`，两者各有独立会话目录。

## 配置

| 变量 | 默认/作用 |
| --- | --- |
| VSJIN_AI_HOST / VSJIN_AI_PORT | 127.0.0.1 / 18900 |
| VSJIN_AI_API_TOKEN | 可选；监听非回环地址时必须设置，接口使用 Bearer |
| VSJIN_AI_DATA_DIR | ./data；本地 JSONL 会话，不依赖业务数据库 |
| VSJIN_MODEL_PROVIDER | openai（也支持 ark） |
| VSJIN_MODEL_NAME / API_KEY / BASE_URL | 使用 VSJIN_MODEL_ 前缀；模型名称必填，远程服务必须有密钥 |
| VSJIN_MESSAGE_KIND | message 或 agentic |
| VSJIN_AI_MAX_ITERATIONS | 12，上限50 |
| VSJIN_AI_TURN_TIMEOUT_SECONDS | 180，每轮总预算 |
| VSJIN_SKILLS_DIR | 未设置则使用编入二进制的 policy/skills |
| VSJIN_SKILL_GROUPS | knowledge.base,knowledge.cad,scai.reporting；可信配置控制允许的组 |
| VSJIN_SKILL_HTTP_BASE_URL / TOKEN / PROJECT_ID | 固定业务服务、访问令牌、项目范围；不进入提示词 |
| VSJIN_SKILL_HTTP_TOKEN_HEADER | 默认 Authorization（Bearer）；直接连接 HRSS 时设 X-Token，不支持任意认证头 |

兼容原示例的 OPENAI_* / ARK_* 环境变量，VSJIN_* 优先。运行配置只从服务端读取，用户消息不能改变工具主机或身份。

## Skill 与 HTTP

首轮提示词注入已授权技能的名称与描述。模型通过 `skills_read({"skill":"drawing-analysis"})` 按需读取完整说明，再以 `skill_http_request({"method":"POST","path":"/声明路径","body":{...}})` 调用清单中的接口。HTTP 路径与参数由技能合同约束，不开放任意 URL、Shell、主机文件系统或通用子 Agent。无业务接口配置时模型应如实说明无法查询。

内置包覆盖资料检索、CAD 增量查询和现场问题整理。现场上报目前仅整理问题与缺参，不创建业务记录；HRSS 身份和确认流程接入后才开放写入。外部 Skill 目录同样需要 SKILL.md 和 http-tools.json，配置目录由维护者控制。

`/healthz` 提供健康状态；`/v1/runtime` 返回技能目录；`/sessions` 支持会话列表、创建、删除，`/sessions/:id/chat` 使用 SSE/A2UI 流式呈现，另有 render、abort、approve。最终事件区分 completed / failed / cancelled / awaiting_input；正常完成必须有最终回复且已保存会话。

当前服务是单用户工作台：服务令牌授予同一会话库访问权限，不能用来替代 HRSS 多用户权限。下一阶段由 HRSS 适配器绑定每轮授权和模型资源，不直接复制 HRSS 数据库或个人开发技能目录。

## 开发与来源

```bash
make check
make build
```

应用品牌、配置、受控 Skill HTTP、界面与集成逻辑由 VSJin 维护。底层基于 Eino ADK；初始源码来自用户指定的 ChatWithEino checkout。保留 Apache-2.0 许可证与原版权，详见 THIRD_PARTY_NOTICES.md、docs/provenance.json。独立应用不依赖原 examples 仓库的 replace 或本机源码路径。

## 接入其他业务系统

独立引擎提供 `/v1/runs` 协议，见 docs/design/einoloop-wire-v1.md。业务系统传入本轮已授权的消息和工具定义，接收模型/工具回调请求，经自己的模型客户端与工具执行器处理，再回传结果。模型密钥、用户身份与业务权限留在调用方。

只作为运行引擎启动时，设置 `VSJIN_AI_RUNTIME_ONLY=true` 和 `VSJIN_AI_API_TOKEN`，无需独立聊天模型凭据。此时工作台聊天接口返回503；协议接口必须携带服务令牌，回传/取消还必须持有本轮令牌。第一版协议支持文本及 Skill 工具；多模态输入明确拒绝，不静默丢图。流式协议目前在最终答复阶段发送正文，工具和模型请求实时发送。

HRSS 适配并行开发中，配置为 `HRSS_EINO_LOOP_BASE_URL` 和 `HRSS_EINO_LOOP_API_TOKEN`。是否完成 HRSS 页面接入，以 HRSS 的本次验收报告为准，独立应用报告不能代替端到端业务验收。
