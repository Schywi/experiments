defmodule WormRegression.MixProject do
  use Mix.Project

  def project do
    [
      app: :worm_regression,
      version: "0.1.0",
      elixir: "~> 1.16",
      start_permanent: Mix.env() == :prod,
      deps: deps()
    ]
  end

  def application do
    [
      extra_applications: [:logger],
      mod: {WormRegression.Application, []}
    ]
  end

  defp deps do
    [
      {:jason, "~> 1.4"},
      {:plug_cowboy, "~> 2.7"}
    ]
  end
end
