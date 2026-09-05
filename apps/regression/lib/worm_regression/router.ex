defmodule WormRegression.Router do
  @moduledoc false
  use Plug.Router

  plug(Plug.Logger)
  plug(:match)
  plug(Plug.Parsers, parsers: [:json], pass: ["application/json"], json_decoder: Jason)
  plug(:dispatch)

  post "/ingest" do
    case WormRegression.Store.ingest(conn.body_params) do
      {:accepted, result} -> json(conn, 202, %{status: "accepted", result: result})
      {:duplicate, result} -> json(conn, 200, %{status: "duplicate", result: result})
      {:error, reason} -> json(conn, 422, %{error: reason})
    end
  end

  get "/result" do
    json(conn, 200, WormRegression.Store.result())
  end

  match _ do
    json(conn, 404, %{error: "not found"})
  end

  defp json(conn, status, body) do
    conn
    |> Plug.Conn.put_resp_content_type("application/json")
    |> Plug.Conn.send_resp(status, Jason.encode!(body))
  end
end
