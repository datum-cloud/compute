// The serverless "function" -- supplied at deploy time via a ConfigMap, NOT
// baked into the runtime image. Datum mounts the ConfigMap at /app, so this
// file lands at /app/index.js and the Kraftfile's cmd points node at it.
//
// Dependency-free (Node stdlib only): the generic runtime image ships no
// node_modules, so the function must not `require` anything outside core.
//
// The VERSION string is the swap marker: change it in configmap.yaml, re-apply,
// restart the workload, and the HTTP response proves the new code took effect
// with no image rebuild -- the core serverless value prop.
const http = require('http');

const VERSION = 'v1';
const port = parseInt(process.env.PORT, 10) || 8080;

const server = http.createServer((req, res) => {
  if (req.url === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok\n');
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('Hello from a ConfigMap-mounted function — ' + VERSION + '\n');
});

server.listen(port, () => {
  console.log('serverless-js: ' + VERSION + ' listening on :' + port);
});
