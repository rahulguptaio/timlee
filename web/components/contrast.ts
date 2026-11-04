/**
 * contrast.ts — WCAG 2 contrast for the stories' and the unit tests' checks.
 *
 * Not a component and not shipped. Whatever a computed style serialises a
 * colour to (rgb(), oklch(), color-mix()), it is painted into a canvas pixel
 * and read back as sRGB first, so the ratio is the one the reader sees.
 */

let probe: CanvasRenderingContext2D | null = null;

/** Any CSS colour as sRGB channels and alpha, 0–255, read back from a canvas. */
function paint(color: string): [number, number, number, number] {
  probe ??= document.createElement('canvas').getContext('2d', { willReadFrequently: true });
  if (!probe) throw new Error('no 2D canvas');
  probe.clearRect(0, 0, 1, 1);
  probe.fillStyle = color;
  probe.fillRect(0, 0, 1, 1);
  const [r = 0, g = 0, b = 0, a = 0] = probe.getImageData(0, 0, 1, 1).data;
  return [r, g, b, a];
}

/** Any CSS colour as opaque sRGB channels, 0–255. */
export function srgb(color: string): [number, number, number] {
  const [r, g, b] = paint(color);
  return [r, g, b];
}

/** WCAG 2 relative luminance. */
export function luminance(color: string): number {
  const [r, g, b] = srgb(color).map((channel) => {
    const c = channel / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG 2 contrast ratio between two colours. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

/** The colour actually painted behind an element: its nearest opaque background. */
export function backdrop(el: Element): string {
  for (let node: Element | null = el; node; node = node.parentElement) {
    const color = getComputedStyle(node).backgroundColor;
    if (paint(color)[3] === 255) return color;
  }
  return getComputedStyle(document.documentElement).backgroundColor;
}
