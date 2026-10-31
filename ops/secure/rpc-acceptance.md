# 主网 RPC 现场验收矩阵

## 2026-10-10 后续：BSC 预算优化的代码验证

可选 bsc_header_https 已实现，补扫区块头与日志分源，双方 chain ID / 日志 hash / checkpoint 检查不变；BSC 小批次最多等待 5 秒并按期处理剩余区块。默认测试 / vet / 关键 race 通过，RN 使用新 Go 客户端的只读探针通过真实区块头和日志 hash 核验，临时探针已删。优化后基准约 36.288M Alchemy CU/30 天，单账号免费 30M 仍不足；两账号手动切换、实际配额与长期运行验收仍需落实。以下 82.368M 是优化前基准，不是当前分源配置的预算。

## 2026-10-10 RN 现场补充

44 次 HTTPS 请求均成功：三套 BSC 来源的 chain ID 56 / finalized / 真实 USDT 交易与回执事件 / 重读 / 未找到语义通过；TronGrid 与 Chainstack 的固化区块 / USDT 交易与回执一致，bulk receipt 可用。Alchemy 主 / 备 WSS 各两轮 TLS、订阅、取消、再次连接通过。两链主网锚点已从独立运营方交叉核对并填入私有草稿。未运行应用真实资金证明或连续扫描，不等于故障恢复 / 实付通过。

BSC 样本约 0.45 秒/块。当前代码四次区块头 ×20 CU + getLogs 60 CU，约 140 CU/推进批次，10 块一批；加无事件 WSS 统计头，基准约 82.368M CU/30 天，尚不含订单 / 失败重试。官网免费 30M CU/账号/月，两个账号也不足，真实账户额度 / 用量未确认。当前先解决预算，不能在部署时偷改 checkpoint / finalized / 双源要求；详细测算在工作区 RPC_BUDGET.md（本地交接，不在网关 Git 内）。

本地只验证合成响应，不能以接口文档或账号存在代替真实接口验收。Key / 完整凭据 URL 留私有配置；记录提供方、套餐、日期、脱敏方法 / 状态、延迟、预算，不记录完整响应中的客户资料。

| 消费方 | 接口 | 证据和拒绝规则 |
| --- | --- | --- |
| 网关 TRON | `/walletsolidity/getnowblock`、`/walletsolidity/getblockbynum`；`/wallet/gettransactioninfobyblocknum` | 持久固化高度 / hash、全区块回执及每条真实 Transfer；缺回执、旧断点变化不推进 |
| 商城 TRON，每个来源 | `/walletsolidity/getblockbynum`、`gettransactionbyid`、`gettransactioninfobyid`、`getnowblock` | 固定主网锚点、成功交易 / 回执、真实 USDT Transfer、6 decimals、规范区块和固化头，重读规范块；两家均须支持这些接口 |
| 网关 BSC HTTPS | `eth_chainId`、`eth_getBlockByNumber`（含 finalized）、`eth_getLogs` | chain ID 56、持久 checkpoint/hash，补扫每批最多 10 块；失败不跳过，finalized 变化停止调查 |
| 网关可选 BSC header HTTPS | `eth_chainId`、`eth_getBlockByNumber`（latest / finalized / 规范块 / checkpoint） | 设置 `bsc_header_https` 后补扫区块头走独立节点，日志仍走原 HTTPS；两方链身份都校验，日志 hash 须匹配规范头，不作自动回退 |
| 网关 BSC WSS | `eth_subscribe` logs、区块头查询 | 实时发现提示；断线后靠 HTTPS 补扫追平，WSS 事件不直接结算 |
| 商城 BSC，每个来源 | `eth_chainId`、`eth_getTransactionByHash`、`eth_getTransactionReceipt`、`eth_getBlockByNumber`（锚点 / 入块 / finalized / 再读） | chain ID 56、Binance-Peg USDT 固定合约 / 18 decimals、status=1、精确事件 / amount / recipient；不能用 latest 或固定确认数代替 finalized |

