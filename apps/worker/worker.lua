-- A single bounded "worm" worker.  It deliberately has no Kubernetes API.
--
-- The component host supplies `worm_host`:
--   getenv(name) -> string|nil
--   now_unix_ms() -> integer
--   sleep_ms(milliseconds)
--   http_post(url, body) -> true | false, optional error text
--
-- Keeping I/O in this tiny host interface lets this file remain ordinary Lua
-- while the WASI Preview 2 component supplies outbound HTTP capability.

local host = assert(worm_host, "worm_host capability is required")

local MAX_BATCH = 10
local MAX_RETRIES = 5
local POLL_INTERVAL_MS = 1000

local function required_env(name)
  local value = host.getenv(name)
  assert(value and value ~= "", name .. " is required")
  return value
end

local worm_id = required_env("WORM_ID")
local events_url = required_env("VECTOR_EVENTS_URL")
local replicator_url = required_env("REPLICATOR_URL")

local function endpoint(base, path)
  return (base:gsub("/$", "")) .. path
end

local function json_string(value)
  return '"' .. value:gsub('\\', '\\\\'):gsub('"', '\\"'):gsub('\n', '\\n'):gsub('\r', '\\r'):gsub('\t', '\\t') .. '"'
end

local function post_with_retry(url, payload)
  for attempt = 1, MAX_RETRIES do
    local ok, accepted = pcall(host.http_post, url, payload)
    if ok and accepted then
      return true
    end
    if attempt < MAX_RETRIES then
      -- A deterministic cap is more important than sophisticated retry logic
      -- here: a failed sink must never make a worm grow memory without bound.
      host.sleep_ms(100 * attempt)
    end
  end
  return false
end

local function encode_event(event)
  return "{" ..
    "\"worm_id\":" .. json_string(event.worm_id) .. "," ..
    "\"sequence\":" .. event.sequence .. "," ..
    "\"x\":" .. event.x .. "," ..
    "\"y\":" .. event.y .. "," ..
    "\"occurred_at\":" .. event.occurred_at ..
    "}"
end

local function encode_batch(events)
  local encoded = {}
  for index, event in ipairs(events) do
    encoded[index] = encode_event(event)
  end
  return "{\"events\":[" .. table.concat(encoded, ",") .. "]}"
end

local function send_replication_intent()
  -- The intent is stable for the process lifetime.  The controller can use it
  -- as an idempotency key if an HTTP response is lost after acceptance.
  local intent_id = worm_id .. ":1"
  local payload = "{\"worm_id\":" .. json_string(worm_id) ..
    ",\"intent_id\":" .. json_string(intent_id) .. "}"
  return post_with_retry(endpoint(replicator_url, "/v1/replication-intents"), payload)
end

local function sample(sequence)
  local now = host.now_unix_ms()
  return {
    worm_id = worm_id,
    sequence = sequence,
    x = now,
    -- A simple monotonic signal makes the initial regression deterministic.
    y = sequence,
    occurred_at = now,
  }
end

local queue = {}
local sequence = 0

-- Ask exactly once.  Failure is bounded and does not stop telemetry.
send_replication_intent()

while true do
  sequence = sequence + 1
  queue[#queue + 1] = sample(sequence)

  -- Send fixed-size batches.  The queue cannot grow past MAX_BATCH: a failed
  -- delivery is discarded before another sample can be enqueued.
  if #queue >= MAX_BATCH then
    post_with_retry(events_url, encode_batch(queue))
    queue = {}
  end

  host.sleep_ms(POLL_INTERVAL_MS)
end
