defmodule WormRegression.StoreTest do
  use ExUnit.Case, async: false

  alias WormRegression.Store
  @server __MODULE__

  setup do
    start_supervised!({Store, name: @server, window_ms: 60_000, max_samples: 1_200})
    :ok
  end

  test "calculates slope and intercept" do
    now = 1_000_000

    for {x, y, sequence} <- [{1, 3, 1}, {2, 5, 2}, {3, 7, 3}] do
      assert {:accepted, _} = Store.ingest(event("a", sequence, x, y, now), now, @server)
    end

    result = Store.result(now, @server)
    assert result.sample_count == 3
    assert_in_delta result.slope, 2.0, 1.0e-12
    assert_in_delta result.intercept, 1.0, 1.0e-12
  end

  test "deduplicates worm id and sequence" do
    now = 1_000_000
    assert {:accepted, _} = Store.ingest(event("a", 1, 1, 3, now), now, @server)
    assert {:duplicate, result} = Store.ingest(event("a", 1, 99, 99, now), now, @server)
    assert result.sample_count == 1
    assert Store.result(now, @server).slope == nil
  end

  test "expires old samples from the rolling window" do
    now = 1_000_000
    assert {:accepted, _} = Store.ingest(event("a", 1, 1, 2, now), now, @server)
    assert Store.result(now + 60_001, @server).sample_count == 0
  end

  test "rejects invalid values and timestamps" do
    now = 1_000_000
    assert {:error, _} = Store.ingest(event("", 1, 1, 2, now), now, @server)
    assert {:error, _} = Store.ingest(event("a", -1, 1, 2, now), now, @server)
    assert {:error, _} = Store.ingest(event("a", 1, "x", 2, now), now, @server)
    assert {:error, _} = Store.ingest(event("a", 1, 1, 2, now - 60_001), now, @server)
    assert {:error, _} = Store.ingest(event("a", 1, 1, 2, now + 1), now, @server)
  end

  defp event(worm_id, sequence, x, y, occurred_at),
    do: %{worm_id: worm_id, sequence: sequence, x: x, y: y, occurred_at: occurred_at}
end
