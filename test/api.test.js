const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
let child, baseUrl, tempDir;
before(async () => {
  tempDir = await fs.mkdtemp(path.join(os.tmpdir(), 'entregaviva-test-'));
  const port = 20000 + Math.floor(Math.random() * 40000); baseUrl = `http://127.0.0.1:${port}`;
  child = spawn(process.execPath, ['server.js'], { cwd: path.resolve(__dirname, '..'), env: { ...process.env, PORT: String(port), DATABASE_PATH: path.join(tempDir, 'test.sqlite') }, stdio: 'ignore' });
  for (let i = 0; i < 100; i++) { if (child.exitCode !== null) throw new Error(`Server exited with code ${child.exitCode}`); try { if ((await fetch(`${baseUrl}/api/orders`)).ok) return; } catch {} await new Promise(resolve => setTimeout(resolve, 50)); }
  throw new Error('Test server did not become ready');
});
after(async () => { if (child && child.exitCode === null) { child.kill(); await Promise.race([once(child, 'exit'), new Promise(resolve => setTimeout(resolve, 2000))]); } if (tempDir) await fs.rm(tempDir, { recursive: true, force: true }); });
async function json(url, options) { const response = await fetch(baseUrl + url, { ...options, headers: { 'Content-Type': 'application/json', ...(options?.headers || {}) } }); return { response, body: await response.json() }; }
test('creates a privacy-safe customer tracking link and records valid state changes', async () => {
  const invalid = await json('/api/orders', { method: 'POST', body: JSON.stringify({ restaurant: 'Corner Cafe', customer: 'Guest Person', address: '1 Main St', total: 25, estimatedMinutes: 5 }) }); assert.equal(invalid.response.status, 400);
  const created = await json('/api/orders', { method: 'POST', body: JSON.stringify({ restaurant: 'Corner Cafe', customer: 'Guest Person', address: '1 Main St', total: 25, estimatedMinutes: 60 }) }); assert.equal(created.response.status, 201); assert.equal(created.body.estimatedMinutes, 60); assert.ok(created.body.code);
  const page = await fetch(baseUrl + '/track.html'); assert.equal(page.status, 200);
  const tracking = await json(`/api/orders/track/${created.body.code.toLowerCase()}`); assert.equal(tracking.response.status, 200); assert.equal(tracking.body.status, 'recebido'); assert.ok(tracking.body.remainingMinutes <= 60); assert.equal('address' in tracking.body, false); assert.equal('customer' in tracking.body, false);
  const badTransition = await json(`/api/orders/${created.body.id}/status`, { method: 'PATCH', body: JSON.stringify({ status: 'entregue' }) }); assert.equal(badTransition.response.status, 409);
  const next = await json(`/api/orders/${created.body.id}/status`, { method: 'PATCH', body: JSON.stringify({ status: 'preparando' }) }); assert.equal(next.response.status, 200);
  const updatedTrack = await json(`/api/orders/track/${created.body.code}`); assert.equal(updatedTrack.body.status, 'preparando'); assert.equal(updatedTrack.body.events.length, 2);
});
