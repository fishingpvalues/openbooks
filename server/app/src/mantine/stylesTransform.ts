// Mantine v9 replaced emotion with a `stylesTransform` hook so apps can still
// use CSS-in-JS for the `styles` prop. This wires the local serializer into
// that hook: every styles record passed to a component is serialized to real
// CSS (handling the "&"-nested selectors Mantine v9 no longer resolves) and
// the resulting class names are returned for the matching style names.

import type { MantineStylesTransform } from "@mantine/core";
import { injectCss, serializeStyleBlock, type StyleValue } from "./serializer";

type StyleRecord = Record<string, unknown>;

// One raw entry of the styles prop: a record, an array of records, or a
// (theme, props, ctx) function returning one of those.
function resolveEntry(entry: unknown, theme: unknown): StyleRecord | undefined {
  if (!entry) return undefined;
  if (typeof entry === "function") {
    return (entry as (t: unknown) => StyleRecord)(theme);
  }
  if (Array.isArray(entry)) {
    const merged: StyleRecord = {};
    entry.forEach((item) => {
      if (item && typeof item === "object") {
        Object.assign(merged, item);
      }
    });
    return merged;
  }
  if (typeof entry === "object") {
    return entry as StyleRecord;
  }
  return undefined;
}

export const mantineStylesTransform: MantineStylesTransform = {
  styles: () => (styles: unknown, payload: { theme?: unknown }) => {
    const resolved = resolveEntry(styles, payload?.theme);
    if (!resolved) return {};
    const out: Record<string, string> = {};
    for (const key of Object.keys(resolved)) {
      const value = resolved[key];
      if (!value || typeof value !== "object") continue;
      const { className, css } = serializeStyleBlock(
        value as Record<string, StyleValue>
      );
      if (!css) continue;
      out[key] = className;
      injectCss(css);
    }
    return out;
  }
};
