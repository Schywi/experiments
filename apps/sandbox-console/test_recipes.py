"""Recipe runner tests: the registry, single-flight, the burst, the ceiling."""

import asyncio

import lifecycle
import recipes


def run(coro):
    return asyncio.run(coro)


def collect(generator):
    async def drain():
        return [item async for item in generator]

    return run(drain())


def test_registry_names_the_three_recipes():
    assert set(recipes.RECIPES) == {"claim-burst", "burst-then-teardown", "capacity-check"}
    for recipe in recipes.RECIPES.values():
        assert recipe.blast_radius


def test_unknown_recipe_is_refused():
    try:
        collect(recipes.stream("nope"))
    except recipes.UnknownRecipe as error:
        assert "nope" in str(error)
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected UnknownRecipe")


def test_a_second_recipe_is_refused_while_one_runs(monkeypatch):
    release = asyncio.Event()

    async def slow_create(*args, **kwargs):
        await release.wait()
        return lifecycle.Created("claim", "c", "sandbox-demo", 0.0)

    def fake_claim(namespace, pool, name=None):
        return lifecycle.Created("claim", "c1", namespace, 0.0)

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(lifecycle, "wait_for_sandbox", lambda created, deadline: _ready(created))

    async def _ready(created):
        created.sandbox = created.name
        return created.name

    async def body():
        first = recipes.stream("claim-burst", count=1)
        iterator = first.__aiter__()
        await iterator.__anext__()  # take the first line, the recipe is now running
        assert recipes.busy_with() == "claim-burst"
        try:
            await recipes.stream("claim-burst").__aiter__().__anext__()
        except recipes.RecipeBusy as busy:
            return str(busy)
        finally:
            await iterator.aclose()
        return None

    assert run(body()) == "claim-burst"
    assert recipes.busy_with() is None


def test_claim_burst_reports_each_claim_and_the_total(monkeypatch):
    created = {"n": 0}

    def fake_claim(namespace, pool, name=None):
        created["n"] += 1
        return lifecycle.Created("claim", f"c{created['n']}", namespace, 0.0)

    async def fake_wait(item, deadline):
        item.sandbox = f"sb-{item.name}"
        return item.sandbox

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(lifecycle, "wait_for_sandbox", fake_wait)

    lines = collect(recipes.stream("claim-burst", count=3))

    assert created["n"] == 3
    text = "\n".join(item.get("line", "") for item in lines)
    assert "claiming 3 warm sandboxes" in text
    assert text.count("created c") == 3
    assert "3/3 claims ready" in text
    assert lines[-1] == {"done": True}
    assert any(item.get("metrics", {}).get("created") == 3 for item in lines)


def test_burst_size_is_clamped_to_the_ceiling(monkeypatch):
    import config

    seen = []

    def fake_claim(namespace, pool, name=None):
        seen.append(name)
        return lifecycle.Created("claim", f"c{len(seen)}", namespace, 0.0)

    async def fake_wait(item, deadline):
        item.sandbox = item.name
        return item.name

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(lifecycle, "wait_for_sandbox", fake_wait)

    collect(recipes.stream("claim-burst", count=999))
    assert len(seen) == config.MAX_CONCURRENT_SANDBOXES


def test_teardown_recipe_deletes_what_it_created(monkeypatch):
    deleted = []
    counter = {"n": 0}

    def fake_claim(namespace, pool, name=None):
        # Unique names matter: the burst loops until every created claim is ready,
        # and duplicate names would never satisfy that count.
        counter["n"] += 1
        return lifecycle.Created("claim", f"c{counter['n']}", namespace, 0.0)

    async def fake_wait(item, deadline):
        item.sandbox = item.name
        return item.name

    def fake_delete(namespace, name):
        deleted.append(name)

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(lifecycle, "wait_for_sandbox", fake_wait)
    monkeypatch.setattr(lifecycle, "delete_claim", fake_delete)

    lines = collect(recipes.stream("burst-then-teardown", count=2, hold=0.01))
    text = "\n".join(item.get("line", "") for item in lines)

    assert sorted(deleted) == ["c1", "c2"]
    assert "teardown complete" in text


def test_capacity_check_is_read_only_and_reports_the_ceiling(monkeypatch):
    import kube

    class Snapshot:
        sandboxes = [{"status": {"conditions": [{"type": "Ready", "status": "True"}]}}]
        pods = [{}, {}]
        errors: list = []

    monkeypatch.setattr(kube, "snapshot", lambda namespace: Snapshot())
    monkeypatch.setattr(kube, "nodes", lambda: [{"name": "fedora", "allocatable": {"cpu": "6", "memory": "16Gi", "pods": "110"}}])

    lines = collect(recipes.stream("capacity-check", count=5))
    text = "\n".join(item.get("line", "") for item in lines)
    assert "sandboxes: 1 (1 ready)" in text
    assert "pods: 2" in text
    assert "ceiling:" in text
    assert "node fedora" in text
    assert lines[-1] == {"done": True}
