---
name: macOS Light SSH Workspace
colors:
  surface: '#f9f9ff'
  surface-dim: '#d0daf2'
  surface-bright: '#f9f9ff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#f0f3ff'
  surface-container: '#e8eeff'
  surface-container-high: '#dfe8ff'
  surface-container-highest: '#d9e3fb'
  on-surface: '#111c2d'
  on-surface-variant: '#45474c'
  inverse-surface: '#273143'
  inverse-on-surface: '#ecf0ff'
  outline: '#76777d'
  outline-variant: '#c6c6cd'
  surface-tint: '#565e71'
  primary: '#000000'
  on-primary: '#ffffff'
  primary-container: '#141b2c'
  on-primary-container: '#7c8498'
  inverse-primary: '#bfc6dc'
  secondary: '#006d3a'
  on-secondary: '#ffffff'
  secondary-container: '#87faac'
  on-secondary-container: '#00743e'
  tertiary: '#000000'
  on-tertiary: '#ffffff'
  tertiary-container: '#001a43'
  on-tertiary-container: '#317ffe'
  error: '#ba1a1a'
  on-error: '#ffffff'
  error-container: '#ffdad6'
  on-error-container: '#93000a'
  primary-fixed: '#dbe2f9'
  primary-fixed-dim: '#bfc6dc'
  on-primary-fixed: '#141b2c'
  on-primary-fixed-variant: '#3f4759'
  secondary-fixed: '#87faac'
  secondary-fixed-dim: '#6bdd92'
  on-secondary-fixed: '#00210d'
  on-secondary-fixed-variant: '#00522a'
  tertiary-fixed: '#d9e2ff'
  tertiary-fixed-dim: '#afc6ff'
  on-tertiary-fixed: '#001a43'
  on-tertiary-fixed-variant: '#004398'
  background: '#f9f9ff'
  on-background: '#111c2d'
  surface-variant: '#d9e3fb'
typography:
  headline-xl:
    fontFamily: Inter
    fontSize: 28px
    fontWeight: '600'
    lineHeight: 36px
    letterSpacing: -0.02em
  headline-lg:
    fontFamily: Inter
    fontSize: 22px
    fontWeight: '600'
    lineHeight: 30px
    letterSpacing: -0.015em
  headline-sm:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: '600'
    lineHeight: 24px
    letterSpacing: -0.01em
  body-lg:
    fontFamily: Inter
    fontSize: 15px
    fontWeight: '400'
    lineHeight: 22px
  body-md:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  body-sm:
    fontFamily: Inter
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
  label-md:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
  label-sm:
    fontFamily: Inter
    fontSize: 11px
    fontWeight: '500'
    lineHeight: 14px
  mono-body:
    fontFamily: JetBrains Mono
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
  mono-sm:
    fontFamily: JetBrains Mono
    fontSize: 11px
    fontWeight: '500'
    lineHeight: 14px
rounded:
  sm: 0.25rem
  DEFAULT: 0.5rem
  md: 0.75rem
  lg: 1rem
  xl: 1.5rem
  full: 9999px
spacing:
  gutter: 1rem
  margin: 1.5rem
  space-xs: 0.25rem
  space-sm: 0.5rem
  space-md: 0.75rem
  space-lg: 1rem
  space-xl: 1.5rem
---

## Brand & Style

This design system blends the precision of modern macOS desktop interfaces (Sonoma/Ventura) with the clean, utilitarian hierarchy of enterprise SaaS design systems. Designed specifically for an SSH host and remote server management workspace, the UI prioritizes clarity, structural density without visual clutter, and seamless transitions between administrative data views and command execution.

The style is characterized by:
- **Calm, High-Contrast Precision:** Crisp off-white panels floating over cool slate-grey neutral backdrops, accented with restrained status indications (active connections, latency alerts, CPU/memory pressure).
- **Desktop Chrome Integration:** Native macOS window controls (traffic lights), unified toolbar breadcrumbs, compact segmented controllers, and persistent sidebars.
- **Architectural Utility:** Dense data grids, monospaced inline metadata, server connection cards, and clean telemetry graphs calibrated for operational peace of mind.

## Colors

The palette relies on subtle, cool-toned neutral tiers with strict functional semantics for SSH infrastructure status.

- **Primary (`#101828`):** Deep charcoal used for primary titles, focused text, high-emphasis action triggers, and primary terminal viewframes.
- **Secondary (`#099250`):** Crisp forest green indicating healthy daemons, established SSH tunnels, zero-packet-loss states, and connected hosts.
- **Tertiary (`#1570EF`):** Core interactive focus rings, active breadcrumbs, selected navigation tabs, and system information callouts.
- **Neutral (`#667085`):** Secondary metadata, latency figures, IP labels, and inactive icon states.

### Surface Tones & Borders
- **App Canvas / Window Base:** `#F8F9FA`
- **Sidebar Surface:** `#F2F4F7`
- **Panel / Card Container:** `#FFFFFF`
- **Subtle Hairline Borders:** `#EAECF0`
- **Input & Hover Borders:** `#D0D5DD`
- **Terminal Viewport (Light Mode):** `#FAFAFB` with hairline `#E4E7EC`
- **Terminal Viewport (Dark Mode option):** `#0F172A` with `#1E293B`

## Typography

The typographic hierarchy uses **Inter** for all graphical chrome, layout hierarchies, navigation, and settings, paired with **JetBrains Mono** for technical identifiers (IPv4/IPv6 addresses, SSH fingerprint hashes, port mappings, command logs, and embedded terminal instances).

