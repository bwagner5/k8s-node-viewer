# k8s-node-viewer (`knv`)

A full-screen terminal visualiser for Kubernetes nodes and pods, built for
projecting during a presentation.

Every node is a box. The box interior fills to show utilisation, each pod is a
cell sized by its CPU request, and the lifecycle events worth showing off —
Karpenter provisioning a node, a node draining, a node being deleted — are
animated rather than merely reported.

It takes the box-per-node idea from
[kube-ops-view](https://github.com/hjacobs/kube-ops-view) and the
capacity-and-cost framing from
[eks-node-viewer](https://github.com/awslabs/eks-node-viewer), and adds live
k9s-style filtering plus animation.

```
┏ ip-10-0-4-91 ━━━━━━━ ▼ drain ━┓ ╭ ip-10-0-2-13 ┄┄┄┄┄┄┄┄ ◇ new ┄╮
┃███████▒▒▒▒▒▒▒▒▒▒▒▓▓▓▓▓▓▓▓▓▓▓▓┃ ┊                              ┊
┃▓▓▓▓ ╱  ╱  ╱  ╱  ╱  ╱  ╱  ╱  ╱┃ ┊                              ┊
┃ ╱  ╱  ╱  ╱  ╱  ╱  ╱  ╱  ╱  ╱ ┃ ┊                              ┊
┃ cpu        6.8/16        42% ┃ ┊ cpu           0/16        0% ┊
┃ mem     10.5Gi/64.0Gi    16% ┃ ┊ mem       0/64.0Gi        0% ┊
┗━ general · m5.4xlarge ━ 6p 9m ┛ ╰┄ spot-batch · m5.2xlarge ┄ 0s ╯
```

## Running

```sh
go build -o knv ./cmd/knv

./knv                          # current kube context
./knv --context prod --mode nodes
./knv --demo                   # simulated cluster, no cluster required
```

`--demo` runs a self-contained simulation. Use it to rehearse: the seed is
fixed, so `--demo-seed 42` replays the same cluster every time, and
`--demo-autopilot=false` keeps the screen still until you trigger events
yourself with `+` (scale up), `-` (drain) and `x` (churn pods).

## Keys

| Key | |
|---|---|
| `↑↓←→` / `hjkl` | move the selection |
| `pgup` `pgdn`, `g` `G` | scroll |
| `z` / `Z` | zoom in / out; `0` back to fit |
| `:` | command bar |
| `/` | filter nodes by name |
| `\` | clear all filters |
| `p` | pods ⇄ utilisation-only |
| `d` | dense table mode |
| `u` | meters from requests ⇄ actual usage |
| `s` / `S` | cycle sort / reverse |
| `l` | toggle legend |
| `?` | help (`↑↓` scrolls it; any other key closes) |
| `q` | quit |

## Mouse and trackpad

| Gesture | |
|---|---|
| two-finger scroll | zoom on the node under the pointer |
| `ctrl` / `option` / `shift` + two-finger scroll | scroll the grid |
| horizontal two-finger scroll | pan sideways when zoomed in |
| click or drag | select a node |

**A pinch cannot work, and it is not your terminal's fault.** macOS delivers a
magnify gesture to the *application* — here, the terminal emulator — and there is
no escape sequence for "the user pinched", so nothing is forwarded to the program
running inside it. Most emulators use the pinch to resize their own font.

So the bare two-finger scroll zooms, because it is the closest thing to the
gesture you actually reach for, and scrolling moves to a modified scroll. All
three modifiers are accepted because some emulators swallow `ctrl`+scroll for
font sizing before the application sees it: if one does nothing, try another. On
iTerm2, `option`+scroll is the reliable one.

Zoom accumulates four wheel notches per level. A level is a nineteenth of the
range, so one flick of a dozen notches is a modest change of scale rather than a
leap: at three notches and a fixed 1.4× a level, one flick was a four-fold jump
and the grid lurched instead of zooming.

A zoom gesture zooms about the pointer, the way a map does: the point under it
holds still while everything grows around it. Because the grid is a fixed canvas
(see [Zoom](#zoom)), this is exact — the card you aimed at is still under your
fingers however far you go, and keep flicking and it is what fills the screen.

Two rules keep it honest. **The anchor changes when the pointer moves, and at no
other time**, so a gesture cannot re-aim itself between flicks. And the anchored
card is always kept *whole* on screen: holding the pointer's point perfectly
still is right until the card is nearly as big as the window, at which point it
would hang off an edge and could never be seen entire — you asked to look at that
card and would get two halves of two cards. The constraint costs nothing while
zoomed out and tightens into exact alignment as the card approaches the size of
the window.

A pointer in a gutter, or in the margin beside a zoomed-out canvas, still aims
at the card it is nearest — refusing to re-anchor there left the zoom pinned to a
card the pointer had long since left. A pointer that is not over the grid at all
(the header, the status line) is not treated as an aim: the wheel zooms about the
centre of the screen instead, holding whatever is selected. Mapping a stray
coordinate to "the nearest card" would map it to an edge, and an edge in both
axes is a corner that every further notch drives deeper into.

Dense mode has no card geometry to scale, so there the wheel keeps scrolling.

**A trackpad's vertical scroll is not vertical.** A plain two-finger flick emits
a horizontal wheel event for very nearly every vertical one — 99 against 100 in
the session that produced this paragraph. Horizontal events therefore drive
nothing during a vertical gesture, and pan the view rather than moving the
selection when they are the gesture.

Moving the *selection* by a card per horizontal notch is what they used to do,
and it made zooming unusable in a way that looked like nothing to do with
horizontal scrolling: every flick walked the selection along the list,
`ensureVisible` panned the grid to chase it, and after a dozen notches the
selection had run to the end of the list — the bottom-left card in a two-column
grid — with the view following it there. It read as "zoom always ends up in the
corner", and the corner was just wherever the list ran out.

For the same reason the zoom anchors on the card the wheel aimed at, held by
name, and **not** on the selection. Anything can move the selection — an arrow
key, a filter, a node arriving — and a zoom that follows it inherits every one of
those.

If the gesture misbehaves on your terminal, two diagnostics:

```sh
KNV_DEBUG_MOUSE=1 ./knv --demo      # status line becomes a live readout
KNV_TRACE=/tmp/knv.log ./knv --demo # every input, and what it did
```

The trace logs keys as well as mouse events, which is the point — a readout of
what the wheel did says nothing about a terminal that is *also* sending arrow
keys. A flick that arrives cleanly looks like `mouse wheel up x=80 y=21`
repeated; one that does not shows `key "down"` interleaved.

Scrolling deliberately brings the selection with it rather than leaving it
behind. That is not only a nicety: a new snapshot arrives up to ten times a
second and scrolls the selected node back into view, so a viewport that
disagreed with the selection was dragged back within 100ms and the wheel looked
broken.

## Commands

`:` opens a k9s-style command bar with fuzzy completion. Typing a command that
takes an argument and pressing Enter opens a picker for it, so `:nodepool` then
Enter lists the Karpenter NodePools to choose from.

```
:nodepool <name|all>        show only nodes from a NodePool     (:np)
:namespace <name|all>       highlight pods in a namespace       (:ns)
:owner <substring|all>      highlight pods by controller name   (:app)
:node <regex|all>           filter nodes by name
:type <instance-type|all>   filter by instance type
:capacity <spot|on-demand>  filter by capacity type             (:cap)
:phase <ready|draining|…>   filter by node phase; repeat to toggle
:zoom <in|out|fit|max>      card size; max is one node, full screen  (:z)
:mode <pods|nodes|dense>    change the view                     (:m)
:pods <on|off>              shorthand for mode pods / mode nodes
:only <on|off>              hide nodes with no matching pod
:daemonsets <on|off>        include DaemonSet pods              (:ds)
:sort <key>                 name, cpu, mem, pods, age, nodepool, type
:util <requests|usage>      what drives the meters
:theme <dark|light>         palette
:legend <on|off>            colour legend
:clear                      drop every filter
```

Namespace and owner filters **highlight** rather than hide: matching pods keep
their colour and everything else goes grey, so you can see where a workload
lives across the whole fleet. `:only on` turns them into hard filters.

## Modes

- **pods** — every pod is a cell, sized by CPU request, coloured by state, with
  CPU and memory meters below. The kube-ops-view view.
- **nodes** — no pod cells. The whole box becomes two tall gauges with large
  percentages and a pod count. This is what reads from the back of a room.
- **dense** — one row per node, for clusters with more nodes than boxes will fit.

### Zoom

`z` and `Z` scale the cards; `0` returns to the automatic layout. Zooming in
grows a card until one node fills the screen — the answer to "what is that node
doing?" from the back of a room — and zooming out shrinks them to the smallest
thing still worth calling a card. Filters and zoom compose: `:nodepool
spot-batch` then `z` a few times is how you hone in on a group.

**The grid is a fixed canvas and the screen is a window onto it.** A card's row
and column come from the node list and the column count, and zoom changes
neither — it changes the size of the cards, and the window pans over the result.
That is the whole design, and it is what makes a zoom aimable: the card under
your pointer stays under your pointer, because nothing has been re-dealt into a
different place.

Reflowing the columns to fill the space was the obvious first implementation and
is what made zooming unusable. Every level moved every card, so the card under
a stationary pointer changed as a side effect of your own gesture, the grid
appeared to jump, and you would arrive somewhere you had never pointed. No
amount of anchoring heuristics on top of a reflowing grid fixes that; the grid
has to stop moving.

The cost is that a zoomed-in canvas is wider than the window — the columns are
still there, off to the sides — so the view pans horizontally as well as
vertically. Moving the selection with the arrow keys pans to it. Only the fitted
layout (`0`) re-columns for the window, which is exactly what "fit" means.

The two ends of the range are defined by what they are for, rather than by a
ratio: fully out is a card at the readable floor, fully in is a card filling the
window. The nineteen levels between are geometric, so every step is the same
proportional change and the range covers the same ground on any terminal and any
cluster size. `z` and `Z` move two levels a press because a keypress wants a
coarser grain than a trackpad. Levels that resolve to the same whole-cell
geometry are skipped when stepping, so a key never appears to do nothing while
there is still room to move.

## Reading the screen

Node phase is carried by border colour **and** border weight **and** a badge, so
it survives a washed-out projector and does not depend on distinguishing hues:

| Phase | Border | Badge |
|---|---|---|
| ready | thin, green | — |
| provisioning | dashed, violet, breathing | `◇ new` |
| cordoned | thin, blue | `⏸ cord` |
| draining | heavy, amber, pulsing + diagonal hatch on free capacity | `▼ drain` |
| deleting | heavy, red, fast pulse | `✕ term` |
| not ready | heavy, orange | `! down` |

Pod cells encode state in the glyph as well as the colour: `█` running,
`▒` pending, `▓` terminating, `╳` failed.

### What the colours inside a node mean

Exactly two visual languages, and the legend labels both:

```
node   ││ ready  ┊┊ provisioning  ││ cordoned  ┃┃ draining  ┃┃ deleting     meter ▁▂▃ 0→100%
pods   ██ running  ▒▒ pending  ▓▓ terminating  ╳╳ failed  · cell size = cpu request
```

- **Border** = node phase. Shown in the legend as border glyphs, never as solid
  blocks, so a phase swatch cannot be mistaken for a pod cell.
- **Fill inside the card** = pod state, and nothing else. Running is one calm
  teal, so a node interior reads as a single packed mass whose *area* is how
  full the node is. Pending, terminating and failed are accents that only show
  up when something is happening.
- **Cell size** = the pod's CPU request as a fraction of allocatable.
- **Glyph** = the same state as the colour, so it survives a bad projector.

Pod colours were briefly hashed from the workload name. That was a mistake: a
colour nobody can decode is worse than no colour, and the hashed hues collided
with the phase palette. One fixed, stated meaning is easier to narrate and keeps
the legend to two rows.

A test (`TestPodPaletteDoesNotCollideWithPhaseColours`) keeps the pod and phase
palettes apart in Lab space.

### Card design

A node is a card, not a rectangle outline:

- Its **body has its own background**, a step up from the page, so an idle node
  still reads as a node instead of as empty screen with a hairline round it.
- The **pod area is an inset well**, darker than the card, so unallocated
  capacity looks like a hole in the card rather than a gap in the layout.
- A **title strip** carries the name and phase badge, tinted toward the phase
  colour. Cards shorter than 8 rows fall back to the name in the top border.
- **Ready borders are muted** toward the card body. On a healthy cluster every
  node is ready, and a screen of saturated green outlines hides the one node
  actually doing something. Non-steady states keep full saturation and pulse.
- Cards have real **gutters** between them and the grid is **centred** in the
  viewport.

A **provisioning** box appears the moment Karpenter creates a NodeClaim, before
the Node object exists. That gap is the most interesting half-second of a
scale-up and is invisible if you only watch Nodes.

Utilisation defaults to summed pod **requests**, which is what the scheduler
acts on and needs nothing installed. Press `u` for actual usage from
metrics-server if it is present.

Node cost is shown when a node or NodeClaim carries the annotation
`node-viewer.oxide.computer/hourly-price`; no cloud pricing table is bundled.

## Architecture

```
   ┌─────────────────────┐
   │ SharedInformers     │  Nodes, Pods (transformed to strip
   │ (client-go)         │  managedFields at the cache boundary)
   ├─────────────────────┤
   │ dynamic informers   │  karpenter.sh NodePools, NodeClaims
   ├─────────────────────┤        as unstructured
   │ metrics poller      │  metrics.k8s.io, on a timer (no watch support)
   └──────────┬──────────┘
              │  convert to model types at the edge
              ▼
       ┌─────────────┐
       │ model.Store │  mutex-guarded index; coalesces event bursts
       └──────┬──────┘
              │  immutable *model.Snapshot, ≤10/sec
              ▼
       ┌─────────────┐        ┌───────────────┐
       │  ui.Model   │◀──────▶│ anim.Registry │  per-entity tracks
       └──────┬──────┘        └───────────────┘
              ▼
        ui.canvas → styled frame
```

The decisions that matter, and why:

**Informers convert at the edge.** Handlers turn `corev1.Node` into
`model.Node` immediately. Nothing downstream can accidentally mutate a shared
cache object, and every layer above the source is testable without a cluster —
which is what the `fake` source relies on.

**One store, coalesced.** A 500-node rollout emits thousands of events per
second. Informer handlers mark the store dirty; `Store.Watch` rebuilds a
snapshot at most every 100ms. Wiring handlers straight to `Program.Send` would
put the renderer under the API server's event rate.

**Snapshots are immutable.** The UI holds one across frames without locking.
Sources keep writing into the store meanwhile.

**Animation state is separate from cluster state.** `anim.Registry` holds what
the *screen* is doing; `model.Snapshot` holds what the *cluster* is doing. A
snapshot arriving mid-flight never resets an in-progress animation, and filters
deliberately do not touch the registry — hiding a node must not make it animate
away, or unhiding it would replay its entrance.

**Deleted nodes are tombstoned, not dropped.** The store keeps a deleted node
for `TombstoneGrace` (3s) so the UI has something to animate out. The store
knows nothing about animation; it just holds the fact a little longer.

**Rendering composites into a cell grid.** `ui.canvas` lets a utilisation fill, a
row of pod cells and a text label be drawn independently and overlap correctly,
with per-cell contrast picking for text over fills. Runs of identical style are
coalesced on output. Meter *fills* and meter *tips* are separate calls so the
sub-cell tip can be drawn after the labels and never lands on top of one.

**The grid is laid out by aspect ratio, not by width.** `computeLayout` scores
each column count by how close the resulting card is to a pleasing shape,
preferring layouts that show every node at once and that leave no ragged last
row, then centres the result. Scoring on width alone made every candidate tie
once the width clamp engaged, which stacked three nodes in the corner of an
otherwise empty screen.

**Frames are exactly `w × h`.** Every section's height is allocated in one place
(`Model.layoutFrame`), chrome is shed as the terminal shrinks, and the frame is
normalised as a final backstop. Tests assert this across sizes, node counts,
modes and mid-animation states.

**Optional APIs degrade, never fail.** metrics-server and Karpenter are probed
once at startup; missing either loses a feature and says so in the header.

### Extending it

- **A command:** one entry in `registry` (`internal/ui/commands.go`). Help text
  and the help overlay are generated from it, so they cannot drift.
- **A completion source:** one case in `Model.candidates`.
- **Colours:** `internal/theme`. `Theme` is the whole surface; `theme.Use` swaps.
- **Card geometry:** the sizing constants at the top of `internal/ui/layout.go`.
- **Node-state styling:** `borderStyle` in `internal/ui/nodebox.go`.
- **Another provisioner:** the well-known label and taint constants at the top of
  `internal/source/kube/convert.go`, plus `derivePhase`.

## Testing

```sh
go test ./...        # includes a headless end-to-end run of the whole pipeline
go test -race ./...
```

`TestEndToEnd` drives the simulated cluster, store, snapshot channel and
bubbletea model together with no terminal involved. `TestFrameGeometry` asserts
frame rigidity across every size/mode/node-count combination.

To eyeball layout without a terminal:

```sh
KNV_DUMP=/tmp/frames.txt go test ./internal/ui/ -run TestDumpFrames
```

## Permissions

Read-only. `get`/`list`/`watch` on nodes and pods; the same on
`karpenter.sh` nodepools and nodeclaims if present; and
`metrics.k8s.io` if you want actual usage. The viewer never writes to a real
cluster — the interactive scale/drain commands are rejected unless the data is
coming from `--demo`.
