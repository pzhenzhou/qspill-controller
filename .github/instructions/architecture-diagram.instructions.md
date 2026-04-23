---
applyTo: "**/*.html"
---

# Architecture Diagram Generator

When the user asks for an architecture diagram, system diagram, infrastructure diagram, cloud architecture visualization, network topology, state machine diagram, flow chart, or any technical diagram showing system components and their relationships, generate a **self-contained HTML file** with inline SVG graphics following this design system.

## When to Activate

Trigger on requests like:
- "Create/generate/draw an architecture diagram"
- "Make a diagram showing..."
- "Visualize the architecture of..."
- "Draw a state machine / flow chart / topology for..."

## Output Rules

Always produce a **single self-contained `.html` file** with:
- Embedded CSS (no external stylesheets except Google Fonts)
- Inline SVG (no external images)
- No JavaScript required (pure CSS animations)
- Must render correctly when opened directly in any modern browser

Save the file to the project's `docs/` directory by default, or wherever the user specifies.

## qspill-controller Defaults

When the request is about this repository, prefer diagrams that reflect the actual controller design rather than a generic web/cloud stack.

### System architecture defaults

- Show **qspill-controller** as a **standalone external controller** inside the Kubernetes cluster, not as a Volcano in-tree component.
- Show the main reconcile flow as:
  `Informer watches -> policy-keyed workqueue -> snapshot.Builder.Build -> evaluator.Evaluate -> action.Apply`
- The watch layer should usually include:
  - PodGroup watch
  - Pod / Event watch
  - Node watch
  - Queue watch
  - ConfigMap watch
- Show **leader election** before active reconciliation when drawing runtime/control-plane views.
- Show configuration coming from the controller ConfigMap (`qspill-controller-config`) into the immutable policy registry / registry store.
- Show the action layer as two possible implementations:
  - `Nope` dry-run YAML output
  - `Patch` server-side apply to the Volcano Queue

### State and ownership defaults

- The controller owns only:
  - `Queue.spec.affinity.nodeGroupAffinity`
  - `spill.example.com/state`
  - `spill.example.com/condition-since`
  - `spill.example.com/decision-hash`
- Do **not** depict the controller as evicting pods, modifying Volcano internals, or writing `spec.capability`.
- If the diagram shows policy resolution, represent it as:
  - PodGroups map to policies by `PodGroup.spec.queue`
  - Pods map through Pod annotation -> PodGroup -> Queue -> policy
  - Nodes map through the configured nodegroup label key

### State machine defaults

- The state machine has exactly **two states**:
  - **Steady**: dedicated nodegroup only
  - **Spill**: dedicated + overflow nodegroups, dedicated preferred
- Show Steady -> Spill triggers as:
  - autoscaler exhausted
  - stale pending
- Show Spill -> Steady trigger as:
  - overflow drained / switchback
- When transitions are annotated, use the real cooldown semantics:
  - Steady -> Spill uses `TimeOn`
  - Spill -> Steady uses `TimeOff * (1 + Hysteresis)`
- If showing restart safety, note that cooldown state is reconstructed from Queue annotations.

### Component naming defaults

Prefer real package/component names from this repo when labeling boxes:
- `pkg/watcher`
- `pkg/snapshot`
- `pkg/evaluator`
- `pkg/reconcile`
- `pkg/action`
- `pkg/config`
- `pkg/leader`

For deployment-oriented diagrams, use the namespace `qspill-controller-system` and place generated HTML files under `docs/` unless the user asks for another path.

## Design System

### Color Palette

Use these semantic colors consistently for component types:

| Component Type   | Fill (rgba)                    | Stroke                    |
|------------------|--------------------------------|---------------------------|
| Frontend         | `rgba(8, 51, 68, 0.4)`        | `#22d3ee` (cyan-400)      |
| Backend/Service  | `rgba(6, 78, 59, 0.4)`        | `#34d399` (emerald-400)   |
| Database/Storage | `rgba(76, 29, 149, 0.4)`      | `#a78bfa` (violet-400)    |
| AWS/Cloud        | `rgba(120, 53, 15, 0.3)`      | `#fbbf24` (amber-400)     |
| Security         | `rgba(136, 19, 55, 0.4)`      | `#fb7185` (rose-400)      |
| Message Bus      | `rgba(251, 146, 60, 0.3)`     | `#fb923c` (orange-400)    |
| External/Generic | `rgba(30, 41, 59, 0.5)`       | `#94a3b8` (slate-400)     |

### Typography

Use JetBrains Mono for all text:
```html
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
```

Font sizes: 12px component names, 9px sublabels, 8px annotations, 7px tiny labels.

### SVG Background

Dark theme (`#020617` slate-950) with subtle grid:
```svg
<pattern id="grid" width="40" height="40" patternUnits="userSpaceOnUse">
  <path d="M 40 0 L 0 0 0 40" fill="none" stroke="#1e293b" stroke-width="0.5"/>
</pattern>
```

### SVG Arrowhead Marker

```svg
<marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
  <polygon points="0 0, 10 3.5, 0 7" fill="#64748b" />
</marker>
```

### Component Box Pattern

```svg
<!-- Opaque background to mask arrows passing behind -->
<rect x="X" y="Y" width="W" height="H" rx="6" fill="#0f172a"/>
<!-- Styled component on top -->
<rect x="X" y="Y" width="W" height="H" rx="6" fill="FILL_COLOR" stroke="STROKE_COLOR" stroke-width="1.5"/>
<text x="CENTER_X" y="Y+20" fill="white" font-size="11" font-weight="600" text-anchor="middle">LABEL</text>
<text x="CENTER_X" y="Y+36" fill="#94a3b8" font-size="9" text-anchor="middle">sublabel</text>
```

