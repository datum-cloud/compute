<?php
// Router script for PHP's built-in web server (php -S 0.0.0.0:8080 server.php).
// PHP invokes this script for every request; route on the request path.
//   /healthz      -> "ok"
//   anything else -> "Hello from Datum (PHP)"
// PHP's built-in server prints its own boot marker to the console on start:
//   "PHP <ver> Development Server (http://0.0.0.0:8080) started"
// so no extra startup print is required here.

header('Content-Type: text/plain; charset=utf-8');

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);

if ($path === '/healthz') {
    echo "ok\n";
} else {
    echo "Hello from Datum (PHP)\n";
}
