## qspill-controller

A standalone external controller that flips a Volcano `Queue`'s
`spec.affinity.nodeGroupAffinity` between **Steady** (dedicated nodegroup only)
and **Spill** (dedicated + overflow, dedicated preferred) states based on
reactive signals — autoscaler-exhaustion events and stuck-pending Pods.

The controller never evicts pods, never writes `spec.capability`, and never
modifies Volcano itself. All decisions are realised purely by changing what
the scheduler considers for **future** placements.

## Build

```sh
go build -o bin/qspill-controller ./cmd/controller
```
