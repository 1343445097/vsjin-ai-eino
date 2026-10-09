---
name: drawing-analysis
description: 在授权企业图纸中检索单页、定位原文并读取该页证据。
groupKey: knowledge.cad
---

# 图纸分析

先调用 `skill_http_request({method:"POST",path:"/knowledge-base/skill-tools/knowledge.search",body:{query:"图号、图名或图纸关键词"}})`，从真实命中获取 `sourceId`。再调用 `skill_http_request({method:"POST",path:"/knowledge-base/skill-tools/cad.list_sheets",body:{sourceId:"真实来源ID"}})` 列出文件页面，并按用户给出的图号、图名或部位匹配候选。需要查找页内连续原文时调用 `/knowledge-base/skill-tools/cad.search_text`；需要按问题查找视图时调用 `/knowledge-base/skill-tools/cad.find_views`。每次请求均通过 `skill_http_request({method:"POST",path,body})` 发出。

只有候选明确后，才把 `cad.list_sheets` 返回的 `sourceId`、`viewId`、`fileVersionId`、`publicationId` 和 `chunkSetId` 原样传给 `/knowledge-base/skill-tools/cad.get_sheet`。这些版本 pin 必须来自目录结果；首次读取前不得调用 `cad.get_sheet` 获取 pin，也不得替换成其他版本。回答人员、单位、尺寸或构件时以工具返回原文和质量限制为依据；无法确认时明确保留不确定性。

本包只声明图纸读取操作，不创建图纸关联、不发布或修改文件，也不提供旧查看器链接。默认启用 `knowledge.base` 和 `knowledge.cad`；只有 Prompt 列出的实际 method/path 可调用。运行时绑定项目范围，模型不可自行指定项目、身份或凭证。
