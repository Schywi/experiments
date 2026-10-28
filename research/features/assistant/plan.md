# Plan — real links, LLM queries, chat/investigate split

Scope, in order:

1. No fake hosts. Delete the `neo4j.home.arpa` default in `provenance.py`; that
   link exists only when `NEO4J_UI_URL` is set.
2. Cilium link. Cilium's UI is the Hubble UI; the existing `hubble` link is the
   "Open in Cilium" link. It fires whenever a fact carries a namespace.
3. Argo CD and Kargo links, by resource, inside `provenance.py`:
   `ARGOCD_URL`, `KARGO_URL`. Missing env means no link.
4. The LLM writes the PromQL. No bundle table, no canned queries. One plan LLM
   call per investigate question picks the tools and their arguments.
5. Chat is chat. `/chat` uses no tools. `/investigate` uses tools.
6. GPU assistant only (`assistant-e4-gpu`, image `:laya-exp`). The CPU deployment
   is not touched.

Not in scope: new tools, dependency chains, new facts, sandbox-specific work.

Assumptions:

- No new tools. A link is built only from names already present in a fact; when
  no usable name exists, the link points at that UI's overview.
- Hosts come from env, never literals in code.
- Removing the bundles costs one plan LLM call per investigate question.
