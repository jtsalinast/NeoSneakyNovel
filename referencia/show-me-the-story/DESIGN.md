---
name: Show Me The Story
description: A calm, high-density workspace for long-form fiction.
colors:
  canvas: "oklch(20% 0 0)"
  surface: "oklch(26% 0 0)"
  surface-raised: "oklch(37% 0 0)"
  ink: "oklch(96% 0.001 286.375)"
  primary: "oklch(81% 0.117 11.638)"
  info: "oklch(75% 0.10 250)"
  success: "oklch(75% 0.10 150)"
  warning: "oklch(82% 0.14 85)"
  error: "oklch(70% 0.18 25)"
typography:
  title:
    fontFamily: "Inter, Noto Sans SC, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.3
  body:
    fontFamily: "Inter, Noto Sans SC, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.65
  label:
    fontFamily: "Inter, Noto Sans SC, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 500
    lineHeight: 1.5
rounded:
  control: "0.5rem"
  surface: "0.5rem"
spacing:
  xs: "0.25rem"
  sm: "0.5rem"
  md: "1rem"
  lg: "1.25rem"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.canvas}"
    rounded: "{rounded.control}"
    height: "2rem"
  panel:
    backgroundColor: "{colors.canvas}"
    textColor: "{colors.ink}"
    rounded: "{rounded.surface}"
    padding: "1rem"
---

# Design System: Show Me The Story

## Overview

**Creative North Star: "The Writer's Desk"**

A focused working surface for authors who stay in the application for hours. The interface is restrained and familiar: prose and decisions lead, while system state remains easy to scan. Density is purposeful, not cramped, and visual personality comes from precise rhythm rather than decoration.

**Key Characteristics:** flat tonal layers, one restrained accent, compact controls, readable prose, visible system status, and responsive drawers.

## Colors

The existing Xianii dark neutral palette is normative. Primary color is reserved for the current location and the single main action; semantic colors communicate actual status only.

## Typography

One sans-serif family serves interface text. Body copy is 16px with relaxed leading; labels use 14px; 12px is reserved for metadata. Prose stays within 75 characters per line.

## Layout

At 1280px and above the application uses navigation, primary workspace, and assistant columns. From 1024px to 1279px the assistant becomes a drawer. Below 1024px both navigation and assistant become drawers; below 768px fixed two-column editors stack into one column. The main workspace always uses `min-width: 0` and independent vertical scrolling. The header title has no truncation cap and may wrap; responsive drawers are positioned within the workspace so header height may grow safely.

## Elevation & Depth

The system is flat. Tonal surfaces, one-pixel borders, spacing, and selected states communicate depth; shadows are not used.

## Shapes

Controls and panels use the existing 0.5rem radius. Pills are limited to compact status badges. Nested panels use tonal contrast without a second border.

## Components

Primary buttons are solid and rare. Ordinary actions are outlined, low-priority actions are ghost buttons, and destructive actions remain outlined error buttons. Inputs use the content surface with a clear accent focus ring. Cards group a real task only; spacing and dividers replace unnecessary nested cards. Navigation uses plain text, grouped by thin dividers, with a solid selected state.

## Do's and Don'ts

### Do:
- Do keep the active writing or review surface dominant.
- Do expose loading, error, empty, disabled, focus, and selected states.
- Do use motion only for feedback, drawer state changes, and the graph camera’s 320ms ease-out fit transition; reduced-motion uses the final camera transform immediately.

### Don't:
- Don't add decorative emoji, gradients, shadows, or status colors.
- Don't introduce another component or icon library for this visual system.
- Don't change routes, product terminology, or business behavior for visual novelty.
