# k8s-node-viewer (`knv`)

A Kubernetes node and pod visualization TUI.

## Running

```sh
go build -o knv ./cmd/knv

./knv                          # current kube context
./knv --context prod --mode nodes
./knv --price-annotation example.com/hourly-price
./knv --demo                   # simulated cluster, no cluster required
./knv --demo --playback-speed 0.5
./knv --record incident.knv    # record a live session
./knv --replay incident.knv    # replay it later, without a cluster
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
| `]` | move forward five seconds through buffered or recorded history |
| `r` | jump to real-time, or to the end of a recording |
| `R` | start or stop recording the current session |
| `d` | dense table mode |
| `s` / `S` | cycle sort / reverse |
| `l` | toggle legend |
| `?` | help (`↑↓` scrolls it; any other key closes) |
| `q` | quit |
