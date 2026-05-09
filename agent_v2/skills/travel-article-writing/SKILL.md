---
name: travel-article-writing
description: 将旅游规划素材转成适合发布的攻略文章。Use when the task is to write a travel article from a travel planning JSON that separates route validation from subjective insights.
---

# Travel Article Writing

## 目标

把 `travel_plan` 转成一篇结构清晰、可执行、适合发布的旅游攻略文章。

## 写作规则

- 先给路线总览，再按天或按片区展开。
- `route_validation` 只能写成已验证事实。
- `content_insights` 只能写成体验建议、避坑提示或推荐理由。
- 优先写交通衔接、体力成本、节奏安排和注意事项。
- 不要把“待实时确认”的信息写成确定事实。

## 推荐结构

```markdown
# 标题

## 为什么这样走

## 路线总览

## 第 1 天

### 地点 A

### 地点 B

## 第 2 天
```

## 风格要求

- 具体、克制、少空话
- 少堆历史背景
- 多写执行层信息