- **Headings:** Set in medium/semi-bold weights with tight negative tracking (`-0.01em` to `-0.02em`) to mirror native macOS SF Pro rendering.
- **Labels & Micro-copy:** Rendered with deliberate letter spacing and clear contrast against neutral surfaces (`#344054` on `#FFFFFF` or `#F2F4F7`).
- **Technical & Monospace Data:** Rendered in `JetBrains Mono` with proportional line heights to keep data tables, tunnel mappings, and key signatures aligned across dense server rows.

## Layout & Spacing

The layout is built around a split-view desktop architecture:

- **Mac Window Shell:** Fixed outer wrapper containing standard window controls (traffic lights: 12px circular elements with 8px horizontal separation, anchored 16px from top and left bounds).
- **Navigation Sidebar:** 240px–280px fixed width panel sitting on `#F2F4F7`, separated from the workspace by a 1px border (`#EAECF0`). Internal padding is `space-md` with `space-xs` item vertical spacing.
- **Main Workspace Canvas:** Fluid container with `margin` of `1.5rem` and internal structural card gaps utilizing `space-lg`.
- **Server Grid / Metric Tables:** Fluid responsive columns utilizing `gutter` (1rem). Server card views adapt from 1 column on compact window widths (<840px) up to 3–4 columns on expanded desktop monitors (>1280px).
- **Information Density:** Compact vertical paddings (6px to 10px inside table rows and list items) allow operators to scan 20+ servers or tunnel configurations above the fold.

## Elevation & Depth

Visual hierarchy uses macOS-style ambient light scattering paired with precise 1px stroke outlines, avoiding deep or heavy drop shadows:

- **Window Canvas Level:** Flat background (`#F8F9FA`) with no elevation.
- **Card & Content Container Level:** `#FFFFFF` surface with a crisp `1px solid #EAECF0` border and a delicate dual shadow:
  - Ambient: `0 1px 2px 0 rgba(16, 24, 40, 0.04)`
  - Key: `0 1px 3px 0 rgba(16, 24, 40, 0.06)`
- **Hover & Active Popovers / Dropdowns:** 
  - `0 4px 6px -2px rgba(16, 24, 40, 0.03), 0 12px 16px -4px rgba(16, 24, 40, 0.08)`
  - Border: `1px solid #D0D5DD`
- **Modal Windows & Host Connection Sheets:**
  - `0 8px 8px -4px rgba(16, 24, 40, 0.04), 0 20px 24px -4px rgba(16, 24, 40, 0.1)`
  - Backdrop blur filter: `blur(8px)` with `rgba(16, 24, 40, 0.2)` overlay.

## Shapes

The design system uses a balanced rounding scale (`roundedness: 2`):

- **Window Chrome:** Outer application frame uses 12px (`0.75rem`) border radius, matching native macOS window curvature.
- **Cards & Data Tables:** 8px (`0.5rem`) to 10px (`0.625rem`) corner radius.
- **Buttons & Inputs:** Standardized at 6px (`0.375rem`) to 8px (`0.5rem`) for crisp form boundaries.
- **Pill Badges & Status Indicators:** Full capsule curves (`9999px`) for connection indicators, ping tags, protocol chips (e.g., `SSH`, `SFTP`, `MOSH`), and session badges.

## Components

### Buttons & Trigger Controls
- **Primary Button:** `#101828` background, `#FFFFFF` text, 8px radius, height 36px. Hover transition to `#1D2939`.
- **Secondary / Outline Button:** `#FFFFFF` background, `1px solid #D0D5DD`, `#344054` text. Subtle hover state to `#F9FAFB`.
- **Icon / Window Action Buttons:** 32x32px squircle touch target, transparent default background, hover to `#F2F4F7`, `#475467` icon fill.

### Server Host Rows & Cards
- **Table / List Row:** Height 44px–52px. Hover effect: `#F9FAFB` subtle fill. Left-aligned host alias in `Inter SemiBold`, subtitle containing `user@hostname` in `JetBrains Mono` (`#667085`).
- **Ping & Latency Badge:** Pill shape with a 6px status dot.
  - *Healthy (<50ms):* `#ECFDF3` pill, `#027A48` text, `#12B76A` dot.
  - *Degraded (50-150ms):* `#FFFAEB` pill, `#B54708` text, `#F79009` dot.
  - *Offline / Error:* `#FEF3F2` pill, `#B42318` text, `#F04438` dot.

### Segmented View Switchers & Tabs
- Encapsulated pills inside an `#F2F4F7` background frame with 6px internal padding.
- Selected tab: Pure `#FFFFFF` background, `1px solid #EAECF0` outline, soft ambient shadow (`0 1px 2px rgba(16, 24, 40, 0.06)`), and `#101828` text weight.

### Search & Command Bar
- Global shortcut input (`Cmd + K`) with an inset icon, `#FFFFFF` fill, `1px solid #D0D5DD` border, and a keyboard keycap badge (`⌘K`) on the right in `#F2F4F7` with `#344054` monospace text.

### Integrated Light / Dark Terminal Panel
- **Frame:** Seamless drawer or split panel with a 1px border separator (`#E4E7EC`).
- **Light Terminal:** `#FAFAFB` background, `#101828` default font, `#1570EF` primary prompt indicator, subtle `#D0D5DD` divider rules.
- **Dark Terminal:** `#0F172A` background, `#F8FAFC` default font, `#38BDF8` prompt indicator.