两来源必须是实际不同运营方，URL / provider 标签不同只是配置校验，不能证明独立。现有 TronGrid 免费账号用于一个 TRON 来源；**第二个 TRON 独立来源仍缺现场确认**。Alchemy BNB HTTPS / WSS 属同一来源；BSC 第二家同样要验证 finalized / 历史回执，不能只测 chainId 就算通过。主网锚点高度 / hash 须通过独立公开证据确认，不从不可信网关复制。

| 预算 / 停止条件 | 当前行为与现场工作 |
| --- | --- |
| 商城 RPC | 每提供方进程内限速 2 次/秒；一次核验两来源，结算前再核验。按真实调用数与服务商 CU 计费，不把 2 QPS 当成日预算控制 |
| TRON 扫描 | 3 秒轮询固化头、检查历史断点、逐块取完整回执；当前代码会复用本轮返回的最新区块。按 3 秒出块、每轮推进一块估算约 8.64 万次/日，补扫 / 额外区块读取 / 重试会增加，可能超过 10 万次。全天监听即使零订单也消耗额度，不能假定免费额度足够 |
| BSC 补扫 | 5 秒调度、10 块查询跨度；停机追平比稳态消耗高，WSS 重连与 getLogs 也可能按 CU 计费。记录 24 小时预估及最长可接受追平时间 |
| 新增付款门禁 | 网关差距 >20 块、头时间落后 >2 分钟、轮询 >30 秒时拒绝创建；商城核验任一来源失败 / 分歧不结算，持久退避 / 人工状态 |
| 告警 | 商城持久扫描健康观测 / scanner_unhealthy 告警及 RPC 积压告警；同机宕机无法保证发出，外部探针尚未配置 |
| 额度不足 / 429 | 先停止新 U 支付、保留扫描断点和候选，补额度或更换经过验证的提供方。没有自动付费购买或自动降到单源；恢复前确认追平 |

免费套餐是否够用是当前实际部署门槛。若 TRON bulk receipt 权限或预算不足，先换套餐 / 合格来源，或另立任务改为有完整性证明的按地址发现策略；不要通过跳块、只识别 calldata、忽略间接 Transfer 降低成本。这里不承诺免费套餐可长期支撑全区块扫描。

分别记录：TLS / WSS 握手、方法权限、成功 / 失败 / 未找到语义、finalized 或固化滞后、429 / timeout / 断线恢复、同一已知交易的两来源一致性、规范块重查、运行时钟。真实凭据只在私有配置操作，不回显。2026-10-09 用户明确 Alchemy 旧 Key 主用、第二账号 Key 备用，不要求轮换；旧轮换门槛不再适用。当前手动切换，两账号仍只算一家独立运营方。

## 2026-10-09 22:28 CST：本机凭据兼容检查

使用实际私有凭据做 15 次只读请求：Alchemy 主 / 备各自 chainId=56、finalized 区块和 10 块 USDT getLogs 通过；TronGrid / Chainstack 的固化头、同一固化区块、交易和回执查询通过，两源该块 hash 一致；TronGrid 全区块回执接口返回非空列表。没有链上写入、客户订单匹配或真实支付，未打印完整端点 / 交易内容。

这是 Mac 上的短样本，仅证明已测接口当前可用；WSS、RN、主网锚点、双源完整资金证明、故障恢复、真实额度与持续扫描仍未验收。两套 Alchemy 不满足 BSC 第二独立来源。以下较早的服务商表保留当时的公开套餐调查结果，其“未有 endpoint / 未用 Key”状态以本节为准。

## 2026-10-09：服务商与预算准备

后续已落实 BSC 第二来源：`https://bsc-dataseed.bnbchain.org`。Mac 测试以及 RN 于 22:56 CST 的 6 次只读查询均通过 chainId56、finalized、与 Alchemy 相同固定块、交易 / 成功回执及 USDT 事件、固定块重读。已写入商城草稿，未启用付款；PublicNode 本轮 Mac 的 6 个请求仍 HTTP403，不配置。

