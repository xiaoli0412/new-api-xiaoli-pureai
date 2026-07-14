---
inclusion: auto
---

# New API 项目开发规范

## 基本要求

- 所有回复使用中文
- 所有修改必须提交到本地 Git 仓库并推送到 GitHub 远程仓库 `xiaoli`
- 远程仓库地址: https://github.com/xiaoli0412/new-api-xiaoli-pureai.git
- Git 用户: xiaoli0412 / liguangtuo138@outlook.com

## UI 一致性规范

- 所有用户可见文案必须使用 `t()` 包裹进行国际化处理
- 新增翻译 key 必须同时更新 `web/default/src/i18n/locales/en.json` 和 `zh.json`
- 中文术语参照项目现有翻译保持一致（速率限制、不限、分组等）
- UI 组件使用项目 shadcn/ui 组件库，不引入新依赖
- 表单布局遵循 `SideDrawerSection` 模式
- 前端样式使用 Tailwind CSS，保持与现有组件一致的 class 命名

## 后端规范

- Go 代码修改后必须通过 `go build` 和 `go vet` 验证
- 新增数据库字段使用 GORM tag，支持自动迁移
- 新增系统设置需在 `model/option.go` 的 `InitOptionMap` 和 `updateOptionMap` 中注册
- 缓存结构(UserBase)必须与主模型(User)同步更新
- 中间件通过 Context 传递数据，新增 key 在 `constant/context_key.go` 注册

## 工作流程

- 每次大修改后，本地最小性能消耗启动项目供前端检查
- 后端: `go run main.go`（需要前端 dist 产物）或 docker compose
- 前端开发模式: `cd web/default && bun run dev`（需要 bun）
- 根据任务复杂度使用 sub agent 分工
- 修改完成后统一 commit + push

## 提交规范

- feat: 新功能
- fix: 修复
- refactor: 重构
- docs: 文档
- style: 样式/格式
- commit message 使用中文描述，标题简洁

## 技术栈速查

- 后端: Go 1.25+, Gin, GORM, Redis
- 前端(default): React 19, TypeScript, TanStack Router/Query, Tailwind CSS 4, Rsbuild, Zustand, shadcn/ui
- 数据库: PostgreSQL / MySQL / SQLite
- 缓存: Redis
- 构建工具: bun (前端), go build (后端)
- Docker 镜像: calciumion/new-api:latest