### Visual Element Rules

- **Component boxes**: Rounded rectangles (`rx="6"`), 1.5px stroke, semi-transparent fills
- **Security groups**: Dashed stroke (`stroke-dasharray="4,4"`), transparent fill, rose color
- **Region boundaries**: Dashed stroke (`stroke-dasharray="8,4"`), amber color, `rx="12"`
- **Auth/security flows**: Dashed lines in rose color (`#fb7185`)
- **Message buses**: Small connector elements between services, orange color

### Arrow Z-Order (Critical)

Draw connection arrows **early** in the SVG (after the background grid) so they render behind component boxes. Since component fills are semi-transparent, always draw an opaque `fill="#0f172a"` rect before the styled component rect to fully mask arrows passing behind.

### Spacing Rules (Critical)

- Standard component height: 60px for services, 80–120px for larger components
- Minimum vertical gap between components: 40px
- Inline connectors (message buses): Place IN the gap between components, not overlapping

Example vertical layout:
```
Component A: y=70,  height=60  → ends at y=130
Gap:         y=130 to y=170   → 40px gap, place bus at y=140 (20px tall)
Component B: y=170, height=60  → ends at y=230
```

### Legend Placement (Critical)

Place legends **OUTSIDE** all boundary boxes (region, cluster, security group).
- Calculate where all boundaries end (y + height)
- Place legend at least 20px below the lowest boundary
- Expand SVG viewBox height if needed

## HTML Page Structure

1. **Header** — Title with pulsing cyan dot indicator, subtitle
2. **Main SVG diagram** — Contained in rounded border card
3. **Summary cards** — Grid of 3 cards below diagram highlighting key architecture aspects
4. **Footer** — Minimal metadata line

## Base Template

Use this as the starting skeleton and customize components, arrows, cards:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>TITLE Architecture Diagram</title>
  <link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: 'JetBrains Mono', monospace;
      background: #020617;
      min-height: 100vh;
      padding: 2rem;
      color: white;
    }
    .container { max-width: 1200px; margin: 0 auto; }
    .header { margin-bottom: 2rem; }
    .header-row { display: flex; align-items: center; gap: 1rem; margin-bottom: 0.5rem; }
    .pulse-dot {
      width: 12px; height: 12px; background: #22d3ee;
      border-radius: 50%; animation: pulse 2s infinite;
    }
    @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
    h1 { font-size: 1.5rem; font-weight: 700; letter-spacing: -0.025em; }
    .subtitle { color: #94a3b8; font-size: 0.875rem; margin-left: 1.75rem; }
    .diagram-container {
      background: rgba(15, 23, 42, 0.5);
      border-radius: 1rem; border: 1px solid #1e293b; padding: 1.5rem; overflow-x: auto;
    }
    svg { width: 100%; min-width: 900px; display: block; }
    .cards {
      display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
      gap: 1rem; margin-top: 2rem;
    }
    .card {
      background: rgba(15, 23, 42, 0.5);
      border-radius: 0.75rem; border: 1px solid #1e293b; padding: 1.25rem;
    }
    .card-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.75rem; }
    .card-dot { width: 8px; height: 8px; border-radius: 50%; }
    .card-dot.cyan { background: #22d3ee; }
    .card-dot.emerald { background: #34d399; }
    .card-dot.violet { background: #a78bfa; }
    .card-dot.amber { background: #fbbf24; }
    .card-dot.rose { background: #fb7185; }
    .card h3 { font-size: 0.875rem; font-weight: 600; }
    .card ul { list-style: none; color: #94a3b8; font-size: 0.75rem; }
    .card li { margin-bottom: 0.375rem; }
    .footer { text-align: center; margin-top: 1.5rem; color: #475569; font-size: 0.75rem; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <div class="header-row">
        <div class="pulse-dot"></div>
        <h1>TITLE Architecture</h1>
      </div>
      <p class="subtitle">SUBTITLE</p>
    </div>

    <div class="diagram-container">
      <svg viewBox="0 0 1000 680">
        <defs>
          <marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
            <polygon points="0 0, 10 3.5, 0 7" fill="#64748b" />
          </marker>
          <pattern id="grid" width="40" height="40" patternUnits="userSpaceOnUse">
            <path d="M 40 0 L 0 0 0 40" fill="none" stroke="#1e293b" stroke-width="0.5"/>
          </pattern>
        </defs>
        <rect width="100%" height="100%" fill="url(#grid)" />

        <!-- ARROWS FIRST (render behind components) -->

        <!-- COMPONENTS (render on top of arrows) -->

        <!-- LEGEND (outside all boundaries) -->

      </svg>
    </div>

    <div class="cards">
      <!-- 3 summary cards -->
    </div>

    <p class="footer">Project • Metadata</p>
  </div>
</body>
</html>
```

## Diagram Types

### Architecture / System Diagram
- Position components in logical layers (clients → ingress → services → data)
- Use region/cluster boundaries to group related components
- Show data flow with labeled arrows

### State Machine Diagram
- Each state is a component box
- Transitions are labeled arrows between states
- Use different colors for initial, intermediate, and terminal states
- Highlight the happy path with brighter strokes

### Flow Chart / Sequence Diagram
- Arrange steps top-to-bottom or left-to-right
- Use decision diamonds (rotated rectangles) for branch points
- Label edges with conditions
- Use dashed arrows for async or optional flows

## Attribution

Design system based on [Architecture Diagram Generator](https://github.com/CocoonAI/architecture-diagram-generator) by Cocoon AI (MIT License).
