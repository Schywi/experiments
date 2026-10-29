"""Probe tests: how a fetch failure becomes `blocked` or `reachable`."""

import probes

TIMEOUT_MS = 4000


def test_build_expression_injects_the_deadline():
    expression = probes.build_expression(TIMEOUT_MS)
    assert "const timeoutMs = 4000;" in expression
    assert "__TIMEOUT_MS__" not in expression
    assert "10.43.0.1:443" in expression
    assert "169.254.169.254" in expression
    assert "1.1.1.1" in expression


def test_an_abort_at_the_deadline_is_blocked():
    assert probes.classify({"outcome": "timeout", "ms": 4001, "error": "AbortError"}, TIMEOUT_MS) == "blocked"


def test_a_fast_non_abort_failure_is_reachable():
    # A completed handshake followed by a certificate error: nothing was dropped.
    assert probes.classify({"outcome": "refused", "ms": 18, "error": "TypeError"}, TIMEOUT_MS) == "reachable"


def test_a_successful_fetch_is_reachable():
    assert probes.classify({"outcome": "ok", "ms": 55, "error": None}, TIMEOUT_MS) == "reachable"


def test_public_egress_is_ok_when_the_fetch_did_not_time_out():
    assert probes.public_ok({"outcome": "ok", "ms": 20}, TIMEOUT_MS) is True
    assert probes.public_ok({"outcome": "refused", "ms": 20}, TIMEOUT_MS) is True
    assert probes.public_ok({"outcome": "timeout", "ms": 4001}, TIMEOUT_MS) is False


def test_parse_accepts_a_json_string_and_rejects_noise():
    assert probes.parse('{"cluster": {"outcome": "timeout"}}')["cluster"]["outcome"] == "timeout"
    assert probes.parse({"cluster": {}}) == {"cluster": {}}
    assert probes.parse("not json") == {}
    assert probes.parse("[1, 2]") == {}


def test_summarise_merges_live_and_spec_claims_and_keeps_evidence():
    observations = {
        "cluster": {"outcome": "timeout", "ms": 4002, "error": "AbortError"},
        "metadata": {"outcome": "timeout", "ms": 4001, "error": "AbortError"},
        "public": {"outcome": "ok", "ms": 38, "error": None},
    }
    spec = {"sa_token": "absent", "cluster_dns": "none:8.8.8.8,1.1.1.1"}
    reachability = probes.summarise(observations, TIMEOUT_MS, spec)

    assert reachability["cluster"] == "blocked"
    assert reachability["metadata"] == "blocked"
    assert reachability["egress_public_ok"] is True
    assert reachability["sa_token"] == "absent"
    assert reachability["cluster_dns"] == "none:8.8.8.8,1.1.1.1"
    assert reachability["evidence"]["cluster"] == {
        "ms": 4002,
        "outcome": "timeout",
        "error": "AbortError",
        "source": "live",
    }
    # The two spec-derived claims say so, so the badge never implies otherwise.
    assert reachability["evidence"]["sa_token"] == "spec"
    assert reachability["evidence"]["cluster_dns"] == "spec"


def test_summarise_without_observations_reports_reachable_not_blocked():
    reachability = probes.summarise({}, TIMEOUT_MS, {})
    # An empty observation means "refused" by default: the pane must not claim
    # isolation it did not observe.
    assert reachability["cluster"] == "reachable"
    assert reachability["egress_public_ok"] is True
    assert reachability["sa_token"] == "unknown"


def test_spec_claims_read_the_hardened_template_pod():
    pod = {
        "spec": {
            "dnsPolicy": "None",
            "dnsConfig": {"nameservers": ["8.8.8.8", "1.1.1.1"]},
            "automountServiceAccountToken": False,
        }
    }
    claims = probes.spec_claims(pod)
    assert claims["sa_token"] == "absent"
    assert claims["cluster_dns"] == "none:8.8.8.8,1.1.1.1"


def test_spec_claims_read_an_unhardened_bare_sandbox_pod():
    # No dnsPolicy and no automountServiceAccountToken: the Kubernetes defaults.
    claims = probes.spec_claims({"spec": {"containers": []}})
    assert claims["sa_token"] == "mounted"
    assert claims["cluster_dns"] == "clusterfirst"


def test_spec_claims_degrade_to_unknown_without_a_pod():
    assert probes.spec_claims(None) == {"sa_token": "unknown", "cluster_dns": "unknown"}
