defmodule WormRegression.RouterTest do
  use ExUnit.Case, async: false
  use Plug.Test

  test "accepts an ingest request and exposes its result" do
    now = System.system_time(:millisecond)
    sequence = System.unique_integer([:positive])

    body =
      Jason.encode!(%{
        worm_id: "http-test-#{sequence}",
        sequence: sequence,
        x: 1.0,
        y: 3.0,
        occurred_at: now
      })

    ingest =
      conn(:post, "/ingest", body)
      |> put_req_header("content-type", "application/json")
      |> WormRegression.Router.call([])

    assert ingest.status == 202
    assert %{"status" => "accepted"} = Jason.decode!(ingest.resp_body)

    result = conn(:get, "/result") |> WormRegression.Router.call([])
    assert result.status == 200
    assert %{"sample_count" => count} = Jason.decode!(result.resp_body)
    assert count >= 1
  end
end
