defmodule WormRegression.Store do
  @moduledoc """
  A bounded, in-memory rolling window for worm observations.

  `occurred_at` is a Unix timestamp in milliseconds.  A sample key is retained
  only while its sample remains in the 60-second window, so memory use is bounded
  and duplicate deliveries do not affect the regression.
  """
  use GenServer

  @default_window_ms 60_000
  @default_max_samples 1_200

  defstruct samples: %{}, window_ms: @default_window_ms, max_samples: @default_max_samples

  @type event :: %{
          required(:worm_id) => String.t(),
          required(:sequence) => integer(),
          required(:x) => number(),
          required(:y) => number(),
          required(:occurred_at) => integer()
        }

  def start_link(opts) do
    config = Application.get_env(:worm_regression, __MODULE__, [])
    opts = Keyword.merge(config, opts)
    GenServer.start_link(__MODULE__, opts, name: Keyword.get(opts, :name, __MODULE__))
  end

  @spec ingest(map(), integer()) :: {:accepted | :duplicate, map()} | {:error, String.t()}
  def ingest(event, now_ms \\ System.system_time(:millisecond), server \\ __MODULE__),
    do: GenServer.call(server, {:ingest, event, now_ms})

  @spec result(integer()) :: map()
  def result(now_ms \\ System.system_time(:millisecond), server \\ __MODULE__),
    do: GenServer.call(server, {:result, now_ms})

  @impl true
  def init(opts) do
    {:ok,
     %__MODULE__{
       window_ms: Keyword.get(opts, :window_ms, @default_window_ms),
       max_samples: Keyword.get(opts, :max_samples, @default_max_samples)
     }}
  end

  @impl true
  def handle_call({:ingest, event, now_ms}, _from, state) do
    state = prune(state, now_ms)

    with {:ok, sample} <- validate(event, now_ms, state.window_ms) do
      key = {sample.worm_id, sample.sequence}

      if Map.has_key?(state.samples, key) do
        {:reply, {:duplicate, regression(state)}, state}
      else
        samples = Map.put(state.samples, key, sample)
        state = %{state | samples: trim(samples, state.max_samples)}
        {:reply, {:accepted, regression(state)}, state}
      end
    else
      {:error, reason} -> {:reply, {:error, reason}, state}
    end
  end

  def handle_call({:result, now_ms}, _from, state) do
    state = prune(state, now_ms)
    {:reply, regression(state), state}
  end

  defp validate(event, now_ms, window_ms) when is_map(event) do
    with worm_id when is_binary(worm_id) and byte_size(worm_id) > 0 <- value(event, :worm_id),
         sequence when is_integer(sequence) and sequence >= 0 <- value(event, :sequence),
         x when finite_number?(x) <- value(event, :x),
         y when finite_number?(y) <- value(event, :y),
         occurred_at when is_integer(occurred_at) <- value(event, :occurred_at),
         true <- occurred_at >= now_ms - window_ms,
         true <- occurred_at <= now_ms do
      {:ok,
       %{worm_id: worm_id, sequence: sequence, x: x * 1.0, y: y * 1.0, occurred_at: occurred_at}}
    else
      false ->
        {:error, "occurred_at must be within the rolling window and not in the future"}

      _ ->
        {:error,
         "invalid event; expected worm_id, non-negative sequence, finite x/y, and Unix-millisecond occurred_at"}
    end
  end

  defp validate(_, _, _), do: {:error, "event must be a JSON object"}
  defp value(event, key), do: Map.get(event, key) || Map.get(event, Atom.to_string(key))
  defp finite_number?(value) when is_integer(value), do: true
  defp finite_number?(value) when is_float(value), do: value == value and abs(value) <= 1.0e308
  defp finite_number?(_), do: false

  defp prune(state, now_ms) do
    cutoff = now_ms - state.window_ms

    %{
      state
      | samples: Map.filter(state.samples, fn {_key, sample} -> sample.occurred_at >= cutoff end)
    }
  end

  defp trim(samples, max_samples) when map_size(samples) <= max_samples, do: samples

  defp trim(samples, max_samples) do
    samples
    |> Map.values()
    |> Enum.sort_by(& &1.occurred_at, :desc)
    |> Enum.take(max_samples)
    |> Map.new(fn sample -> {{sample.worm_id, sample.sequence}, sample} end)
  end

  defp regression(state) do
    values = Map.values(state.samples)
    n = length(values)

    {sum_x, sum_y, sum_xx, sum_xy} =
      Enum.reduce(values, {0.0, 0.0, 0.0, 0.0}, fn sample, {sx, sy, sxx, sxy} ->
        {sx + sample.x, sy + sample.y, sxx + sample.x * sample.x, sxy + sample.x * sample.y}
      end)

    denominator = n * sum_xx - sum_x * sum_x

    base = %{sample_count: n, window_ms: state.window_ms}

    if n < 2 or abs(denominator) < 1.0e-12 do
      Map.merge(base, %{slope: nil, intercept: nil})
    else
      slope = (n * sum_xy - sum_x * sum_y) / denominator
      Map.merge(base, %{slope: slope, intercept: (sum_y - slope * sum_x) / n})
    end
  end
end
