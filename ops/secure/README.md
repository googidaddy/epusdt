# 独立 Epusdt 安全运行手册

这是本 fork 给单个商城使用的部署入口。源码和合成环境已验收，RN 尚未部署。此文中的服务器命令供后续已授权的部署阶段执行；占位符必须先替换，不能整段盲跑。

## 运行边界

只构建 `src/cmd/secure`，只接受 USDT-TRC20 / USDT-BEP20、CNY 固定付款意图。顾客使用商城同源会员收银页。`pay.185101.xyz` 仅提供四个服务 POST；**没有管理域名、管理页或管理 API**，运维只走 SSH 隧道。普通密码 / JWT、安装、补单、EPay、OkPay、公开收银、换链和 Telegram 入站管理均未注册；安全进程不启动旧成功回调 worker。

网关保存候选，商城拉取后持久 ACK，再通过两家独立 RPC 核验并结算。ACK 不等于到账，网关的订单状态不等于商城的付款或发货状态。`notify_url` 是现有创建协议仍要求的精确占位目标，须与商城渠道一致；本 profile 不发送成功回调，不能通过开启旧 worker 补救对接问题。

付款地址来自各自受控配置，两端必须独立核对；服务器只保存地址，不保存助记词 / 私钥。首期静态汇率单位为 USDT/CNY，四位小数、0.0001 尾差步长、最多 0.0099 尾差，共最多 100 个尝试槽位。历史槽位永久保留；重叠价格区间会减少可用容量。耗尽后拒绝新建，需受控更换独占收款地址或另行改造地址池，不能删除订单历史复用金额。

## 文件、权限和网络

| 宿主机目录（示例） | 容器路径 | 内容 / 权限 |
| --- | --- | --- |
| `/opt/dujiao-epusdt-state/config` | `/config`，只读 | `gateway.json`，0600，容器 UID 10001 可读 |
| `/opt/dujiao-epusdt-state/data` | `/data` | 主库 `epusdt.db`、WAL / SHM、持久候选 / 订单 / 扫描状态 / 认证 |
| `/opt/dujiao-epusdt-state/runtime` | `/runtime` | `epusdt-runtime.db`、进程锁、运行时状态 |
| `/opt/dujiao-epusdt-state/security` | `/security` | `revocations.jsonl`，独立于数据库备份的吊销记录 |

三个写目录均有 `.epusdt-storage` 标记，目录 0700、UID/GID 10001:10001；配置父目录同样受限。全部放在 Git checkout / build context 之外，不使用 symlink / 网络共享文件系统。`create_host_path:false` 禁止 Docker 隐式创建空目录；`serve` 不创建缺失数据库。丢文件时调查 / 恢复，不能运行 `init` 掩盖丢失。

Compose 只绑定经核实空闲的 `127.0.0.1:PORT`，使用独立 project / bridge，不接商城 DB / Redis 网络，不挂 Docker socket。运行 UID10001、只读 rootfs、drop ALL capabilities、no-new-privileges、384 MiB / 0.5 CPU / 128 pids；Docker local 日志最多 3 × 10 MiB。这些是初始上限，RN 验收须观察 OOM / 扫描延迟和商城健康；Docker 运行限额不限制构建。

应用出站限制 HTTPS/WSS、固定配置目标、443、公网 DNS 拨号、禁代理 / 重定向；这不是宿主机级出口防火墙，也不能隔离同机 root / 内核失守。源码改动后需重新审查出站路径。

## 首次构建、初始化与启动

1. 部署阶段重新核对 RN 资源 / 时钟 / Docker / 端口和既有站点健康。保留支付宝；不启用 U 支付渠道及商城三个开关，SQL 控制保持 frozen。
2. 用只读部署凭据 clone 自有 fork，核对维护分支 / 完整 SHA / clean Git；已有 checkout 用 `git pull --ff-only`，遇本地修改或分叉即停止调查。此仓库当前未提交的本地改造须先另行完成发布审核。
3. 建立独立受限 builder。下面内存 / CPU 为本地已验证的起始配置，RN 不足时暂停构建或调整已审核资源，不挤占商城：

```sh
export EPUSDT_BUILDER=dujiao-epusdt-build
docker buildx create --name "$EPUSDT_BUILDER" --driver docker-container --driver-opt memory=1536m --driver-opt cpu-period=100000 --driver-opt cpu-quota=100000 --driver-opt image=moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea --buildkitd-config ops/secure/buildkitd.toml
export EPUSDT_IMAGE=dujiao-epusdt:REPLACE_FULL_REVIEWED_COMMIT
sh ops/secure/build.sh
```

