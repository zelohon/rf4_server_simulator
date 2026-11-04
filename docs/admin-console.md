# 管理控制台

管理控制台直接内嵌在 `launcher` 中，通过统一控制中心 `http://127.0.0.1:9500` 的“管理后台”页签进入，不再监听独立的 `8080` 端口。admin 的 HTTP handler 挂载在 `/admin/`，前端 API 使用 `/admin/api/*`；内部 handler 仍保持 `/api/*` 路由，便于测试和独立调试。控制台使用 `config.yaml` 中的管理员凭据进行认证；登录成功后返回 JWT，供控制台和 API 使用。

该界面可以修改玩家数据、发放物品、调整经济配置以及管理会话。请仅在可信网络接口上开放。

## 玩家管理

- 按 UID 或名称搜索玩家。
- 编辑等级、经验、银币、金币和银行存款。
- 查看并修改背包和鱼护内容。
- 添加或移除鱼。
- 编辑各钓鱼技能分类的熟练度。
- 发放目录中的物品并移除已有物品。
- 将在线玩家踢下线或封禁玩家。

影响在线客户端的修改会通过 gameserver 命令队列下发。

## 商店

- 基础商店目录为只读数据。
- 按商店和物品设置的覆盖价格与库存会单独存储。
- 价格和库存可以恢复为基础目录中的值。
- 控制台、商店浏览和购买结算使用同一套目录解析逻辑。

解析顺序和覆盖范围详见 [shop_catalog.md](shop_catalog.md)。

## 钓鱼配置

- 选择水域并配置咬钩概率和时间。
- 启用或禁用鱼种，并设置相对权重。
- 从完整鱼池中添加鱼种。
- 将水域配置重置为静态默认值。

后续的钓鱼抽取和咬钩计时会读取这些修改，无需重启服务器。

“新吃饵逻辑”是全水域开关，默认关闭。开启后按地图名册、后台启禁、已识别饵料和本地实钓搭配筛鱼；没有匹配鱼时不使用备用鱼池。明确不吃的饵优先排除，关闭后恢复旧判鱼流程。开关独立于调试预设保存，最多 30 秒影响后续判鱼，不重新抽取已咬钩的鱼；GM 指定下一条鱼仍保留原行为。

当前尚未接入钓点坐标分布、双饵组合、船票与上船、顺水漂流及 jig 等特定动作、海图区号，控制台会显示这些范围。未识别且没有实钓证据的搭配暂不放行。规则随服务端发布，本次不自动联网更新统计数据。

## 邮件

向玩家发送一条文本邮件，并可附带零个或多个目录物品。每项附件单独设置数量。邮件投递由 gameserver 执行。

## 审计日志

管理操作产生的变更会写入 `audit_log`。控制台提供只读的审计列表。

## API

统一入口下，以下路径均增加 `/admin` 前缀，例如登录为 `POST /admin/api/login`。除登录外，其他请求均需将 JWT 作为 bearer token 提交。独立调试 `cmd/admin` 时仍直接使用 `/api/*`。

```text
POST /api/login
GET  /api/me
GET  /api/dashboard
GET  /api/tunables
POST /api/tunables
GET  /api/players
GET  /api/players/detail
POST /api/players/update
POST /api/players/grant
POST /api/players/items/delete
POST /api/players/keepnet/add
POST /api/players/keepnet/delete
POST /api/players/skills/update
POST /api/players/kick
POST /api/players/ban
GET  /api/catalog/items
GET  /api/stores
GET  /api/stores/items
POST /api/stores/items/update
POST /api/stores/items/reset
GET  /api/fish/areas
GET  /api/fish/config
POST /api/fish/config
POST /api/fish/config/reset
GET  /api/fish/pool
POST /api/mail/send
GET  /api/audit
```
