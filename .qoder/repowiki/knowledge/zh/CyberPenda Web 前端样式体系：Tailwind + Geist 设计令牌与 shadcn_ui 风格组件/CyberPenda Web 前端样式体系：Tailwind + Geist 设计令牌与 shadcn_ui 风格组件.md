---
kind: frontend_style
name: CyberPenda Web 前端样式体系：Tailwind + Geist 设计令牌与 shadcn/ui 风格组件
category: frontend_style
scope:
    - '**'
source_files:
    - web/src/index.css
    - web/tailwind.config.js
    - web/src/components/ui.tsx
    - web/src/components/ThemeProvider.tsx
    - web/src/components/theme-context.ts
    - web/src/lib/utils.ts
    - web/src/components/sharedStyles.ts
    - web/package.json
---

## 1. 系统与方法论

CyberPenda 的 Web 前端（`web/`）采用 **Tailwind CSS 3** 作为原子化样式引擎，结合 **Geist 字体家族**（@fontsource/geist、geist-mono）和一套以 HSL CSS 变量为核心的**设计令牌（Design Tokens）**，实现明暗双主题。组件层遵循 **shadcn/ui 风格约定**：使用 `class-variance-authority`（cva）声明变体、`clsx` + `tailwind-merge` 合并类名，并通过 React forwardRef 暴露语义化 UI 原语（Button、Input、Textarea、Select、Label、Badge、Card 等）。

## 2. 关键文件与包

- 样式入口与令牌定义
  - `web/src/index.css` — Tailwind 指令注入 + 明暗主题 HSL 变量（background/foreground/card/sidebar/semantic colors/shadows/radius/font）+ 全局 base 规则（滚动条、focus-visible、代码字体、动画 keyframes）
  - `web/tailwind.config.js` — 将 CSS 变量映射到 Tailwind 颜色、圆角、阴影、字体族与自定义 easing（`geist`）
- 主题运行时
  - `web/src/components/theme-context.ts` — Theme 类型、localStorage 持久化键、`resolveTheme`/`applyTheme`/`useTheme`
  - `web/src/components/ThemeProvider.tsx` — 监听 OS `prefers-color-scheme`、切换 `.dark` class、导出 `ThemeToggle` 按钮
- 基础 UI 组件（cva 变体）
  - `web/src/components/ui.tsx` — Card / Button / Badge / Input / Textarea / Select / Label 等原子组件
  - `web/src/lib/utils.ts` — `cn(...)` 工具函数（clsx + tailwind-merge），供所有组件复用
  - `web/src/components/sharedStyles.ts` — 页面级共享样式辅助（如设置项列表选中态）
- 构建与依赖
  - `web/package.json` — React 19、Vite 8、Tailwind 3、cva、clsx、lucide-react、react-router-dom、vitest 测试栈

## 3. 架构与约定

| 层次 | 职责 | 约定 |
|---|---|---|
| **设计令牌层** | 在 `:root` / `.dark` 下以 HSL 三元组声明所有颜色、阴影、圆角、字体 | 通过 `hsl(var(--xxx))` 被 Tailwind 直接消费，alpha 修饰符（如 `bg-primary/50`）天然可用 |
| **Tailwind 配置层** | `tailwind.config.js` 将 token 映射到 `colors.*`、`borderRadius.*`、`boxShadow.*`、`fontFamily.*`、`transitionTimingFunction.geist` | 新增 token 只需改 CSS 变量 + 对应 Tailwind extend 条目 |
| **主题运行时层** | `ThemeProvider` 维护 `light`/`dark` 状态并同步到 `<html>` 的 `class` 与 `colorScheme` | 组件只读 `useTheme()`，不直接操作 DOM |
| **UI 组件层** | `ui.tsx` 中每个组件用 `cva` 声明 `variant`/`size` 变体，统一通过 `cn()` 合并 className | 禁止在组件内写死色值或尺寸，一律走 token 或 cva 变体 |
| **页面/业务层** | `pages/` 组合 UI 组件与业务逻辑；`components/` 下按功能域拆分（如 `task-transcript/`） | 复用 `sharedStyles.ts` 中的共享类生成器，避免重复条件类拼接 |

## 4. 开发者应遵守的规则

1. **颜色与尺寸一律走 Token**
   - 使用 `bg-background`、`text-muted-foreground`、`border-border` 等 Tailwind 语义类，而非硬编码十六进制。
   - 需要新语义色时，先在 `index.css` 的 `:root` / `.dark` 下追加 HSL 变量，再在 `tailwind.config.js` 的 `extend.colors` 中注册。

2. **组件变体使用 cva 管理**
   - 新增 UI 组件必须用 `cva` 声明 `variant`/`size`，并通过 `forwardRef` 暴露 ref，保持与现有 Button/Input/Card 一致的 API。

3. **类名合并使用 `cn()`**
   - 所有 `className` 拼接都经过 `cn(...)`（clsx + tailwind-merge），确保冲突类正确覆盖且无冗余。

4. **主题切换仅通过 Context**
   - 组件内通过 `useTheme()` 读取当前主题；修改主题调用 `setTheme` 或 `toggleTheme`，不要直接操作 `document.documentElement.classList`。

5. **响应式与可访问性**
   - 利用 Tailwind 断点（`sm:`/`md:`/`lg:`/`2xl:`）做响应式布局；焦点可见性统一走 `focus-visible:ring-ring`，已在 base 层提供默认 ring 样式。
   - 动画需尊重 `prefers-reduced-motion`，已有 `logo-spin`、`save-label-in`、`save-check-pop` 示例。

6. **图标与字体**
   - 图标统一使用 `lucide-react`；字体通过 `--font-geist-sans` / `--font-geist-mono` 变量引用，避免直接写字体族字符串。

7. **测试与回归**
   - 样式回归由 `design-system-regressions.test.ts` 与 `index.css.test.ts` 保障；新增 UI 组件建议附带 Vitest 快照或行为测试。
