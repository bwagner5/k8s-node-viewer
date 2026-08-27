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
┏ ip-10-0-4-91 ━━━━ ▼ DRAINING ━┓ ╭ ip-10-0-2-13 ┄┄ ◇ PROVISIONING ┄╮
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
./knv --price-annotation example.com/hourly-price
./knv --demo                   # simulated cluster, no cluster required
./knv --demo --playback-speed 0.5
```

`--demo` runs a self-contained simulation. Use it to rehearse: the seed is
fixed, so `--demo-seed 42` replays the same cluster every time, and
`--demo-autopilot=false` keeps the screen still until you trigger events
yourself with `+` (scale up), `-` (drain), `x` (churn pods) and `b` (submit a
burst of pods with no node, which piles up in the pending meter until there is
somewhere to put them).

## Keys

| Key | |
|---|---|
| `↑↓←→` / `hjkl` | move the selection |
| `enter` | node details and events; `esc` or `backspace` back |
| `pgup` `pgdn`, `g` `G` | scroll |
| `z` / `Z` | zoom in / out; `0` back to fit |
| `:` | command bar |
| `/` | filter nodes by name |
| `\` | clear all filters |
| `v` | cycle pods → nodes → dense |
| `p` | pause/resume the cluster timeline |
| `[` | rewind five seconds (repeat to go farther) |
| `r` | discard buffered history and jump to real-time |
| `d` | dense table mode |
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
:node <regex|all>           filter nodes by name
:zoom <in|out|fit|max>      card size; max is one node, full screen  (:z)
:mode <pods|nodes|dense>    change the view                     (:m)
:sort <key>                 name, cpu, mem, pods, age, nodepool, type
:speed <0x…1x|realtime>     cluster playback rate; 0x pauses
:pause / :resume            pause or resume at the previous rate
:rewind <duration>          rewind by e.g. 5s or 20s             (:back)
:theme <dark|light>         palette
:legend <on|off>            colour legend
:quit                       exit
:clear                      drop every filter
:help                       show help
```

### Slow-motion playback

Playback slows the cluster timeline without slowing the controls. `:speed 0.5x`
makes observed states and their animations last twice as long; `:speed 0.25x`
makes them last four times as long. `:speed 1x` continues normally from the
currently displayed snapshot, preserving the accumulated delay. Use `r` or
`:speed realtime` to discard the backlog and jump to the newest state.

`p` pauses or resumes at the previous rate. Snapshots continue buffering while
paused, and the status line shows the playback rate, exact cluster timestamp,
and precise distance behind the cluster in one readout. Node-detail payloads are
sampled and buffered alongside the list so opening a node remains aligned with
the delayed view. If detail history is not available, the pane labels itself as
live instead of presenting current data as historical.

While the viewer is in real-time it keeps a rolling 30-second snapshot window,
bounded by the same memory limit. If something flashes past, press `p` and then
`[` to step back five seconds; repeat `[` or use `:rewind 20s` to go farther.
Each `[` press shows a brief centred seek badge, with rapid presses accumulating
the displayed amount just like a video player.
Press `p` again to replay from there at the previous speed, or `r` to discard the
backlog and return to the newest state. List snapshots are retained continuously;
node-detail history still begins sampling only after playback becomes delayed so
the rewind buffer does not add a stream of API requests per node.

History defaults to ten minutes and approximately 256 MiB. Configure the limits
with `--history-duration` and `--history-memory`. If the snapshot buffer reaches
either limit, the viewer returns to real-time with a warning rather than silently
dropping lifecycle transitions.

## Modes

- **pods** — every pod is a cell, sized by CPU request, coloured by state, with
  CPU and memory meters below. The kube-ops-view view.
- **nodes** — no pod cells. The whole box becomes two tall gauges with large
  percentages and a pod count. This is what reads from the back of a room.
- **dense** — one row per node, for clusters with more nodes than boxes will fit.
  This is the table, and it carries the `CONS` column (below). Columns are dropped
  as the terminal narrows, in increasing order of value: nodepool, then instance
  type, then state, then `CONS`.

### Consolidatable (CONS)

The dense table's `CONS` column is `y` when Karpenter's last word on the node was
`ConsolidationCandidate` — it is willing to remove this node — `n` when the last
word was `Unconsolidatable`, and `·` when it has not said. The detail pane shows
the same verdict with the message and the age behind it.

There is no field for this anywhere in the API. The decision lives in Karpenter's
disruption loop and surfaces only as an event, so this is a **reported fact with
an age**, not a computed one. Two consequences worth knowing:

- A verdict expires after 30 minutes of silence and reverts to `·`. Karpenter
  re-evaluates continuously, so silence that long means the last answer is no
  longer evidence of anything — and a stale `y` on a node that has since filled up
  would be worse than admitting we do not know.
- `·` means *unjudged*, not *no*. A cluster without Karpenter shows `·` on every
  row, because nothing is making this decision.

Karpenter reports against the NodeClaim it is reasoning about rather than the
Node, and the event routinely arrives before the Node object does. The store keys
verdicts by whichever object was named and joins them to nodes when it builds a
snapshot, taking the more recent of the two; an older verdict never overwrites a
newer one, which is what stops the column flickering on every informer resync.

### Pending pods

The third header meter is the scheduling backlog: pods that exist and have no
node. It is the one thing on screen that no node box can show, because a pod with
nowhere to run is drawn nowhere.

```
 14 nodes  cpu ▄▄▄▄▄▄▄▄▄▄▄▄▖▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄  29%  65 / 224
 $10.75/hr mem ▄▄▄▄▖▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄  11%  100Gi / 896Gi
          pend ▄▄▄▄▄▄▄▄▄▄▄▄▄▖▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄  32%  28 pending · 7 unschedulable
```

Two colours, one bar. Amber is *waiting* — normal, and usually gone by the next
frame. Red is *unschedulable*: the scheduler has looked at the pod and refused
it (`PodScheduled=False`, reason `Unschedulable`), which is the only signal here
that means the cluster is out of room. Red is a subset of the bar rather than a
second bar, so its length is the backlog and the red part is how much of it is
stuck.

The bar is measured against every pod in the cluster, placed or not, because
"40 pending" says nothing until you know whether the cluster runs 50 pods or
5000. Anything non-zero claims at least one cell: a small backlog is small, but
it must not look like none.

This is the meter to watch during a scale-up: pods go amber, turn red, Karpenter
answers with a provisioning box in the grid, and the red drains away as the new
node registers and absorbs them.

### Node details

`enter` opens the selected node's describe pane: what it is, where it is, what it
reserves, its conditions and taints, the pods on it — and its **events, newest
first**. That last part is why the pane exists. A node's story is an ordered one
("launched, registered, went NotReady, was disrupted, evicted eleven pods") and
the grid can only ever show you the last frame of it. The newest event is on the
first line, so the thing that just happened is what you land on; read down for
how it got there.

`esc`, `backspace` or `q` returns to the grid, and it comes back *exactly* as you
left it: same filters, same sort, same zoom, same pan, same selection. That is
not restoration — the pane is a separate screen and opening it changes no view
state at all, so there is nothing to put back and nothing to get subtly wrong.

The pane scrolls (`↑↓ jk`, `pgup`/`pgdn`, `g`/`G`, or the wheel) and re-reads
itself every few seconds, because on a draining node the events arrive while you
are reading them; `ctrl+r` re-reads live detail immediately. Events are fetched
through bounded describe requests rather than watched: on demand in real-time,
and periodically for each node while playback history is active. A cluster-wide
event watch would cost more than the rest of the viewer put together. On a
Karpenter cluster the NodeClaim's events
are merged in and tagged `(claim)` — the launch and disruption decisions are
recorded there, not on the Node, so a pane without them shows only kubelet's half
of the conversation.

Open the pane on a **provisioning** box and it describes the NodeClaim — its
capacity, labels, provider ID and provisioning conditions (`Launched`,
`Registered`, `Initialized`, `Ready`) alongside the events — because there is no
Node object to describe yet. When kubelet registers, the pane **follows the claim
into the Node it became**: the two objects have different names, so a pane that
did not follow would announce that the node had gone at the exact moment it
succeeded. The claim's content stays up until the Node's first read lands, and
the cursor moves with it, so `esc` returns to the right card.

In `--demo` the simulation records its own history as it runs, so the pane works
in a rehearsal exactly as it does against a cluster.

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
| provisioning | dashed, violet, breathing | `◇ PROVISIONING` |
| cordoned | thin, blue | `⏸ CORDONED` |
| draining | heavy, amber, pulsing + diagonal hatch on free capacity | `▼ DRAINING` |
| terminating | heavy, red, fast pulse | `✕ TERMINATING` |
| not ready | heavy, orange | `! NOT READY` |

Pod cells encode state in the glyph as well as the colour: `█` running,
`▒` pending, `▓` terminating, `╳` failed.

### What the colours inside a node mean

Exactly two visual languages, and the legend labels both:

```
node   [✓ READY] [◇ PROVISIONING] [⏸ CORDONED] [▼ DRAINING] [✕ TERMINATING] [! NOT READY]
pods   [● RUNNING] [○ PENDING] [◐ TERMINATING] [× FAILED]  · cell size = cpu request
```

- **Status chip** = the node's current phase. It keeps the full label wherever
  the card width permits and at least the phase icon when extremely compact;
  colour and border treatment reinforce it rather than requiring a legend lookup.
- **Recent footer** = observed phase changes from the last ten seconds. The
  current chip never lags the cluster, while a quick cordon or termination stays
  visible long enough to explain during a live demonstration.
- **Fill inside the card** = pod state, and nothing else. Running is one calm
  teal, so a node interior reads as a single packed mass whose *area* is how
  full the node is. Pending, terminating and failed are accents that only show
  up when something is happening.
- **Cell size** = the pod's CPU request as a fraction of allocatable.
- **Glyph** = the same state as the colour, so it survives a bad projector.
- **Meter colour** = how well packed the thing is, and it runs *toward green*.
  See below.

### The utilisation ramp runs toward green

The CPU and memory meters — on every card, in the header, and in the detail pane
— are coloured by a ramp that goes slate → blue → cyan → **green** as they fill.
A node at 95% is bright green.

That is the opposite of a monitoring dashboard, on purpose. This tool is about
bin-packing and what a cluster costs: an empty node is money being burned, and a
full one is a provisioner doing its job. Colouring 90% red would tell the
audience the wrong story every time a scale-up finished successfully.

Red and amber are still on screen, and they still mean something is wrong — a
failed pod, a draining node, an unschedulable backlog in the pending meter. They
just no longer double as "this node is busy". Luminance climbs monotonically
along the ramp, so the fill level is legible even where the hue is not.

**Stacked meters are half-height bars.** Where meters sit a row apart — the
header's three, one per row in the dense table, four in the detail pane — the
bar is drawn as a `▄` glyph rather than as a filled cell background. Two
background fills on adjacent rows have no edge between them and merge into a
single two-row block of two colours, which reads as neither bar. A half-height
bar has a top edge, and that edge is the separation, at a cost of no rows at
all. The node cards keep the full-cell fill: their figures have to sit *on* the
meter, which a glyph bar cannot support, and there the text breaks the colour up
anyway.

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

Ages count in **seconds up to 99s** before rounding to minutes, rather than
switching at 60s the way `kubectl` does. An instance takes about ninety seconds
to reach Ready, and that is the wait the second-by-second resolution is for.

Utilisation is summed pod **requests**, which is what the scheduler acts on and
needs nothing installed. Keeping this fixed gives every meter one meaning.

Node cost is shown when a node or NodeClaim carries a numeric hourly-price annotation; no cloud
pricing table is bundled. The default is Oxide's `karpenter.oxide.computer/hourly-price`. Use
`--price-annotation example.com/hourly-price` for a provider that publishes the value under another
annotation key.

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

There are two meter primitives, and which one is right depends on whether text
has to sit on the bar. `hMeter` fills the cell *background*, so labels and
percentages can be drawn over it with per-cell contrast — that is what the node
cards need, because there is no room beside a 28-column card meter for its
figures. `hMeterRail` draws the bar as a half-height glyph (`▄`) instead, which
gives it a top edge and costs no rows; nothing may be drawn on top of it, so its
percentage sits beside it. Everywhere meters stack a row apart — the header's
three, the dense table's one-per-node, the detail pane's four — uses rails.

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

**Events are read two ways, on purpose.** The detail pane reads a node's events
through a plain API call and a `ui.Describer` the sources implement — on demand
while live, and as bounded periodic samples while playback history is active.
Events are the highest-volume resource in a cluster by a wide margin, and an
informer for all of them would be the most expensive thing here. Sampling only
while it serves an open or delayed view keeps that cost proportional to the
feature using it.

The `CONS` column cannot work that way: it needs a verdict for every node,
continuously, and polling every node's events to fill a table column would be
absurd. So it gets a watch — but a `fieldSelector=reason=…` one, a separate
informer per reason, because field selectors have no set operator and a
client-side filter over all events is the very thing being avoided. The API server
sends a handful of objects per node and nothing else. Which read a feature needs is
decided by whether it wants one node on demand or every node all the time.

### Extending it

- **A command:** one entry in `registry` (`internal/ui/commands.go`). Help text
  and the help overlay are generated from it, so they cannot drift.
- **A completion source:** one case in `Model.candidates`.
- **Colours:** `internal/theme`. `Theme` is the whole surface; `theme.Use` swaps.
- **Card geometry:** the sizing constants at the top of `internal/ui/layout.go`.
- **Node-state styling:** `borderStyle` in `internal/ui/nodebox.go`.
- **A section in the detail pane:** one `append…` helper called from
  `Model.detailLines` (`internal/ui/detail.go`); lines are spans of coloured text,
  so nothing there can emit a row of the wrong width.
- **Another provisioner:** the well-known label and taint constants at the top of
  `internal/source/kube/convert.go`, plus `derivePhase`.
- **Another consolidation verdict reason:** one entry in `consolidationReasons`
  (`internal/source/kube/consolidation.go`); a watch is started per entry.

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

Read-only. `get`/`list`/`watch` on nodes and pods; `list`/`watch` on events for
the detail pane and the `CONS` column (without it the pane still opens and says
why the events are missing, and the column stays `·`); the same on
`karpenter.sh` nodepools and nodeclaims if present; and
`metrics.k8s.io` if you want actual usage. The viewer never writes to a real
cluster — the interactive scale/drain commands are rejected unless the data is
coming from `--demo`.
