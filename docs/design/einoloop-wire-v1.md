# VSJin EinoLoop Wire v1 (implementation contract)

One trusted backend connection; no business/model credentials in model prompts or the independent engine. HRSS remains owner of identity, model invocation, tool authorization, chat history and audit. Fast path unchanged. CLI retained.

Configuration: HRSS `HRSS_EINO_LOOP_BASE_URL` and `HRSS_EINO_LOOP_API_TOKEN`; VSJin `VSJIN_AI_API_TOKEN` must match. Explicit Eino expert binding must fail clearly if not configured; do not silently change engine.

POST `/v1/runs`, Authorization Bearer service token, Content-Type application/json. Body:
```
{"version":"1","runId":"uuid","runToken":"random-secret-per-run","messages":[{"role":"system","content":"authorized system and skill catalog"},{"role":"user","content":"..."}],"tools":[{"type":"function","function":{"name":"skills_read","description":"...","parameters":{"type":"object",...}}}],"maxIterations":12,"timeoutSeconds":180,"temperature":0.2}
```
`messages` is standard LLM message JSON (role/content/name/tool_calls/tool_call_id; tool_calls OpenAI-style id/type/function{name,arguments}). First version text only; reject unsupported multimodal rather than silently drop images.

Response SSE each `data: <json>`. Every event has `version:"1",type,runId,sequence`.
- `run.started`: new runtimes may advertise `features:["model.delta"]`. HRSS streams model output only after this capability is present; older runtimes that omit it continue to receive one complete model response.
- `model.request`: requestId plus `messages` and `tools` arrays. HRSS calls existing authorized LLMClient, audits input/output and posts response below. No API key/address in event.
- `tool.request`: requestId plus `call:{id,type:"function",function:{name,arguments}}`. HRSS invokes EXISTING per-run tool invoker; runtime must only accept declared tools. Tool may be skills_read or skill_http_request; schema and tool scope remain HRSS-owned.
- `message.delta`: `delta` string. A capable runtime may emit multiple ordered chunks while the final HRSS model request is still running. `run.completed.final` must equal their concatenation. If no chunks were sent, HRSS emits the complete final answer as one compatibility delta.
- `run.completed`: `final` string, MUST nonempty final assistant after all tools.
- `run.failed`: `error` sanitized string.
- `run.cancelled`.

POST `/v1/runs/:runId/responses` Authorization service token AND `X-VSJin-Run-Token: runToken`. Body `{"requestId":"...","message":<standard assistant message>}` for model, `{"requestId":"...","delta":"visible answer chunk"}` for a model delta, `{"requestId":"...","result":"tool result"}` for tool or `{"requestId":"...","error":"sanitized failure"}`. Deltas are nonempty, apply only to the matching pending model request, retain its request ID, and do not complete it; HRSS follows them with exactly one complete assistant message. Exactly one final reply is accepted; repeat/unknown replies rejected 409. 204 success. HRSS reads the runtime SSE concurrently with the model callback so deltas reach the session before model completion. Tool-enabled model rounds remain whole-response callbacks. POST cancel `/v1/runs/:runId/cancel`, same two tokens; HTTP context cancellation also cancels the run. No arbitrary callback URL/SSRF. Ended run discarded, duplicate active runId returns409. Request<=4MiB, response<=4MiB, whole run bounded.

Runtime interface on HRSS side can match existing Run(ctx, []*schema.Message, emit AgentEvent)(*LLMResponse,error), SetAuditRecorder, SetForceFirstTool, SetReturnDirectlyTools if needed. External engine remains stateless; same authorized tools/history supplied each run. Service-token binding required even on localhost for `/v1/runs*`.

`maxIterations` bounds model-generation rounds, including the final answer. The last allowed round exposes no tools and asks the model to summarize available evidence and limitations. If a tools-enabled decision round returns content without tool calls, runtime spends the reserved next round on a tool-free final answer so ordinary no-tool answers can use real streaming safely. A one-round budget is answer-only. Business-tool errors return structured `isError:true` result observations while retaining failed audit artifacts; undeclared tools, invalid protocol, model failures and cancellation remain terminal failures. No error is converted into fabricated successful evidence.

Streaming answer normalization trims leading/trailing whitespace incrementally and removes an explicit leading, closed `<think>...</think>` envelope (or a leading orphan closer followed by a Markdown heading). An unclosed leading thinking envelope produces no visible answer and fails. Model reasoning belongs in the separate reasoning field or this explicit envelope. Literal markers in the visible body are preserved; a streaming reply cannot retract previously emitted text using the whole-response legacy heuristic. Older whole-response callbacks retain that heuristic. The streamed concatenation and final answer must agree exactly.

Final answer instructions end the model history in a user message, asking for the formal reply to the original question. Ending with an added system message after an assistant draft can produce a successful HTTP stream with zero answer tokens on some compatible providers. This internal generation request is not persisted as a new business-user turn.
