'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { buildCurlCommand } = require('../public/curl');

test('builds a GET command without a body', () => {
  assert.equal(
    buildCurlCommand('GET', 'http://localhost:3000/api/inspector/whoami'),
    "curl --request 'GET' 'http://localhost:3000/api/inspector/whoami'",
  );
});

test('quotes a JSON body safely for the shell', () => {
  assert.equal(
    buildCurlCommand('POST', 'http://localhost:3000/api/inspector/anything', '{"text":"it\'s safe"}'),
    "curl --request 'POST' 'http://localhost:3000/api/inspector/anything' --header 'Content-Type: application/json' --data-raw '{\"text\":\"it'\\''s safe\"}'",
  );
});