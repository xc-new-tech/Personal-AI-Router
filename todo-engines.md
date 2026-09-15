# 扩展 PAIR 支持 oMLX / vLLM / SGLang

目标：让 PAIR 把 oMLX（Mac）、vLLM 和 SGLang（DGX）识别为一等引擎，可发现、可路由、可在 UI 管理。

## 成本修正

先前估算「每个引擎需要 ~5000 行 proxy」**是高估的**。读代码后的实际情况：

| 层面 | 实际成本 | 依据 |
| --- | --- | --- |
| 生命周期 / 健康探针 / 安装卸载 | **声明式 manifest，零 Go 代码** | `registry.go` 的 `Runtime`、`Probe`、`StopSpec` |
| 模型清单 | **声明式，零 Go 代码** | `ActionResult` 注释明确「no per-engine code in the runner」 |
| pull / delete / load 动作 | **声明式** | `Action` 支持 HTTP / Cmd / RemovePath |
| 协议代理 | **三引擎共用一个**，不是三个 | 三者均 OpenAI 兼容 |
| 调度器 / 前端常量 / 构建脚本 | 少量清单式改动 | 见下方待办 |

两个现有 proxy 除 `proxy.go` 外的 7 个文件（各约 570 行），去掉引擎名后差异仅 **0–29 行** —— 是复制粘贴的兄弟，不是两套独立实现。

## 关键机制：adopt（接管已运行引擎）

`lifecycle.go` 的 `st.adopted` 与 `isManagedInstallPath()` 表明 PAIR 已支持：

- **识别并使用**不是自己启动的引擎（靠 `Ready`/`Health` 探针，而非进程句柄）
- **拒绝停止**外部管理的引擎，报 `running under external management`

这对本项目是决定性的：DGX 上的 vLLM/SGLang 由 supervisord/docker 以特定参数拉起
（`--mem-fraction-static`、投机解码、docker mount 等），**绝不能让 PAIR 接管启停**。
因此三个新引擎一律走 **adopt-only**：manifest 只声明 detect + 健康探针 + 模型清单，
不声明 install，不声明 start。

## 设计决策

1. **adopt-only，不托管生命周期** —— PAIR 只发现、探活、路由，不安装不启停。
   理由见上。UI 上这三个引擎的 Install/Start/Stop 应置灰或隐藏。
2. **一个通用 `openai-proxy`，三份 manifest** —— 三引擎共享 OpenAI 协议表面
   （`/v1/models`、`/v1/chat/completions`），从 `lmstudio-proxy` 派生，按引擎名 + 端口参数化。
   待确认：modular-supervisor 是否支持同一二进制以不同参数启动多个实例（见待办 2）。
3. **模型清单零代码** —— OpenAI 的 `{"data":[{"id":...}]}` 正好对应
   `ActionResult{Array:"data", Field:"id"}`，三个引擎复用同一份声明。
4. **端口** —— oMLX `:8123`、vLLM `:8000`、SGLang `:30000`（均为各自惯例，实测确认）。

## 待办

### 阶段 0 — 摸清剩余耦合点 ✅ 完成

- [x] 1. `ui-broker` 的 per-engine 文件承担**固定端口拓扑所有权**
       （`managedLMStudioFacadePort=1234`/`backendStart=1235`，ollama 同理）
- [x] 2. **「一个通用 proxy 多实例」不成立**。每个 proxy 是独立二进制 + 独立 relay
       命名空间（`proxy:` / `lmstudio-proxy:`）+ 独立端口拓扑，由
       `proxyEngineFromManagerId()` 与 `proxyRelayPrefix()` 硬编码映射。
       → 正确方案：抽共享 Go 包 + 三个薄二进制
- [x] 3. **adopt-only 在原 schema 下不可表达**。校验强制 `runtime.bin`（process）
       或 `runtime.start`（command），二者必居其一。
       → 已新增第三种模式 `external` 解决

### 阶段 1 — oMLX ◐ engine-manager 层完成，上层未做

- [x] 4. `manifests/omlx.json`（external 模式、port 8123、`/v1/models` 探针）
- [x] 5. **API key 需要 Go 改动**（`ActionHTTP`/`Probe` 原无 header 字段）。
       已加 `headers` 字段 + `{api_key}` 占位符，从 `NVPAIR_<ENGINE>_API_KEY`
       在加载时解析，仓库内无明文
- [x] 6. `schedule.go:17` 的 `schedulerEngines` 加入三个新引擎
- [x] 7. 前端常量 + `EngineCapabilities` + `welcome`，外部引擎的安装/启停能力全部关闭
- [ ] 8. `openai-proxy` —— **未做。见下方「proxy 的真实成本」，比原估算大一圈**
- [x] 9a. PAIR 实跑：五引擎在列，oMLX `installed/running/healthy`，模型进入发现层
- [ ] 9b. 路由一次真实推理 —— **未做，取决于第 8 项**

### 阶段 2 — vLLM / SGLang ◐ manifest 完成并部分实证

