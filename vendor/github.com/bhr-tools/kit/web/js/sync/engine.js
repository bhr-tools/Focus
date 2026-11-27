// Offline sync engine. It keeps the last server snapshot and the queue of
// changes the server has not acknowledged yet, both in IndexedDB. The local
// state is the snapshot with the queued changes replayed on top: the server
// stays the authority, and every snapshot it sends corrects the local guess.

import { openStorage } from './idb.js';

const BATCH = 100;
const RETRY_SECONDS = [2, 5, 15, 30, 60];

/** Error of a sync request: kind is 'offline', 'auth' or 'server'. */
class SyncError extends Error {
  constructor(kind) {
    super(kind);
    this.kind = kind;
  }
}

/** Random UUID v4, also outside secure contexts (plain-HTTP LAN access). */
export function newId() {
  if (globalThis.crypto?.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const hex = [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export class SyncEngine {
  /**
   * dbName: IndexedDB name. empty: snapshot used before the first download.
   * fromSnapshot(snapshot) -> state. reduce(state, op) -> result: applies op
   * to state in place and returns what the matching API call used to return;
   * it throws (an ApiError) before changing anything when op cannot apply.
   */
  constructor({ dbName, empty, fromSnapshot, reduce, url = '/api/sync' }) {
    Object.assign(this, { dbName, empty, fromSnapshot, reduce, url });
    this.base = undefined; // last server snapshot, undefined before the first download
    this.etag = '';
    this.queue = [];       // [{ seq, id, type, args }], oldest first
    this.state = fromSnapshot(structuredClone(empty));
    this.status = 'synced'; // synced | syncing | offline | auth | error
    this.net = Promise.resolve();
    this.timer = null;
    this.retry = 0;
    this.started = false;
    // Hooks set by the app.
    this.onChange = () => {};        // the local state was replaced (server data or another tab)
    this.onStatus = () => {};        // ({ status, pending })
    this.onRejected = () => {};      // (messages) changes the server refused
    this.onAuthRequired = () => {};  // the session expired: show the login
  }

  /** True once a snapshot exists, downloaded now or in an earlier session. */
  get ready() {
    return this.base !== undefined;
  }

  get pending() {
    return this.queue.length;
  }

  /** Loads the saved snapshot and queue. Call once, before anything else. */
  async init() {
    this.storage = await openStorage(this.dbName);
    await this.load();
    if (typeof BroadcastChannel !== 'undefined') {
      this.channel = new BroadcastChannel(`${this.dbName}-sync`);
      this.channel.onmessage = async () => {
        await this.load();
        this.onChange();
        this.emitStatus();
      };
    }
  }

  async load() {
    this.base = await this.storage.get('snapshot');
    this.etag = (await this.storage.get('etag')) || '';
    this.queue = await this.storage.ops();
    this.rebuild();
  }

  /** Recomputes the local state: snapshot + queued changes. */
  rebuild() {
    this.state = this.fromSnapshot(structuredClone(this.base ?? this.empty));
    for (const op of this.queue) {
      try {
        this.reduce(this.state, op);
      } catch {
        // The server will refuse it too; its answer removes it from the queue.
      }
    }
  }

  /** Listens to the network and the page; schedules regular syncs. */
  start() {
    this.started = true;
    window.addEventListener('online', () => this.sync());
    window.addEventListener('offline', () => this.setStatus('offline'));
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') this.sync();
    });
    setInterval(() => {
      if (document.visibilityState === 'visible') this.sync();
    }, 60_000);
    this.emitStatus();
  }

  /**
   * Applies a change locally, queues it for the server and returns the
   * reducer's result. Throws when the change cannot apply.
   */
  async mutate(type, args) {
    const op = { id: newId(), type, args: structuredClone(args) };
    const before = this.state;
    const result = this.reduce(this.state, op);
    op.seq = await this.storage.addOp(op);
    this.queue.push(op);
    // A server answer may have replaced the state while the op was being
    // stored: that state does not know this change yet.
    if (this.state !== before) this.rebuild();
    this.channel?.postMessage('changed');
    this.emitStatus();
    this.schedule(300);
    return result;
  }

  /** Sends the queue, then fetches the latest data. Never throws. */
  sync() {
    clearTimeout(this.timer);
    this.timer = null;
    const run = async () => {
      try {
        if (this.queue.length) await this.push();
        else await this.pull();
        this.retry = 0;
        this.setStatus('synced');
      } catch (err) {
        if (!(err instanceof SyncError)) console.error(err);
        const kind = err instanceof SyncError ? err.kind : 'error';
        this.setStatus(kind === 'server' ? 'error' : kind);
        if (kind === 'auth') this.onAuthRequired();
        else this.schedule(1000 * RETRY_SECONDS[Math.min(this.retry++, RETRY_SECONDS.length - 1)]);
      }
    };
    // One request at a time, so an old snapshot never overwrites a newer one.
    this.net = this.net.then(run, run);
    return this.net;
  }

  schedule(ms) {
    if (!this.started) return; // tests call sync() themselves
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.sync(), ms);
  }

  async request(method, body) {
    const headers = {};
    if (body) headers['Content-Type'] = 'application/json';
    if (!body && this.etag && this.ready) headers['If-None-Match'] = this.etag;
    this.setStatus('syncing');
    let res;
    try {
      res = await fetch(this.url, {
        method, headers, credentials: 'same-origin', cache: 'no-store',
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch {
      throw new SyncError('offline');
    }
    if (res.status === 304) return null;
    if (res.status === 401) throw new SyncError('auth');
    if (!res.ok) throw new SyncError('server');
    return res.json();
  }

  async pull() {
    const data = await this.request('GET');
    if (data) await this.accept(data, []);
  }

  async push() {
    while (this.queue.length) {
      const batch = this.queue.slice(0, BATCH);
      const data = await this.request('POST', { ops: batch.map(({ id, type, args }) => ({ id, type, args })) });
      const rejected = data.results.filter((r) => r.status === 'invalid').map((r) => r.error);
      const answered = new Set(data.results.map((r) => r.id));
      await this.accept(data, batch.filter((op) => answered.has(op.id)));
      if (rejected.length) this.onRejected(rejected);
    }
  }

  /** Takes a server answer: new snapshot, minus the acknowledged changes. */
  async accept({ snapshot, etag }, acknowledged) {
    const done = new Set(acknowledged.map((op) => op.id));
    this.base = snapshot;
    this.etag = etag;
    this.queue = this.queue.filter((op) => !done.has(op.id));
    await this.storage.set('snapshot', snapshot);
    await this.storage.set('etag', etag);
    await this.storage.deleteOps(acknowledged.map((op) => op.seq));
    this.rebuild();
    this.channel?.postMessage('changed');
    this.onChange();
  }

  setStatus(status) {
    this.status = status;
    this.emitStatus();
  }

  emitStatus() {
    let status = this.status;
    if (typeof navigator !== 'undefined' && navigator.onLine === false) status = 'offline';
    this.onStatus({ status, pending: this.queue.length });
  }

  /** Forgets everything stored on this device (on logout). */
  async reset() {
    await this.storage.clear();
    this.base = undefined;
    this.etag = '';
    this.queue = [];
    this.rebuild();
    this.channel?.postMessage('changed');
  }
}
