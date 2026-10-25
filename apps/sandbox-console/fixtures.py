"""Fixtures for the sandbox objects the console reads.

Recorded shapes, not live calls: `Sandbox.status.labelSelector` carries the pod
hash the controller stamps (`controllers/sandbox_controller.go`), adoption drops
the warm-pool label (`extensions/controllers/sandboxclaim_controller.go`), and
`metrics.k8s.io` reports usage as quantities.
"""

import copy

READY_SANDBOX = {
    "metadata": {
        "name": "sb-4f2a",
        "creationTimestamp": "2026-10-24T17:12:03Z",
        "labels": {
            "agents.x-k8s.io/launch-type": "warm",
            "agents.x-k8s.io/created-by": "controller",
        },
        "annotations": {"agents.x-k8s.io/sandbox-template-ref": "browser-tpl"},
    },
    "spec": {},
    "status": {
        "conditions": [{"type": "Ready", "status": "True", "reason": "DependenciesReady"}],
        "podIPs": ["10.42.0.36"],
        "nodeName": "fedora",
        "selector": "agents.x-k8s.io/sandbox-name-hash=abc123",
    },
}

PENDING_SANDBOX = {
    "metadata": {
        "name": "sb-8c1b",
        "creationTimestamp": "2026-10-24T17:13:03Z",
        "labels": {"agents.x-k8s.io/launch-type": "cold"},
    },
    "spec": {},
    "status": {
        "conditions": [
            {"type": "Ready", "status": "False", "reason": "DependenciesNotReady"},
        ],
        "selector": "agents.x-k8s.io/sandbox-name-hash=def456",
    },
}

POOL_MEMBER = {
    "metadata": {
        "name": "browser-pool-7mqd9",
        "creationTimestamp": "2026-10-24T17:10:00Z",
        "labels": {
            "agents.x-k8s.io/warm-pool-sandbox": "9f86d081",
            "agents.x-k8s.io/launch-type": "warm",
        },
    },
    "status": {
        "conditions": [{"type": "Ready", "status": "True", "reason": "DependenciesReady"}],
        "podIPs": ["10.42.0.40"],
        "selector": "agents.x-k8s.io/sandbox-name-hash=ghi789",
    },
}

ADOPTED_POOL_SANDBOX = {
    # Same sandbox after a claim adopted it: the warm-pool label is gone.
    "metadata": {
        "name": "browser-pool-7mqd9",
        "creationTimestamp": "2026-10-24T17:10:00Z",
        "labels": {"agents.x-k8s.io/launch-type": "warm"},
    },
    "status": {
        "conditions": [{"type": "Ready", "status": "True", "reason": "DependenciesReady"}],
        "podIPs": ["10.42.0.40"],
        "selector": "agents.x-k8s.io/sandbox-name-hash=ghi789",
    },
}

CLAIM = {
    "metadata": {"name": "demo-3"},
    "status": {"sandbox": {"name": "sb-4f2a", "podIPs": ["10.42.0.36"]}},
}

PODS = [
    {
        "metadata": {
            "name": "sb-4f2a-pod",
            "labels": {"agents.x-k8s.io/sandbox-name-hash": "abc123"},
        },
        "spec": {"nodeName": "fedora", "dnsPolicy": "None", "automountServiceAccountToken": False},
        "status": {"podIP": "10.42.0.36"},
    },
    {
        "metadata": {
            "name": "browser-pool-7mqd9-pod",
            "labels": {"agents.x-k8s.io/sandbox-name-hash": "ghi789"},
        },
        "spec": {"nodeName": "fedora"},
        "status": {"podIP": "10.42.0.40"},
    },
]

USAGE = {
    "sb-4f2a-pod": {"cpu_millicores": 42, "mem_working_set_bytes": 262144000},
    "browser-pool-7mqd9-pod": {"cpu_millicores": 3, "mem_working_set_bytes": 10485760},
}


def copy_of(*objects):
    return [copy.deepcopy(item) for item in objects]
