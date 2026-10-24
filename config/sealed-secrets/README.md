# Sealed Secrets controller

This directory installs the [Bitnami Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets)
controller, which lets the public repository carry encrypted secrets safely. A
`SealedSecret` is safe to commit: only the controller in this cluster can
decrypt it, using a private key that never leaves the cluster.

## Install

```bash
config/sealed-secrets/install.sh
```

The script pins `SEALED_SECRETS_CHART_VERSION` (`2.20.0`) from
`https://bitnami.github.io/sealed-secrets` and installs it into `kube-system`
with the fixed release name `sealed-secrets-controller` so the `kubeseal` CLI
works with its default settings.

## Sealing a secret

The `kubeseal` binary is required locally. With the controller running:

```bash
# 1. Write the plaintext Secret out-of-band (never commit this file).
kubectl create secret generic cartography-neo4j-auth \
  --namespace cartography \
  --from-literal=password="$(openssl rand -base64 24)" \
  --dry-run=client -o yaml > /tmp/neo4j-auth.yaml

# 2. Encrypt it into a SealedSecret bound to the target namespace.
kubeseal --format yaml < /tmp/neo4j-auth.yaml > config/cartography/sealedsecrets/neo4j-auth.yaml

# 3. Remove the plaintext copy.
rm -f /tmp/neo4j-auth.yaml
```

Commit only the `SealedSecret` (step 2 output). The plaintext file and the
controller's private key are never committed.

## Back up the controller key

The controller generates a private key on first start. If that key is lost, every
committed `SealedSecret` becomes undecryptable. Back it up out of band:

```bash
kubectl get secret -n kube-system -l sealedsecrets.bitnami.com/sealed-secrets-key \
  -o yaml > sealed-secrets-key.yaml   # store securely; never commit
```

## Notes

- SealedSecrets are namespace-scoped by default: a secret sealed for namespace
  `cartography` can only be decrypted into `cartography`.
- Rotating the controller key does not break existing `SealedSecret` resources;
  the controller keeps previous keys for decryption unless explicitly pruned.
