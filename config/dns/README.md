# LAN DNS for the platform hostnames (home.arpa)

The platform ingresses serve both `*.localhost` (the `/etc/hosts` fallback from
`scripts/hosts-entries.sh`) and a **LAN-DNS host** `<name>.home.arpa`. The
`home.arpa` zone (RFC 8375) is meant to be answered by your local resolver, so
these resolve to the shared Cilium LoadBalancer with **no `/etc/hosts`** on any
client.

Hostnames served (all → the Cilium ingress LB `192.168.0.240`):

    grafana.home.arpa          victoriametrics.home.arpa
    argocd.home.arpa           cartography.home.arpa   neo4j.home.arpa
    assistant.home.arpa        hubble.home.arpa

## Configure the resolver (once, on your router / Pi-hole / Unbound)

**dnsmasq / Pi-hole** (`/etc/dnsmasq.d/home-arpa.conf`, or Pi-hole *Local DNS →
DNS Records*):

```
# config/dns/dnsmasq.conf
address=/home.arpa/192.168.0.240
```

**Unbound** (`/etc/unbound/unbound.conf.d/home-arpa.conf`):

```
server:
    local-zone: "home.arpa." redirect
    local-data: "home.arpa. 60 IN A 192.168.0.240"
```

Then point clients at that resolver (or set it as the DHCP DNS). Verify from a
client:

```bash
getent hosts grafana.home.arpa      # -> 192.168.0.240
```

## Notes

- The IP is the **shared Cilium ingress LoadBalancer**. If `lan-pool` ever hands
  it a different address, update it here and in the `hosts:` values.
- `scripts/hosts-entries.sh` remains only as a fallback for hosts with no access
  to the resolver. **Remove the `/etc/hosts` `*.localhost` lines** once the
  `home.arpa` names resolve.
