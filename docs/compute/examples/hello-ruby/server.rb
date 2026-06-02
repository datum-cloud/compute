# Minimal stdlib HTTP service for the Datum compute Ruby runtime proof.
#
# No gems -- only the Ruby standard library (socket / TCPServer) so the rootfs
# needs nothing but the interpreter and its stdlib. We avoid webrick because it
# is no longer a default gem in Ruby 3.x. Serves on $PORT (default 8080):
#   /healthz      -> "ok"
#   anything else -> "Hello from Datum (Ruby)"
# Prints "listening on :<port>" on start as a boot marker on the console.

require "socket"

port = (ENV["PORT"] || "8080").to_i
server = TCPServer.new("0.0.0.0", port)

$stdout.puts "listening on :#{port}"
$stdout.flush

def respond(client, body)
  client.write "HTTP/1.1 200 OK\r\n"
  client.write "Content-Type: text/plain; charset=utf-8\r\n"
  client.write "Content-Length: #{body.bytesize}\r\n"
  client.write "Connection: close\r\n"
  client.write "\r\n"
  client.write body
end

loop do
  client = server.accept
  begin
    request_line = client.gets
    # Drain the rest of the request headers so the client does not see a reset.
    while (line = client.gets) && line != "\r\n"
    end

    path = request_line ? request_line.split(" ")[1].to_s : "/"
    if path == "/healthz"
      respond(client, "ok\n")
    else
      respond(client, "Hello from Datum (Ruby)\n")
    end
  rescue StandardError
    # Ignore malformed connections; keep serving.
  ensure
    client.close
  end
end
