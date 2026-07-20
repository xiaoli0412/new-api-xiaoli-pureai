# Paste this into the new Codex window

请切换到目标执行模式，创建并持续推进以下长期目标，不要只做分析或重新生成计划：

## 目标

完整实施 New API x AETHER 双项目联动，并同时修改：

- `D:\workspaces\new-api`
- `D:\workspaces\Aether-pureai`

工作区根目录必须是 `D:\workspaces` 或更高层目录，并且必须确认对两个仓库以及构建缓存目录都有读写权限。两个仓库都应位于：

```text
codex/aether-newapi-integration
```

开始后首先完整阅读：

```text
D:\workspaces\new-api\docs\aether-newapi-handoff-2026-07-15.md
```

然后以当前工作树、实际文件和 fresh 测试结果为事实来源验证交接内容。不要盲信旧报告，但不要重复实现已经存在且经过验证的能力。

## 不可改变的边界

1. New API 是唯一的用户、充值、退款、订阅、余额、预扣费、结算和用户价格账本。
2. AETHER 仅消费匿名只读事件，并维护自己的上游成本、利润、余额、健康与路由分析；绝不能修改 New API 用户金融数据。
3. 普通 New API 渠道保持独立。禁止同步上游 Key、用户 API Key、支付信息、真实身份或余额明细。
4. AETHER 渠道必须进入 AETHER 现有 API-key 认证、Provider Catalog、Provider Pool、Routing Profile、代理、SSE、重试和 Usage Settlement 主链，不允许建设第二套孤立转发系统。
5. 只有 `direct_channel` 可以执行真实上游请求。`parallel_shadow` 和 `aether_decision` 在完整实现、安全测试和能力门控完成前必须失败关闭。
6. AETHER 的价格与利润结果只能展示或分析，不得自动改写 New API 用户价格。

## 工作树保护

- 两个仓库都有大量未提交修改，全部属于现有工作成果。
- 禁止 `git reset`、`git checkout --`、`git clean`、覆盖或删除现有改动。
- 不要创建新的替代架构，不要把现有实现整体推翻。
- 未经用户明确要求，不要 stage、commit、push 或创建 PR。
- 严格遵守两个仓库内所有适用的 `AGENTS.md`、Kiro SPEC、`SUPERSEDED.md` 和合同文件。
- New API 的项目名称、组织信息、品牌和归属信息受项目政策保护，不得修改或删除。

## 并行执行方式

建立三个同步工作流，并从第一轮就同时推进：

- A：New API 金融事务、outbox、定价、快照和管理界面。
- B：AETHER 验签、事件 inbox/outbox、Provider/Pool/路由、成本、利润和管理界面。
- C：双边合同、签名向量、revision 409、密钥轮换与双进程 E2E。

允许使用子代理并行处理互不重叠的文件域。所有行为变更严格执行 TDD：先写或保留失败测试，确认它因预期缺口失败，再做最小实现并验证绿色。每完成一个阶段，同时运行两个仓库的相关测试。

## 第一个必须处理的当前红灯

两个仓库的三个合同文件当前逐字节一致，但 manifest 内的 bundle hash 已过期：

```text
当前 manifest: 6ad9c1e5cffa66cff1212effa1db7ca04cb4003093c73912c176dfda35ddc730
实际 bundle:   bbeb8fbd2091b4001f43d02195ce38aa215e8f044ed6eaa3d42b718fd464a4bb
```

先在两个仓库同时把 `docs/contracts/aether-newapi-v1.json` 的 `bundle_sha256` 更新为实际值，然后运行 Go 与 Rust 合同测试。禁止只更新一侧。

合同 examples 已包含：

- `relay_signature_vector`
- `service_signature_vector`，覆盖 Unicode 与保留字符查询编码

Schema 已要求 `group`、`model`、`relay_format`。

## 随后按优先级继续

1. 验证 New API 全部 Tx outbox API 和真实业务入口。业务回滚必须不产生事件；重复交付不得重复入账。
2. 确认 usage settlement、refund、task refund、充值、订阅、渠道批量操作和 pricing option 与主数据库 outbox 的事务边界。独立 `LOG_DB` 只能是辅助，不能宣称跨库原子。
3. 运行并修复 pricing 原子快照、snapshot 完整时间窗汇总、payload object、稳定排序、decimal string 与 ETag 测试。
4. 在 AETHER 中让 `TrustedRelayContext` 真正进入现有 request parse、route/provider 和 Usage Settlement 输入。签名上下文与真实模型/格式不一致必须拒绝；不得绕过 AETHER API-key 认证。
5. 用可控假上游证明 direct_channel 只发生一次真实生成，并覆盖 SSE、重试、模型映射、请求 ID 和结算关联。
6. 将 AETHER export pricing/usage/events、event outbox、integration revision 和 profit ledger 改为数据库真源。Redis 只做缓存、租约、原子重放和协调。
7. 停止扩张 `relay_channels` 旁路数据面，复用现有 Provider Catalog、Provider API Keys、Pool、Routing Profile、Quota Snapshot 与 Usage Settlement。
8. 删除生产路径硬编码 `500000`，统一动态 `quota_per_unit`；缺失成本使用 `unknown`/`estimated`，不得伪造利润。
9. 完成每实例独立凭据、加密/哈希存储、轮换、撤销、双密钥过渡和数据库原子 revision compare-and-set；冲突返回 409 与差异。
10. 完成两边管理界面、i18n、三数据库测试、AETHER admin 统计失败修复、前端构建和双进程 E2E。

## 环境恢复

当前旧窗口无法运行 Go 测试，因为无权写入：

```text
D:\DevCache\go-mod\cache\download\golang.org\toolchain\@v\v0.0.1-go1.25.1.windows-amd64.lock
```

新窗口必须给 `D:\DevCache\go-mod` 写权限，或配置可写的 Go toolchain/cache。AETHER 编译所需本地工具信息在交接文档中。启动 Cargo 前先确认没有其他 cargo/rustc 进程。

## 汇报与完成标准

每次阶段汇报必须分别列出：

- New API：修改、红绿测试、剩余事务风险。
- AETHER：修改、编译/测试、主链与数据库真源风险。
- 双边合同：文件哈希、签名/revision/E2E 结果。

只要以下任一项仍未被当前证据证明，就不得标记目标完成：主数据库 outbox 原子性、真实 AETHER Provider/Pool/SSE/重试/结算主链、AETHER 数据库真源、动态价格/余额路由、凭据轮换、多节点失败关闭、SQLite/MySQL/PostgreSQL、双方管理界面、全量测试与构建、双进程 exactly-once 演练。

现在直接开始执行，不需要再次向我确认，也不要先停下来重新提问。

