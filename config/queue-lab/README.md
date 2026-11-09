# config/queue-lab

The queue experiment's cluster footprint, and nothing else: namespace `queue-lab`, one
`SandboxTemplate`, and `SandboxWarmPool` `queue-lab-pool` with a pinned replica count.

Apply it with the same path every other chart takes:

```bash
helm template queue-lab config/queue-lab | kubectl apply -f -
```

**This chart owns `queue-lab` only.** Namespace `sandbox-demo`, its pools and its claims belong
to `config/sandbox` and `config/session-lab`, and a queue run must never touch them: a queue
experiment that takes a replica a persist run is waiting on produces two unusable runs. The
names are separate on purpose — separate namespace, separate pool, separate output directory
(`docs/queue-lab-runs/`) — so a leaked claim is attributable to the experiment that leaked it.

Replicas are pinned rather than autoscaled because the A/B compares ordering policies. A pool
that resizes while the policy is being compared changes capacity and ordering in the same run,
and the result would not say which one moved the tail. Sizing a warm pool from queue depth is
its own experiment, and it needs the depth signal that `slots.Pool` currently keeps to itself.
