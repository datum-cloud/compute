// Minimal pure-Node HTTP service for the Node.js runtime proof on Datum compute.
//
// No dependencies: the "hello" case ships an empty node_modules so the unikernel
// rootfs stays minimal. Listens on $PORT (default 8080) and logs a clear boot
// marker so the unikernel console shows when Node has come up.
const http = require('http');

const port = parseInt(process.env.PORT, 10) || 8080;

const server = http.createServer((req, res) => {
  if (req.url === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok\n');
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('Hello from Datum (Node)\n');
});

server.listen(port, () => {
  console.log('listening on :' + port);
});
