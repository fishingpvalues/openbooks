// Local replacement for the createStyles hook that Mantine v9 removed.
//
// Mantine v5 exposed createStyles from @mantine/core backed by emotion. v9
// dropped the emotion pipeline, so this shim re-implements the small surface
// this app actually uses:
//
//   const useStyles = createStyles((theme, params) => ({ ... }));
//   const { classes, cx, theme } = useStyles(params);
//
// The callback receives a theme object that still exposes the v5 conveniences
// the codebase relies on (theme.colorScheme and theme.fn.primaryColor /
// theme.fn.focusStyles), and returns one CSS class per top-level key. Styles
// are serialized to real CSS (see ./serializer) and injected as <style> tags,
// deduplicated by content so a colour-scheme toggle only adds CSS when the
// serialized output actually changes.

import { useContext, useLayoutEffect, useMemo } from "react";
import { MantineContext, useSafeMantineTheme } from "@mantine/core";
import type { MantineColorScheme, MantineTheme } from "@mantine/core";
import { injectCss, serializeStyleBlock, type StyleValue } from "./serializer";

export type CompatMantineTheme = Omit<MantineTheme, "colors"> & {
  colors: Record<string, readonly string[]>;
  colorScheme: MantineColorScheme;
  fn: {
    primaryColor: (shade?: number) => string;
    focusStyles: () => Record<string, string>;
  };
};

export type StylesCallback<TParams> = (
  theme: CompatMantineTheme,
  params: TParams,
  getRef: (ref: unknown) => void
) => Record<string, Record<string, StyleValue>>;

// Emotion-style class name combiner (variadic: string, record, or array).
export function cx(
  ...inputs: Array<
    string | Record<string, boolean | undefined> | string[] | undefined | null
  >
): string {
  const out: string[] = [];
  const visit = (input: unknown): void => {
    if (!input) return;
    if (typeof input === "string") {
      out.push(input);
    } else if (Array.isArray(input)) {
      input.forEach(visit);
    } else {
      for (const key of Object.keys(input)) {
        if ((input as Record<string, boolean | undefined>)[key]) out.push(key);
      }
    }
  };
  inputs.forEach(visit);
  return out.join(" ");
}

function shadeFor(rawTheme: MantineTheme, scheme: string): number {
  const ps = rawTheme.primaryShade;
  if (typeof ps === "object") {
    return (ps as { light: number; dark: number })[
      scheme === "dark" ? "dark" : "light"
    ];
  }
  return (ps ?? 6) as number;
}

export function useCompatTheme(): CompatMantineTheme {
  // Read the theme and colour scheme without throwing. v9's
  // useMantineTheme()/useMantineColorScheme() throw when called above a
  // MantineProvider, but this app calls createStyles from the component that
  // renders the provider. Fall back to the default theme (matching v5's
  // createStyles) so those call sites keep working instead of crashing.
  const rawTheme = useSafeMantineTheme();
  const scheme = useContext(MantineContext)?.colorScheme;
  const colorScheme: MantineColorScheme = scheme ?? "light";

  return useMemo<CompatMantineTheme>(() => {
    const colors = rawTheme.colors as unknown as Record<
      string,
      readonly string[]
    >;
    const primaryColor = rawTheme.primaryColor ?? "blue";
    return {
      ...rawTheme,
      colors,
      colorScheme,
      fn: {
        primaryColor: (shade?: number) => {
          const tuple = colors[primaryColor];
          if (!tuple) return "";
          const idx = shade ?? shadeFor(rawTheme, colorScheme);
          return tuple[idx] ?? tuple[0] ?? "";
        },
        focusStyles: () => ({ outline: "none" })
      }
    };
  }, [rawTheme, colorScheme]);
}

export function createStyles<TParams extends object = Record<string, never>>(
  callback: StylesCallback<TParams>
): (params?: TParams) => {
  classes: Record<string, string>;
  cx: typeof cx;
  theme: CompatMantineTheme;
} {
  return (params?: TParams) => {
    const theme = useCompatTheme();

    const result = callback(theme, (params ?? {}) as TParams, () => {});

    const classes: Record<string, string> = {};
    const cssParts: string[] = [];
    for (const key of Object.keys(result)) {
      const { className, css } = serializeStyleBlock(result[key]);
      classes[key] = className;
      cssParts.push(css);
    }
    const fullCss = cssParts.join("\n");

    useLayoutEffect(() => {
      injectCss(fullCss);
    }, [fullCss]);

    return { classes, cx, theme };
  };
}
