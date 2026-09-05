import Config

config :worm_regression,
  port: String.to_integer(System.get_env("PORT") || "4000"),
  window_ms: 60_000,
  max_samples: 1_200
