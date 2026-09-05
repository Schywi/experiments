defmodule WormRegression.Application do
  @moduledoc false
  use Application

  def start(_type, _args) do
    children = [
      {WormRegression.Store, []},
      {Plug.Cowboy, scheme: :http, plug: WormRegression.Router, options: [port: port()]}
    ]

    Supervisor.start_link(children, strategy: :one_for_one, name: WormRegression.Supervisor)
  end

  defp port, do: Application.fetch_env!(:worm_regression, :port)
end
