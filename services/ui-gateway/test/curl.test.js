'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { buildCurlCommand } = require('../public/curl');

test('builds a GET command without a body', () => {
  assert.equal(
    buildCurlCommand('GET', 'http://localhost:3000/api/inspector/whoami'),
    "curl --globoff --request 'GET' 'http://localhost:3000/api/inspector/whoami'",
  );
});

test('disables cURL URL globbing for bracketed query values', () => {
  assert.equal(
    buildCurlCommand('GET', 'http://localhost:3000/api/inspector/anything?id=[1-3]'),
    "curl --globoff --request 'GET' 'http://localhost:3000/api/inspector/anything?id=[1-3]'",
  );
});

test('quotes a JSON body safely for the shell', () => {
  assert.equal(
    buildCurlCommand('POST', 'http://localhost:3000/api/inspector/anything', '{"text":"it\'s safe"}'),
    "curl --globoff --request 'POST' 'http://localhost:3000/api/inspector/anything' --header 'Content-Type: application/json' --data-raw '{\"text\":\"it'\\''s safe\"}'",
  );
});