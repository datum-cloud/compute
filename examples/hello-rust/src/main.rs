// Minimal std-only HTTP service used to prove the compiled static-PIE Rust path
// on Datum compute's Unikraft app-elfloader runtime (base:latest).
//
// Dependency-light on purpose: only std::net so the runtime question is isolated
// from any async-runtime / FFI variables. Rust's x86_64-unknown-linux-musl target
// builds a fully static, position-independent (PIE) ELF -- exactly the shape the
// elfloader requires and the property the prior non-PIE bun image lacked.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};

fn main() {
    let port = std::env::var("PORT").unwrap_or_else(|_| "8080".to_string());
    let addr = format!("0.0.0.0:{port}");

    let listener = TcpListener::bind(&addr).unwrap_or_else(|e| {
        eprintln!("failed to bind {addr}: {e}");
        std::process::exit(1);
    });

    println!("listening on :{port}");

    for stream in listener.incoming() {
        match stream {
            Ok(stream) => {
                // Handle connections serially; the workload is a liveness probe,
                // not a load target, so a single-threaded accept loop is enough.
                if let Err(e) = handle(stream) {
                    eprintln!("connection error: {e}");
                }
            }
            Err(e) => eprintln!("accept error: {e}"),
        }
    }
}

fn handle(mut stream: TcpStream) -> std::io::Result<()> {
    // Read until end of request headers (CRLFCRLF) or the buffer fills. We only
    // need the request line to route, so we never consume a body.
    let mut buf = [0u8; 4096];
    let mut filled = 0usize;
    loop {
        if filled == buf.len() {
            break;
        }
        let n = stream.read(&mut buf[filled..])?;
        if n == 0 {
            break;
        }
        filled += n;
        if buf[..filled].windows(4).any(|w| w == b"\r\n\r\n") {
            break;
        }
    }

    let req = String::from_utf8_lossy(&buf[..filled]);
    let path = req
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .unwrap_or("/");

    let body = match path {
        "/healthz" => "ok",
        _ => "Hello from Datum (Rust)",
    };

    let response = format!(
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/plain; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n\
         {}",
        body.len(),
        body
    );

    stream.write_all(response.as_bytes())?;
    stream.flush()?;
    Ok(())
}
