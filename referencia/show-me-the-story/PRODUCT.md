# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Novelists who spend long sessions planning, drafting, revising, and maintaining continuity across a book. The primary use scene is a desktop writing workspace; narrow screens must remain fully usable.

## Product Purpose

Show Me The Story is a local AI-assisted novel-writing workspace. It keeps planning, prose, facts, settings, foreshadows, proofreading, and assistant conversations connected while the author remains in control of every durable change.

## Positioning

A single small local application combines structured long-form planning with chapter-level writing, evidence-linked continuity memory, and explicit review gates instead of treating a novel as one undifferentiated chat transcript.

## Operating Context

Authors create or restore a project, configure the story, plan outline batches, generate and review chapters, inspect continuity knowledge and foreshadows, proofread a completed manuscript, and export or continue it. AI tasks are serialized and their state remains visible throughout the workspace.

## Capabilities and Constraints

- Web UI embedded in a single Go binary; Svelte 4, Tailwind CSS 4, DaisyUI 5, and `@xianii/design-system` are the established frontend stack.
- Project content is bilingual (`zh` or `en`) while UI language is independently switchable.
- Existing API routes, SSE behavior, hash navigation, storage safety, version checks, and direct page actions are product contracts.
- No commercial claims, customer evidence, telemetry, or cloud collaboration should be invented.

## Brand Commitments

Keep the product name and existing logo. The voice is direct, calm, and functional in both Chinese and English.

## Evidence on Hand

The repository contains complete working flows, bilingual UI copy, an existing logo, offline screenshot fixtures, and detailed Chinese and English usage guides. It contains no testimonials, customer logos, or usage metrics.

## Product Principles

- Keep the author in control of permanent changes.
- Make the current writing task visually dominant.
- Preserve context without overwhelming the author.
- Explain failures and recovery paths precisely.
- Prefer a compact local workflow over extra infrastructure.

## Accessibility & Inclusion

All core actions must remain keyboard accessible, focus-visible, readable at WCAG 2.1 AA contrast, and operable on narrow screens. Reduced-motion preferences must be honored.
