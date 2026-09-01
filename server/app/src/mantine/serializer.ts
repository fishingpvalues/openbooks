// Minimal CSS-object serializer used by the local createStyles shim and the
// Mantine stylesTransform.
//
// Handles the subset of the old emotion/Mantine v5 style-object dialect this
// codebase uses:
//   - camelCase property names (hyphenated on output)
//   - number values (px suffix unless the property is unitless)
//   - nested objects under selector keys starting with "&", including
//     combined selectors ("&:hover, &:focus"), pseudo-elements
//     ("&::before") and class suffixes ("&.isResizing")
//
// Output is a set of flat top-level rules: nested selectors are expanded to
// ".<class><suffix>" so the result is valid CSS without nesting support.

// CSS properties that must not get a trailing "px".
const UNITLESS: Record<string, boolean> = {
  animationIterationCount: true,
  borderCount: true,
  boxFlex: true,
  boxFlexGroup: true,
  boxOrdinalGroup: true,
  columnCount: true,
  columns: true,
  flex: true,
  flexGrow: true,
  flexPositive: true,
  flexShrink: true,
  flexNegative: true,
  flexOrder: true,
  gridArea: true,
  gridRow: true,
  gridRowEnd: true,
  gridRowSpan: true,
  gridRowStart: true,
  gridColumn: true,
  gridColumnEnd: true,
  gridColumnSpan: true,
  gridColumnStart: true,
  fontWeight: true,
  lineClamp: true,
  lineHeight: true,
  opacity: true,
  order: true,
  orphans: true,
  tabSize: true,
  widows: true,
  zIndex: true,
  zoom: true,
  fillOpacity: true,
  floodOpacity: true,
  stopOpacity: true,
  strokeMiterlimit: true,
  strokeOpacity: true
};

export type NestedStyleValue = string | number | undefined;
export type StyleValue =
  | NestedStyleValue
  | Record<string, NestedStyleValue | Record<string, NestedStyleValue>>;

function hyphenate(name: string): string {
  return name.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`);
}

// Walk one style object. `parent` is the selector context for nested keys
// ("&" at the top level). Collects declarations per selector.
function walk(
  obj: Record<string, StyleValue>,
  parent: string,
  rules: Map<string, string[]>
): void {
  for (const key of Object.keys(obj)) {
    const value = obj[key];
    if (value === undefined || value === null) continue;
    if (typeof value === "object") {
      // Nested object: the key is a relative selector like "&:hover".
      const selector = key.replace(/&/g, parent);
      walk(value as Record<string, StyleValue>, selector, rules);
      continue;
    }
    if (typeof value === "boolean") continue;
    const prop = key.startsWith("--") ? key : hyphenate(key);
    let val: string | number = value as string | number;
    if (typeof val === "number" && val !== 0 && !UNITLESS[key]) {
      val = `${val}px`;
    }
    const decls = rules.get(parent) ?? [];
    decls.push(`${prop}:${val};`);
    rules.set(parent, decls);
  }
}

// djb2 hash, base36 - stable per content so identical styles share a name.
function hash(text: string): string {
  let h = 5381;
  for (let i = 0; i < text.length; i++) {
    h = ((h << 5) + h + text.charCodeAt(i)) | 0;
  }
  return `obk_${(h >>> 0).toString(36)}`;
}

// Serialize one named style block into a stable class name plus the full CSS
// text for that class (including its nested selectors).
export function serializeStyleBlock(styles: Record<string, StyleValue>): {
  className: string;
  css: string;
} {
  const rules = new Map<string, string[]>();
  walk(styles, "&", rules);

  const parts: string[] = [];
  rules.forEach((decls, sel) => {
    parts.push(`${sel.replace(/&/g, ".__OBS__")}{${decls.join("")}}`);
  });
  const body = parts.join("");
  const className = hash(body);
  return { className, css: body.replace(/__OBS__/g, className) };
}

// Inject a unique CSS body into a <style> tag exactly once.
const injected = new Set<string>();
export function injectCss(cssText: string): void {
  if (!cssText || injected.has(cssText)) return;
  injected.add(cssText);
  const el = document.createElement("style");
  el.setAttribute("data-openbooks-styles", "true");
  el.textContent = cssText;
  document.head.appendChild(el);
}

// Serialize a style block and inject its CSS; returns the class name.
export function serializeToClass(styles: Record<string, StyleValue>): string {
  const { className, css } = serializeStyleBlock(styles);
  injectCss(css);
  return className;
}
