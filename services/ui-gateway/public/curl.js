'use strict';

function shellQuote(value) {
  return `'${String(value).replace(/'/g, "'\\''")}'`;
}

function buildCurlCommand(method, url, body) {
  const parts = ['curl', '--globoff', '--request', shellQuote(method), shellQuote(url)];
  if (body !== undefined) {
    parts.push('--header', shellQuote('Content-Type: application/json'), '--data-raw', shellQuote(body));
  }
  return parts.join(' ');
}

if (typeof module !== 'undefined' && module.exports) module.exports = { buildCurlCommand };
if (typeof window !== 'undefined') window.kubeplayBuildCurlCommand = buildCurlCommand;