`build.sh` 拒绝 dirty Git 和非完整 SHA 标签，仅构建、不 pull、不切服务。Dockerfile 固定 Go 1.26.10 的基础镜像 digest（GO-2026-6809 的修复下限）、`-mod=readonly`、低并发；scratch 仅含 binary / CA。记录实际 image ID 和 revision，本地镜像未必有 registry RepoDigest。fork 已禁用自动沿用上游发布 / www 同步。

4. 创建四目录，将 [gateway.example.json](gateway.example.json) 私下填写为 `config/gateway.json`。独立随机 `merchant_secret` / `service_secret` 至少 32 字节；`pid` 专用且稳定；服务域名、RPC 与地址均核实。两个地址和静态汇率分别与商城渠道校对。不要将配置放入命令参数、聊天、日志或 Git；不要覆盖已填私有配置表。
5. 在 checkout 外建立只含下面三项、0600 的 Compose env 文件；它不含 RPC / API secret：

```dotenv
EPUSDT_IMAGE=dujiao-epusdt:REPLACE_FULL_REVIEWED_COMMIT
EPUSDT_STATE=/opt/dujiao-epusdt-state
EPUSDT_PORT=18081
```

6. 仅全新空目录执行一次初始化，然后启动：

```sh
docker compose --env-file /opt/dujiao-epusdt.compose.env -f ops/secure/compose.yaml -p dujiao-epusdt config --quiet
docker compose --env-file /opt/dujiao-epusdt.compose.env -f ops/secure/compose.yaml -p dujiao-epusdt run --rm --no-deps gateway -mode init
docker compose --env-file /opt/dujiao-epusdt.compose.env -f ops/secure/compose.yaml -p dujiao-epusdt up -d --no-deps gateway
docker compose --env-file /opt/dujiao-epusdt.compose.env -f ops/secure/compose.yaml -p dujiao-epusdt ps
```

初始化失败可能留下部分新目录标记，不能当作初始化成功；确认完全没有业务数据后另建空 state 路径重试，保留失败目录调查。启动包含 additive AutoMigrate，失败退出，不接收请求；升级前要备份并验证兼容性。`health` 只证明 HTTP 进程存活，不能证明扫描 / RPC / 结算正常。服务创建另有扫描新鲜度门禁：扫描差距 ≤20 块、区块时间 ≤2 分钟、最近轮询 ≤30 秒。

## Nginx 与服务域（无管理面）

模板行为、隔离回归与真实验收步骤见 [ingress-acceptance.md](ingress-acceptance.md)。模板增加 HTTP 404、双栈监听、Host/SNI 一致性、no-store / 禁继承缓存与 429；CI 显式启用真实 Nginx 合成证书测试。隔离测试不证明 Cloudflare AOP 已通过。

使用 [nginx.conf.example](nginx.conf.example) 渲染**唯一一个**服务 vhost，替换域名 / 回环端口 / 证书路径；`nginx -t` 成功后才 reload。Cloudflare 使用 Full (strict)，配置并验证 Authenticated Origin Pulls 和源站访问限制；检查 IPv4 / IPv6、其他 Host、默认站点、直连端口不可绕过。模板限流默认按实际连接地址；若启用 real-IP，仅信任已更新的 Cloudflare 网段，不能信任任意转发头。

**没有管理域名、没有管理页、没有 Passkey、没有 Access 应用。** 运维访问走 SSH 隧道到回环端口：

```sh
ssh -L 18081:127.0.0.1:18081 RN
curl -s http://127.0.0.1:18081/status     # 扫描健康 + 最近 30 单状态（不含金额/地址）
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18081/healthz
```

`/status` 与 `/healthz` 在进程里存在，但 nginx 只转发四个服务路径，其余一律 404，所以它们从公网不可达。这是**唯一**的运维入口——不要为了「方便」再把管理域加回来。冻结/解冻仍在商城侧用 `payment-ops` 执行，网关不提供任何改状态的接口。

## 商城配置与启用门槛

商城渠道 JSON 参考 [merchant-channels.example.json](merchant-channels.example.json)；文件仅是两个 `channel.config` 的样例，不是完整导入 API。渠道外层设置 provider=epusdt、member/order、初始 disabled，金额上限沿用“不限制”的实际语义。不要将 secret 给前端。

