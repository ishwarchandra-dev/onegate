#!/usr/bin/env python3
"""WCAG contrast evidence for the OneGate dashboard tokens.

Parses the :root / .dark blocks of web/app/app.css, converts every oklch
token to sRGB, and computes WCAG 2.1 contrast ratios for the pairings the
design system commits to (docs/design-system.md):

  text pairs   -> AA 4.5:1 (normal text)
  graphics     -> AA 3:1  (WCAG 1.4.11 non-text: chart series, focus ring)

Exits non-zero if any committed pairing fails. This is the p6.design-system
acceptance evidence: "Dark/light parity; AA contrast".
"""
import math
import re
import sys

CSS = "web/app/app.css"

# (fg token, bg token, minimum ratio, class)
LIGHT_PAIRS = [
    ("foreground", "background", 4.5, "text"),
    ("foreground", "card", 4.5, "text"),
    ("card-foreground", "card", 4.5, "text"),
    ("popover-foreground", "popover", 4.5, "text"),
    ("muted-foreground", "background", 4.5, "text"),
    ("muted-foreground", "card", 4.5, "text"),
    ("secondary-foreground", "secondary", 4.5, "text"),
    ("accent-foreground", "accent", 4.5, "text"),
    ("primary-foreground", "primary", 4.5, "text"),
    ("destructive", "background", 4.5, "text"),
    ("success", "background", 4.5, "text"),
    ("warning", "background", 4.5, "text"),
    ("success-foreground", "success", 4.5, "text"),
    ("warning-foreground", "warning", 4.5, "text"),
    ("chart-tooltip-foreground", "chart-tooltip", 4.5, "text"),
    ("chart-axis", "card", 4.5, "text"),
    ("sidebar-foreground", "sidebar", 4.5, "text"),
    ("ring", "background", 3.0, "graphics"),
    ("border", "background", 1.0, "info"),
    ("input", "background", 1.0, "info"),
]
GRAPHICS_ON_CARD = [f"chart-{i}" for i in range(1, 9)]

DARK_PAIRS = LIGHT_PAIRS  # identical pairing contract => parity by construction


def parse_tokens(path):
    """Return {block: {token: (L, C, H or None, alpha or 1)}} for :root and .dark."""
    src = open(path).read()
    blocks = {}
    for name in (":root", ".dark"):
        m = re.search(re.escape(name) + r"\s*\{([^}]*)\}", src)
        if not m:
            sys.exit(f"ERROR: {name} block not found in {path}")
        toks = {}
        for line in m.group(1).splitlines():
            mm = re.match(r"\s*--([a-z0-9-]+):\s*oklch\(([^)]+)\)", line)
            if not mm:
                continue
            parts = [p.strip() for p in mm.group(2).split()]
            l = float(parts[0])
            c = float(parts[1])
            h = None
            alpha = 1.0
            for p in parts[2:]:
                if p.endswith("%"):
                    p = p[:-1]
                try:
                    h = float(p)
                except ValueError:
                    if p.startswith("alpha") or "/" in p:
                        pass
            # alpha form: "oklch(1 0 0 / 10%)" -> parts ['1','0','0','/','10%']
            if "/" in parts:
                idx = parts.index("/")
                a = parts[idx + 1]
                alpha = float(a.rstrip("%")) / 100.0
            toks[mm.group(1)] = (l, c, h, alpha)
        blocks[name] = toks
    return blocks


def oklch_to_srgb(l, c, h, alpha=1.0):
    """oklch -> linear sRGB -> composite over white (alpha<1) -> gamma sRGB [0,1]."""
    if h is None:
        h = 0.0
    hr = math.radians(h)
    a_ = math.cos(hr) * c
    b_ = math.sin(hr) * c
    # oklab -> LMS'
    l_ = l + 0.3963377774 * a_ + 0.2158037573 * b_
    m_ = l - 0.1055613458 * a_ - 0.0638541728 * b_
    s_ = l - 0.0894841775 * a_ - 1.2914855480 * b_
    l_, m_, s_ = l_ ** 3, m_ ** 3, s_ ** 3
    # LMS -> linear sRGB
    r = +4.0767416621 * l_ - 3.3077115913 * m_ + 0.2309699292 * s_
    g = -1.2684380046 * l_ + 2.6097574011 * m_ - 0.3413193965 * s_
    b = -0.0041960863 * l_ - 0.7034186147 * m_ + 1.7076147010 * s_

    def over_white(ch):
        # composite linear channel over white with alpha
        return ch * alpha + (1.0 - alpha) * 1.0

    r, g, b = over_white(r), over_white(g), over_white(b)

    def gamma(u):
        if u <= 0.0031308:
            return 12.92 * u
        return 1.055 * (u ** (1 / 2.4)) - 0.055

    return gamma(max(0.0, min(1.0, r))), gamma(max(0.0, min(1.0, g))), gamma(max(0.0, min(1.0, b)))


def rel_luminance(rgb):
    def lin(u):
        return u / 12.92 if u <= 0.04045 else ((u + 0.055) / 1.055) ** 2.4
    r, g, b = (lin(c) for c in rgb)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def ratio(fg, bg):
    l1, l2 = sorted((rel_luminance(fg), rel_luminance(bg)), reverse=True)
    return (l1 + 0.05) / (l2 + 0.05)


def main():
    blocks = parse_tokens(CSS)
    light, dark = blocks[":root"], blocks[".dark"]

    # parity: same token names in both blocks
    if set(light) != set(dark):
        print("ERROR token parity: light-only=%s dark-only=%s" % (
            sorted(set(light) - set(dark)), sorted(set(dark) - set(light))))
        sys.exit(1)

    failures = 0
    rows = []
    for block_name, toks, pairs in (("light", light, LIGHT_PAIRS), ("dark", dark, DARK_PAIRS)):
        for fg, bg, minimum, klass in pairs:
            fgv, bgv = toks.get(fg), toks.get(bg)
            if fgv is None or bgv is None:
                print(f"ERROR {block_name}: token {fg} or {bg} missing")
                failures += 1
                continue
            r = ratio(oklch_to_srgb(*fgv), oklch_to_srgb(*bgv))
            ok = r >= minimum
            rows.append((block_name, klass, fg, bg, r, minimum, ok))
            if not ok:
                failures += 1
        for name in GRAPHICS_ON_CARD:
            v = toks[name]
            r = ratio(oklch_to_srgb(*v), oklch_to_srgb(*toks["card"]))
            ok = r >= 3.0
            rows.append((block_name, "graphics", name, "card", r, 3.0, ok))
            if not ok:
                failures += 1

    w = max(len(f"{r[2]} on {r[3]}") for r in rows)
    print(f"{'mode':<6}{'class':<10}{'pair':<{w}}  ratio   min  pass")
    for mode, klass, fg, bg, r, minimum, ok in rows:
        print(f"{mode:<6}{klass:<10}{f'{fg} on {bg}':<{w}}  {r:5.2f}  {minimum:4.1f}  {'PASS' if ok else 'FAIL'}")
    print(f"\n{len(rows)} pairings checked ({len(rows) - failures} pass, {failures} fail)")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
