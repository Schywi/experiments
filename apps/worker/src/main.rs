use std::{
    env,
    io::{Read, Write},
    net::{SocketAddr, TcpStream, ToSocketAddrs},
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(2);
const IO_TIMEOUT: Duration = Duration::from_secs(2);
const DEFAULT_SAMPLE_INTERVAL: Duration = Duration::from_secs(1);

struct Endpoint {
    host: String,
    port: u16,
    base_path: String,
}

fn main() {
    let worm_id = required_env("WORM_ID");
    let controller = parse_http_endpoint(&required_env("CONTROLLER_URL"))
        .unwrap_or_else(|error| panic!("invalid CONTROLLER_URL: {error}"));
    let interval = sample_interval()
        .unwrap_or_else(|error| panic!("invalid WORM_SAMPLE_INTERVAL_MS: {error}"));

    send_intent_with_retry(&controller, &worm_id);

    let mut sequence: u64 = 0;
    loop {
        sequence += 1;
        emit_sample(&worm_id, sequence);
        thread::sleep(interval);
    }
}

fn required_env(name: &str) -> String {
    env::var(name).unwrap_or_else(|_| panic!("{name} is required"))
}

fn sample_interval() -> Result<Duration, String> {
    match env::var("WORM_SAMPLE_INTERVAL_MS") {
        Ok(value) => {
            let milliseconds: u64 = value
                .parse()
                .map_err(|_| "must be an integer".to_string())?;
            if milliseconds < 1_000 {
                return Err("must be at least 1000".to_string());
            }
            Ok(Duration::from_millis(milliseconds))
        }
        Err(_) => Ok(DEFAULT_SAMPLE_INTERVAL),
    }
}

fn send_intent_with_retry(controller: &Endpoint, worm_id: &str) {
    let path = format!("{}/v1/replication-intents", controller.base_path);
    let body = format!(
        "{{\"worm_id\":{},\"intent_id\":{}}}",
        json_string(worm_id),
        json_string(&format!("{worm_id}:1")),
    );

    for attempt in 1_u64.. {
        match post_json(controller, &path, &body) {
            Ok(_) => return,
            Err(error) => {
                eprintln!("replication intent attempt {attempt} failed: {error}");
                thread::sleep(Duration::from_millis((100 * attempt).min(5_000)));
            }
        }
    }
}

fn emit_sample(worm_id: &str, sequence: u64) {
    let now = unix_milliseconds();
    let record = format!(
        "{{\"kind\":\"sample\",\"worm_id\":{},\"sequence\":{sequence},\"x\":{now},\"y\":{sequence},\"occurred_at\":{now}}}\n",
        json_string(worm_id),
    );
    let mut stdout = std::io::stdout().lock();
    stdout
        .write_all(record.as_bytes())
        .expect("write worker sample");
    stdout.flush().expect("flush worker sample");
}

fn unix_milliseconds() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock before Unix epoch")
        .as_millis()
}

fn parse_http_endpoint(value: &str) -> Result<Endpoint, String> {
    let without_scheme = value
        .strip_prefix("http://")
        .ok_or_else(|| "only http:// endpoints are supported".to_string())?;
    let (authority, path) = without_scheme
        .split_once('/')
        .unwrap_or((without_scheme, ""));
    if authority.is_empty()
        || authority.contains('@')
        || authority.contains('?')
        || authority.contains('#')
    {
        return Err("endpoint authority is invalid".to_string());
    }
    let (host, port) = match authority.rsplit_once(':') {
        Some((host, port)) if !host.is_empty() => (
            host.to_string(),
            port.parse::<u16>()
                .map_err(|_| "port must be a number".to_string())?,
        ),
        Some(_) => return Err("endpoint host is empty".to_string()),
        None => (authority.to_string(), 80),
    };
    let base_path = if path.is_empty() {
        String::new()
    } else {
        format!("/{}", path.trim_end_matches('/'))
    };
    Ok(Endpoint {
        host,
        port,
        base_path,
    })
}

fn post_json(endpoint: &Endpoint, path: &str, body: &str) -> Result<u16, String> {
    let address = resolve_address(&endpoint.host, endpoint.port)?;
    let mut stream =
        TcpStream::connect_timeout(&address, CONNECT_TIMEOUT).map_err(|error| error.to_string())?;
    stream
        .set_read_timeout(Some(IO_TIMEOUT))
        .map_err(|error| error.to_string())?;
    stream
        .set_write_timeout(Some(IO_TIMEOUT))
        .map_err(|error| error.to_string())?;
    let request = format!(
        "POST {path} HTTP/1.1\r\nHost: {}:{}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        endpoint.host,
        endpoint.port,
        body.len(),
    );
    stream
        .write_all(request.as_bytes())
        .map_err(|error| error.to_string())?;
    stream.flush().map_err(|error| error.to_string())?;
    let mut response = [0_u8; 128];
    let count = stream
        .read(&mut response)
        .map_err(|error| error.to_string())?;
    let status_line = std::str::from_utf8(&response[..count])
        .map_err(|_| "controller response was not HTTP".to_string())?
        .lines()
        .next()
        .ok_or_else(|| "controller response was empty".to_string())?;
    let status: u16 = status_line
        .split_whitespace()
        .nth(1)
        .ok_or_else(|| "controller response lacks a status".to_string())?
        .parse()
        .map_err(|_| "controller response status was invalid".to_string())?;
    if (200..300).contains(&status) || status == 409 {
        Ok(status)
    } else {
        Err(format!("controller returned HTTP {status}"))
    }
}

fn resolve_address(host: &str, port: u16) -> Result<SocketAddr, String> {
    (host, port)
        .to_socket_addrs()
        .map_err(|error| error.to_string())?
        .next()
        .ok_or_else(|| "controller hostname resolved to no addresses".to_string())
}

fn json_string(value: &str) -> String {
    format!(
        "\"{}\"",
        value
            .replace('\\', "\\\\")
            .replace('"', "\\\"")
            .replace('\n', "\\n")
            .replace('\r', "\\r")
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_cluster_service_endpoint() {
        let endpoint =
            parse_http_endpoint("http://worm-controller.worm-lab.svc.cluster.local:8081").unwrap();
        assert_eq!(endpoint.host, "worm-controller.worm-lab.svc.cluster.local");
        assert_eq!(endpoint.port, 8081);
    }

    #[test]
    fn refuses_non_http_endpoint() {
        assert!(parse_http_endpoint("https://worm-controller").is_err());
    }

    #[test]
    fn escapes_json_strings() {
        assert_eq!(json_string("a\"b\\c"), "\"a\\\"b\\\\c\"");
    }
}