两端静态 rate 必须一致，precision=4、max_tail=0.0099、lifetime_seconds=900。商城在请求前固定绝对 `payment_expires_at_ms`，网关使用同一截止时间，重试不得延长；旧订单保持原策略，改价前处理未绑定在途单。已绑定单不随新配置改价。

为降低 BSC 查询成本，网关新增可选 `bsc_header_https`（示例为已核对的 BNB Chain 公共节点）：只承担补扫的区块头 / chain ID，`bsc_https` 继续承担日志 / chain ID，`bsc_wss` 保持订阅。两来源均须 chain ID 56；每条日志的 block hash、旧 checkpoint 重读和新 checkpoint 落盘规则保持。任一来源出错不推进，不自动把请求切回收费来源。字段留空时沿用原单端点补扫，不能沿用分源后的成本估算。

BSC 不满 10 块的连续范围最多等待 5 秒（受较小扫描间隔约束），每次最多等 2 秒便重新核对 finalized / checkpoint；到期即扫描余下范围，避免链停滞时一直凑不满。没有跳块、扩大免费 getLogs 范围或移除核验。两个 Alchemy 账号仍是一个独立运营方，主备切换由运维手动配对更改 HTTPS / WSS；商城双源证明完全独立，不能用网关分源替代。

按 0.45 秒/块、满批且无重试估算，分源并凑批后的 Alchemy 推进与无事件 WSS 统计约 36.288M CU/30 天；单个公开免费额度 30M 仍不够，两个账号合计 60M 的可用余量还须核对实际周期 / 用量。失败重试、实际事件、商城核验和不满批另计，不宣称免费无限使用。

商城配置另合并 `ops/maimen/payment-chain-verification.example.yml`，为每链配置两家独立 HTTPS RPC、可信主网锚点；网关 WSS 和商城 HTTPS 的同一服务商不算两个来源。RPC 矩阵见 [rpc-acceptance.md](rpc-acceptance.md)。三个开关及 Telegram 都保持默认 false，直到现场门槛逐项通过；商城 `/app/payment-ops` 控制冻结与审计，详见商城 `ops/maimen/payment-automation-control.md`。

## 更新、回退与轮换

构建新镜像时旧实例可运行；构建成功仍不能直接切换。先冻结商城 U 支付，停网关，手动做一致备份，记录旧 image ID / 配置 / 商城版本。更新 Compose env 镜像标签并重建 gateway；检查主库、runtime、security 均为原 bind mounts。复核服务认证、扫描追平和商城待办，再由当前 revision 明确恢复。失败时保持冻结；不得 `init`、删库、删候选或用 `latest` 临时降级。

只有数据库与固定支付 guard 兼容时才能回退镜像；旧版商城可能绕过核验，不能用旧代码消费新 fixed 单。需要恢复数据库时使用下面协调恢复流程，不把新库直接挂给不兼容旧镜像。

PID / 业务 secret 轮换不是无中断配置替换：商城保存历史订阅凭据，旧源要继续可访问或先排空核对候选；安全网关只启用当前 PID。轮换前冻结、保存成对快照、对账并规划所有未绑定单 / 未 ACK 候选的迁移。`service_secret` 也须协调所有持久订阅；不能直接删除旧订阅止告警。备份含这些凭据，保密等级等同生产配置。

## 用户手动备份到 Mac

无定时任务。用户决定备份时，顺序固定：**商城冻结成功 → 停网关 → 商城主库一致快照 → 网关快照 → 校验 → 用户手动取回并加密**。冻结不阻止普通支付宝业务，商城 PG 应用原生 `pg_dump -Fc` / 一致快照，不能复制活跃 PG 文件；配置文件另作受控保管。Redis 不是资金账本，不能代替主库快照。

服务器执行示例（替换已核对路径 / revision / image ID，商城 dump 已独立准备）：

```sh
python3 ops/secure/snapshot.py --state /opt/dujiao-epusdt-state --merchant-snapshot /opt/private-backup/merchant.dump --output /opt/private-backup/paired-REPLACE_TIMESTAMP --revision REPLACE_COMMIT --image-id sha256:REPLACE_IMAGE_ID --freeze-revision 2
```

