# Deep Research Prompt: A Pareto Platform Cockpit for Kubernetes

You are researching this as a builder and platform engineer, not as a vendor
marketer. Treat this as a standalone experimental research question. Do not
infer requirements from unrelated system, blog, or documentation work.

## Research question

Do platform-engineering products already provide a centralized operational
cockpit that combines:

- Kubernetes workload visibility and control;
- Cilium/Hubble network visibility and policy actions;
- deployment and promotion status from Argo CD/Kargo or equivalent;
- distributed traces from OpenTelemetry/Tempo or equivalent;
- simple, explicit service-to-service connection management;
- safe blast-radius isolation and chaos-testing controls;
- one API/UI instead of forcing engineers through many separate dashboards?

The goal is to determine whether this is an existing product category, a
composable stack, or a small custom control plane that is commonly built
internally.

## Required comparison

Compare the responsibilities and boundaries of these categories:

1. PaaS control planes such as Railway, Render, Fly.io, Northflank, and similar
   products.
2. Internal developer platforms such as Backstage, Port, Humanitec, Qovery,
   and similar products.
3. Kubernetes control-plane UIs such as Rancher, Headlamp, OpenLens, and
   similar products.
4. Network platforms such as Cilium/Hubble, service meshes, and API gateways
   such as Kong.
5. Deployment platforms such as Argo CD, Argo Rollouts, and Kargo.
6. Observability platforms such as OpenTelemetry, Grafana, and Tempo.
7. Automation platforms such as n8n, Temporal, and Conductor.

For every category, state what it owns, what it only observes, what it can
mutate, and what it deliberately does not control. Explicitly distinguish:

- data-plane request routing;
- infrastructure/workload control;
- deployment promotion;
- workflow/business-process execution;
- observability;
- developer-facing aggregation.

## Concrete scenario to analyze

Assume an engineer wants to make `service-a` call `service-b` in the `staging`
environment without manually editing five different systems.

Explain the smallest safe implementation for:

1. declaring the requested connection;
2. validating ownership, environment, port, identity, and authorization;
3. creating or updating service discovery/routing;
4. applying Cilium network policy;
5. configuring authentication or mTLS when needed;
6. rolling out configuration when hot reload is unavailable;
7. observing the result in network flows, logs, metrics, and traces;
8. expiring, revoking, or rolling back the connection.

Compare three approaches:

- direct application-to-application configuration;
- an API gateway such as Kong;
- a declarative platform API/controller backed by Kubernetes and Cilium.

State which approach you recommend for a small team and why.

## Blast radius and isolation

Analyze the practical controls available after Pods exist:

- namespaces and environment boundaries;
- ServiceAccounts and RBAC;
- Cilium default-deny and allow-list policy;
- resource requests, limits, quotas, and priority;
- scheduling, taints, node pools, and cluster boundaries;
- canary, blue/green, rollback, and progressive delivery;
- Pod disruption and eviction behavior;
- workload isolation during an incident;
- controlled chaos actions such as deletion, latency, packet loss, and node
  failure.

For each control, identify whether it limits network blast radius, resource
blast radius, deployment blast radius, data blast radius, or security blast
radius. Include the residual failure modes and the required authorization.

## Pareto analysis

Produce a strict 80/20 recommendation. Start with the smallest useful product
that gives one operational view and a few safe actions. Identify which features
should be deferred until there is demonstrated pain.

Evaluate this candidate MVP:

- one authenticated cockpit UI/API;
- read-only aggregation of Kubernetes, Cilium/Hubble, deployment status, and
  Tempo traces;
- typed actions for deploy, rollback, scale, isolate, and create a service
  connection;
- audit log for every mutation;
- read-only by default and narrowly scoped action permissions;
- no replacement of Kubernetes, Cilium, Argo/Kargo, or Tempo controllers.

Explicitly reject unnecessary features such as a universal workflow engine,
arbitrary GraphQL mutations, unrestricted cluster-admin credentials, automatic
service graph inference, or a new service mesh unless evidence shows they are
required.

## Evidence requirements

- Prefer primary sources: official product documentation, architecture
  documents, APIs, source repositories, and published technical papers.
- Verify current product capabilities, licensing, deployment model, and API
  limitations as of the research date.
- Do not treat marketing claims as proof of control-plane capability.
- Separate sourced facts from inference and opinion.
- Include source links next to material claims.
- Call out products that aggregate links to other tools versus products that
  actually reconcile state.

## Deliverable

Produce:

1. an executive answer to whether this product already exists;
2. a capability comparison matrix;
3. a concrete `service-a -> service-b` control flow;
4. a blast-radius/isolation table;
5. a minimal reference architecture;
6. a Pareto MVP and explicit non-goals;
7. a recommendation of buy, compose, or build;
8. the first three implementation milestones and their acceptance tests;
9. unresolved risks and evidence gaps.

End with one direct decision sentence: what a small platform team should build
or adopt first, and what it should deliberately not build.
