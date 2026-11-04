# experiments — a single-host Kubernetes lab

A Kubernetes lab on one Fedora desktop: native `k3s`, Cilium as the only CNI, an
AMD RX 570 driven through a Go device plugin, three local models, and an
assistant that answers questions about the cluster. Every number in these notes
was measured on that machine.

What is here: the design notes, the measurements, and the record of how the
platform got to its current shape. The graph on the right is the map — drag it,
tap a node to open the note.

## The four collections

| Collection | What it holds | Start at |
|---|---|---|
| Research | Design notes, RFCs, feature contracts, and the session-lifecycle study | [research/](research/) |
| Evolution | One document per phase of the repository, each grounded in commits | [evolution/](evolution/) |
| Benchmark | Measured experiments with the baseline commit recorded next to the numbers | [benchmark/](benchmark/) |
| Blog | Long-form write-ups | [blog/](blog/) |

## The platform as it stands

- State, phases and open work: [ROADMAP.md](ROADMAP.md)
- What runs where, addresses and namespaces: [README.md](README.md)
- The session-lifecycle study, 20 notes: [research/session-lifecycle-elasticity/](research/session-lifecycle-elasticity/)
- The browser console and what it draws: [research/session-lifecycle-elasticity/16-session-wall-ui.md](research/session-lifecycle-elasticity/16-session-wall-ui.md)

## How to read a claim here

Each note states what was measured, what was inferred, and what is still
unknown. A claim that could not be verified in the tree is labelled as such.
Numbers are tied to a run id, a commit, or a file path you can open; where none
of those exist, the note says so.