- [x] 10. `manifests/vllm.json`、`manifests/sglang.json`（结构复用阶段 1，零新增 Go 代码）
- [x] 11. **vLLM 已实证**：隧道映射 spark-10:18085 的真实 vLLM 到本机 :8000，
       adopt 成功、模型清单正确返回 `IndexTeam/IndexTTS-2.5`。
       过程中发现并修复 `reconcilePresence` 对无 detect 引擎的阻塞。
- [ ] 11b. **SGLang 未实证** —— spark-10 上的 SGLang 卡死三小时、`:30000` 从未监听，
       无可用实例。其 manifest 与已验证的 vLLM 同构，但这是推断而非实证。

## 当前进度小结

**已完成（engine-manager 层，两个 commit）**

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| `external` 生命周期模式 | ✅ | 6 个单测；拒绝自相矛盾 manifest、强制探针 |
| manifest 认证头 + 密钥环境变量注入 | ✅ | 单测覆盖解析与缺失丢弃；仓库无明文 |
| oMLX 接管 | ✅ 实证 | `adopting already-running external engine`，4 个真实模型 |
| vLLM 接管 | ✅ 实证 | 经隧道对真实 vLLM，返回真实模型 |
| SGLang 接管 | ◐ 未实证 | manifest 同构，无可用实例 |
| 多引擎聚合 | ✅ 实证 | `engine:models` 同时返回 omlx + vllm |

| 调度器白名单 | ✅ | 三引擎纳入同一份节点排名 |
| 前端可见 + 能力开关 | ✅ 实证 | PAIR 实跑：五引擎在列，oMLX healthy |
| broker 的 OpenAI 信封解析 | ✅ 实证 | 实跑发现并修复，失败计数归零 |

**实跑发现的 bug**：模型清单有**两个**解析器。engine-manager 走 manifest 的声明式
`result` 抽取（已验证），但 broker 拿到的是引擎原始响应，由 `parseListModelNames`
自己解析，而它只认 `{models:[{name|key}]}`。oMLX 因此报
"list_models response is missing its model array"，尽管四个模型就在响应里。已修。
**教训**：声明式抽取只覆盖了一条路径，另一条是手写的 —— 纸面推演发现不了，必须实跑。

## proxy 的真实成本（比原估算大一圈）

原判断是「抽共享包 + 三个薄二进制」。实际读代码后，漏掉了**发现层**：

- 服务键是固定枚举（`ol`/`lm`/`ni`/`em`/`ec`…），且通过**有大小限制的 mDNS TXT
  记录**广播。对端要知道某引擎的端口，该引擎就得有自己的服务键。
- 因此还需要：3 个新服务键 + advertiser 广播 + broker 的引擎端口登记 +
  按「节点 × 引擎」建候选（同一节点上 oMLX:8123 与 vLLM:8000 是不同后端，
  不能共用一个候选）+ `ui-broker` 的端口拓扑 + relay 命名空间映射。

好消息是 proxy 本体几乎与引擎无关：`lmstudio-proxy` 里引擎特异的只有三处
（`defaultProxyPort=1234`、`workloadEngine="lmstudio"`、`EngineModels("lmstudio")`），
且 `workloadEngine` 仅用于 workload 归属元数据。

**建议的设计**：三个引擎同说 OpenAI，应共用**一个** facade（一个 `openai-proxy`、
一个 relay 前缀、一个端口），按模型名路由到持有该模型的节点与引擎 —— 而不是三个
各自的 facade。这与「不接受跨量化路由」的决定天然吻合：模型名已按引擎/量化区分，
按名路由自然落到正确的引擎。`noderec.EnginesModels` 已为此铺好路。

**当前可用性**：PAIR 能发现、探活、列举三个引擎的模型，UI 能看到它们。
**但还不能把推理请求路由给它们** —— 那需要第 8 项。

### 阶段 3 — 集群落地（需单独确认后再做）

- [ ] 12. 在 DGX 上部署 PAIR 并与 Mac 组集群

## 风险与前置

1. **阶段 3 要在两台 DGX 上装 PAIR** —— 这两台跑着生产 TTS。
   PAIR 会常驻多个服务进程并占用发现端口 14318。**这是对生产机的侵入，需你单独批准。**
   阶段 1、2 不触碰 DGX，只在 Mac 上做。
2. **oMLX 的 API key** 是第一个可能突破「纯 manifest」的点（待办 5）。
   若 manifest 无法声明认证头，需改 `ActionHTTP` —— 那是通用改动，三引擎受益。
3. **adopt-only manifest 是未验证组合**（待办 3）。若 PAIR 要求 install 段存在，
   需要改 registry 校验逻辑。
4. **上游无 .git** —— 本仓库经 tarball 获取，改动无法 `git pull` 合并。
   建议先 `git init` 建立基线，否则改动与未来上游版本无法区分。
5. 三个引擎的 `/v1/models` 返回的是**已加载**还是**可用**模型，各框架语义不同，
   可能影响 PAIR「优先路由到已持有模型的节点」的调度假设。

## 不做的事

- 不托管三个新引擎的安装与启停（adopt-only）
- 不改动上游已有的 ollama / lmstudio 路径
- 阶段 1、2 不碰 DGX 生产机