[BNB Chain 官方 RPC 文档](https://docs.bnbchain.org/bnb-smart-chain/developers/json_rpc/json-rpc-endpoint/) 列出该主网节点及 10K/5min 限额，明确公共主网节点禁用 `eth_getLogs`。因此只用作商城按交易回执核验的第二来源，网关 HTTPS 扫描 / WSS 仍使用 Alchemy。不要把它换成网关扫描源，也不要把同一负载均衡域的多个 URL 算作独立运营方。短时一致性不等于长期可用性、历史保留深度或完整资金证明验收；不承诺免费 SLA。

早期准备阶段的官网资料核对；当前 Chainstack endpoint 已提供并通过上节本机兼容检查。以下套餐信息不能替代账户实际额度，也不自动购买套餐：

| 候选 | 官方公开计划 / 接口 | 本项目用途和限制 |
| --- | --- | --- |
| Chainstack Developer | 免费 3M 请求/月，25 RPS，1 节点；TRON 文档明确列出 `/walletsolidity` 的 getblockbynum / getnowblock / gettransactionbyid / gettransactioninfobyid，以及 `/wallet/gettransactioninfobyblocknum` | 优先验证商城第二 TRON 来源；账户选择 TRON Mainnet，使用基础 HTTPS endpoint。仅 1 个免费节点，不能假设同时免费建 TRON 和 BSC。没有该账号 endpoint，尚未现场验收 |
| GetBlock Free | 官方 pricing.md：每天 50,000 CU、20 RPS、2 access tokens | 备选 TRON 或 BSC 独立来源；CU 不是请求次数。先核对账户能否选择该网络和 native HTTP / finalized 方法，再测实际接口；没有账号，未验收 |
| Allnodes / PublicNode BSC | 无 Key 的公开 BSC endpoint | RN 20:21 CST 实测 chainID56 / finalized / 10 块 USDT logs / 交易 / 固定块重读成功；首次回执 HTTPError，随后补测 getLogs HTTP403。不作为已通过的生产第二来源，也没有据其单源结果配置可信锚点 |
| TronGrid 免费 | 现有账号；本轮读取官方 rate-limits 页面，确认 Key / 日额度 / 频率限制和 429 / 403 语义，但该页未承诺固定数字额度 | 以用户控制台的实际额度为准。未使用私有 Key 发请求；不能把历史常见的 10 万/日当作当前账号已确认配额 |

本次 PublicNode 共 8 次无 Key 的只读请求，只返回检查摘要，未存客户交易详情、未发链上交易。这个短样本只说明部分方法可用且存在访问失败，不能推断故障根因或全天 SLA。

TRON 扫描更准确的调用估算：每天 P 次轮询、N 个新块、Q 次成功轮询可复用头块，则基础约 `2P + N + (N-Q)`；两项 P 分别是固化头和旧 checkpoint 重读。理想稳态 P=N=Q=28,800，得到 86,400 次/日，30 天约 259.2 万次；存在补扫时额外读取新块，另加启动、失败重试和商城查链。此修正收敛了旧估算，没有消除免费额度风险，也没有降低证据要求。实际运行必须记录来源用量与追平余量，不能因为理论刚好低于额度就宣布可用。

商城第二 TRON 来源按订单核验，不做全链扫描。当前一次正常证明约每来源 6 次调用（锚点、交易、回执、入块、固化头、入块重读），结算再次证明约翻倍，重试另计。服务商套餐适合这一用途，不代表可以直接替换网关全天扫描；安全网关当前 `tron_url` 还要求根路径，而商城适配器允许供应商路径型 endpoint。

参考：[Chainstack 价格](https://chainstack.com/pricing/)、[TRON 方法](https://docs.chainstack.com/docs/tron-methods)、[TRON endpoint 说明](https://docs.chainstack.com/docs/tron-tooling)、[GetBlock 价格](https://getblock.io/pricing.md)、[PublicNode](https://www.publicnode.com/)、[TronGrid 限额](https://developers.tron.network/reference/rate-limits)。
