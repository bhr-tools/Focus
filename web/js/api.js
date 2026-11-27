// HTTP client of the Focus API: every call returns the decoded JSON (or the
// response itself with raw) and throws an ApiError the user can read.

import { ApiError } from '/kit/js/errors.js';

export { ApiError };

let unauthorized = () => {};

/** Called when the server says the session is no longer valid. */
export function onUnauthorized(fn) {
  unauthorized = fn;
}

export async function api(path, { method = 'GET', body, raw = false } = {}) {
  const options = { method, credentials: 'same-origin', headers: {} };
  if (body instanceof FormData) {
    options.body = body;
  } else if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, options);
  } catch {
    throw new ApiError('Cannot reach the server. Check your connection.', 0);
  }
  if (res.status === 401 && !path.startsWith('/api/auth/')) unauthorized();
  if (!res.ok) {
    const text = (await res.text()).trim();
    let message = text;
    try { message = JSON.parse(text).error || text; } catch { /* plain text */ }
    throw new ApiError(message || `Request failed (${res.status})`, res.status);
  }
  return raw ? res : res.json();
}