脚本取得同一进程锁、SQLite backup / integrity_check，输出两库、配置、吊销账本、商城快照及哈希 manifest；不会启停 / 解冻服务。它不能证明外部 PG dump 里的冻结 revision，顺序和配对身份需操作者核对。输出为私有明文，须及时加密且由用户决定清理。

Mac 预先准备 `age` 和离线保管的解密 identity，recipient 是公开加密接收方。用户手动执行：

```sh
python3 ops/secure/backup-mac.py --host RN --remote-snapshot /opt/private-backup/paired-REPLACE_TIMESTAMP --recipient age1REPLACE_PUBLIC_RECIPIENT --output /Users/REPLACE/private-backups/epusdt-REPLACE_TIMESTAMP.tar.age --handoff-root /Users/REPLACE/path/dujiao_next
```

包装脚本只下载已准备的快照，核对 manifest 后打包加密、写 SHA256，不生成服务器快照、不定时、不覆盖旧备份。解密密钥与备份分开保管；手动核验可解密再判断备份完成。临时明文在该次运行结束清理，但不是 SSD 安全擦除保证。真实 SSH 传输需现场验收，本地验收使用模拟 SCP + 真实 age。

## 恢复

先冻结现有商城、停旧网关并保全当前 `security`；在隔离环境校验 / 解密备份，确认无恶意归档路径后提取到新目录，不覆盖 live state。保持现用配置和最新吊销账本：

```sh
python3 ops/secure/restore-prepare.py --snapshot /opt/private-restore/snapshot --current-security /opt/dujiao-epusdt-state/security --current-config /opt/dujiao-epusdt-state/config/gateway.json --output /opt/dujiao-epusdt-restored-REPLACE_TIMESTAMP
```

脚本验证配对文件 / 哈希、合并吊销、删除旧会话 / challenge / grant、清零候选 ACK 以便幂等重放，不删除候选或分配历史。恢复目录归属改为 UID10001 后才能挂载。缺最新吊销记录时停止自动恢复，执行独立身份恢复审查；不宣称旧备份安全复活。

商城主库另行恢复，**API / worker 启动前先用 payment-ops freeze**。核对备份到故障之间已发货 / 已消费事件 / 分配历史 / 钱包流水，再允许启动新隔离网关追平；未对齐的订单人工处理。链上不能重建已经发出的卡密或通知，不能自动解冻旧库。完成核对后才切域名与恢复，不同时运行两实例共享同一收款地址。

## 持续集成

工作区新增 `.github/workflows/secure-checks.yml`，覆盖完整 Go / vet、关键包 race、secure 入口 govulncheck v1.8.0、带真实 age 的合成备份，以及 secure 镜像构建和缺配置拒绝启动检查。它仅用 GitHub 托管临时资源、contents:read，不发布镜像、不读取私有文件或连接 RN；旧上游发布限制保持。

**2026-11-01 状态**：已提交、已推送、GitHub Actions 已跑通（两个 PR 全绿，含首跑时抓到的 GO-2026-6809 修复）。工作流只做检查、不发布镜像；网关镜像仍按自有 fork 的 clean SHA 在 RN 本地构建。

## 本地检查入口

```sh
# epusdt/src
go test ./...
go test -race -count=1 ./internal/securegateway ./task ./model/service ./model/data
# epusdt 根目录
python3 -B -m unittest discover -s ops/secure -p 'test_*.py'
```

完整验收区分软件认证器 / 合成 DOM 与真实浏览器、假 RPC 与主网、Compose 本地 bind mount 与 RN 实际目录。生产首次自然成交及 48 小时观察未发生前，不标记实付验收完成。

显式主网代码探针：`go test -tags rpcpreflight -run '^TestBSCProviderReadOnlyPreflight$' -count=1 -v ./task`，仅在运维明确运行时读取 `EPUSDT_RPC_PREFLIGHT_CONFIG` 指定的 0600 私有配置；值为 `-` 时从 stdin 读取，可通过加密 SSH 传入，避免远端落盘。最多 45 秒上下文，只查询双来源 chain ID、finalized 和一个 10 块 Transfer 样本，使用真实 Go 解码核对日志 / 头 hash；不创建数据库、订单或付款。不加入普通 CI，不把密钥放在命令参数。

Mac DNS 若将 RPC 域名解析到 `198.18.0.0/15` 等保留地址，安全拨号器会拒绝。这是防内网访问规则的预期行为，应在具有真实公网 DNS 的受控测试位置验证，不放宽地址过滤或启用任意代理来绕过。
