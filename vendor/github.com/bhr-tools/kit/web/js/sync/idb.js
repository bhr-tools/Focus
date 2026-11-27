// Minimal IndexedDB storage for the sync engine: a key-value store "meta"
// (snapshot, etag) and an ordered queue "ops". When IndexedDB is unavailable
// (private window, Node tests), everything is kept in memory instead, so the
// app still works online.

/** Opens the database name, or an in-memory fallback. */
export async function openStorage(name) {
  if (typeof indexedDB === 'undefined') return memoryStorage();
  try {
    const db = await new Promise((resolve, reject) => {
      const req = indexedDB.open(name, 1);
      req.onupgradeneeded = () => {
        req.result.createObjectStore('meta');
        req.result.createObjectStore('ops', { keyPath: 'seq', autoIncrement: true });
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
      req.onblocked = () => reject(new Error('IndexedDB blocked'));
    });
    return idbStorage(db);
  } catch {
    return memoryStorage();
  }
}

function idbStorage(db) {
  const run = (store, mode, fn) => new Promise((resolve, reject) => {
    const tx = db.transaction(store, mode);
    const result = fn(tx.objectStore(store));
    tx.oncomplete = () => resolve(result?.result);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  });
  return {
    get: (key) => run('meta', 'readonly', (s) => s.get(key)),
    set: (key, value) => run('meta', 'readwrite', (s) => { s.put(value, key); }),
    ops: () => run('ops', 'readonly', (s) => s.getAll()),
    /** Stores op and returns its sequence number. */
    addOp: (op) => run('ops', 'readwrite', (s) => s.add(op)),
    deleteOps: (seqs) => run('ops', 'readwrite', (s) => { for (const seq of seqs) s.delete(seq); }),
    clear: () => new Promise((resolve, reject) => {
      const tx = db.transaction(['meta', 'ops'], 'readwrite');
      tx.objectStore('meta').clear();
      tx.objectStore('ops').clear();
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    }),
  };
}

function memoryStorage() {
  const meta = new Map();
  let ops = [];
  let seq = 0;
  return {
    get: async (key) => structuredClone(meta.get(key)),
    set: async (key, value) => { meta.set(key, structuredClone(value)); },
    ops: async () => structuredClone(ops),
    addOp: async (op) => { seq += 1; ops.push({ ...structuredClone(op), seq }); return seq; },
    deleteOps: async (seqs) => { const gone = new Set(seqs); ops = ops.filter((o) => !gone.has(o.seq)); },
    clear: async () => { meta.clear(); ops = []; },
  };
}
