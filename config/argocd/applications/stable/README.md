# Stable Argo CD ownership

This directory is an explicit Argo-owned release mode for the Worm
experiment. It is deliberately separate from the existing
`applications/vector.yaml` file and is not installed by the platform bootstrap
or by Tilt.

Choose exactly one ownership mode before deployment:

- **Tilt-local mode:** do not register these Applications. Tilt owns the
  experiment resources and locally imported images.
- **Argo-stable mode:** stop/disable the Tilt Worm resources, then register
  only the four Applications in this directory. Argo performs manual syncs of
  the pinned release.

The Applications pin the Git source to release revision
`dc3e3596f6a2ff721fb0c8ec51aa07cd78621c82`. Controller, regression, and worker
images use the matching immutable local content tag `dc3e359`; those images
must already be built and imported into the target k3d node before an Argo
sync. No registry push is implied.

The existing `applications/vector.yaml` is intentionally untouched. Do not
register it together with `stable/vector.yaml`; both describe the same Vector
resources. Argo remains internal, no-auth, ClusterIP-only, and has no ingress.
