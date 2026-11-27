// Sound and screen while a session runs: the alarm (decoded once into a Web
// Audio buffer, reliable on phones once a gesture unlocked it; an <audio>
// element otherwise), the iOS stream that rings on the lock screen, the
// media controls and the screen wake lock.

const $ = (id) => document.getElementById(id);

export const IS_IOS = /iPad|iPhone|iPod/.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

let app = null;
let audioCtx = null;
let alarmBuffer = null;
let lastAlarmAt = 0;
let streamRemaining = null;
let wakeLock = null;

export function unlockAudio() {
  const Ctx = window.AudioContext || window.webkitAudioContext;
  if (!Ctx) return;
  if (!audioCtx) audioCtx = new Ctx();
  if (audioCtx.state === 'suspended') audioCtx.resume().catch(() => {});
  if (!alarmBuffer) {
    alarmBuffer = 'loading';
    fetch('alarm.mp3')
      .then((res) => res.arrayBuffer())
      .then((data) => audioCtx.decodeAudioData(data))
      .then((buffer) => { alarmBuffer = buffer; })
      .catch(() => { alarmBuffer = null; });
  }
}

export function playAlarm(alertType, force = false) {
  const t = Date.now();
  if (!force && t - lastAlarmAt < 4000) return; // the iOS stream may already ring
  lastAlarmAt = t;
  stopSessionAudio();
  unlockAudio();

  if (audioCtx && alarmBuffer instanceof AudioBuffer) {
    const source = audioCtx.createBufferSource();
    source.buffer = alarmBuffer;
    source.connect(audioCtx.destination);
    source.start();
  } else {
    const el = $('alarm-audio');
    el.currentTime = 0;
    el.play().catch(() => {});
  }

  const button = $('btn-sound-test');
  button.classList.add('ringing');
  setTimeout(() => button.classList.remove('ringing'), 1500);
  if (navigator.vibrate) navigator.vibrate([300, 150, 300, 150, 500]);

  if (alertType && 'Notification' in window && Notification.permission === 'granted') {
    const [title, body] = alertType === 'break_ended'
      ? ['Break over', 'Prepare a new session for deepwork.']
      : ['Deepwork session over', 'Time for a break.'];
    try {
      new Notification(title, { body, icon: 'icon-192.png' });
    } catch { /* not allowed in this context */ }
  }
}

// iOS suspends pages in the background: a server-made MP3 of silence
// followed by the alarm keeps playing on the lock screen and rings exactly
// when the countdown ends.
export function playSessionAudio() {
  if (!IS_IOS) return;
  const player = $('session-audio');
  const snap = app.state.timer;
  const pomodoro = !snap || snap.mode === 'pomodoro';
  if (pomodoro) {
    const remaining = snap && snap.remaining_seconds > 0 ? snap.remaining_seconds : (snap ? snap.work_duration_min * 60 : 3600);
    if (streamRemaining === null || player.paused || Math.abs(streamRemaining - remaining) > 3) {
      streamRemaining = remaining;
      player.loop = false;
      player.src = `/api/audio/session.mp3?remaining=${remaining}&t=${Date.now()}`;
    }
  } else if (!player.src.endsWith('silent.mp3')) {
    streamRemaining = 0;
    player.loop = true;
    player.src = 'silent.mp3';
  }
  player.play().catch(() => {});
}

export function stopSessionAudio() {
  const player = $('session-audio');
  if (!player.paused) player.pause();
  streamRemaining = null;
}

async function requestWakeLock() {
  if (!('wakeLock' in navigator) || wakeLock || document.visibilityState !== 'visible') return;
  try {
    wakeLock = await navigator.wakeLock.request('screen');
    wakeLock.addEventListener('release', () => { wakeLock = null; });
  } catch { /* denied */ }
}

function releaseWakeLock() {
  if (wakeLock) wakeLock.release().catch(() => {});
  wakeLock = null;
}

/** Follows a new snapshot: wake lock, iOS stream, media controls. */
export function updateMediaSession(snap, previous, fromThisDevice) {
  if (snap.status === 'running' && fromThisDevice) requestWakeLock();
  else releaseWakeLock();
  if (!IS_IOS) return;

  if (snap.status === 'running' && fromThisDevice) {
    const phaseChanged = previous && (previous.phase !== snap.phase || previous.status !== 'running');
    if ($('session-audio').paused || phaseChanged) playSessionAudio();
  } else if (snap.status === 'idle' || !fromThisDevice) {
    stopSessionAudio();
  } else if (Date.now() - lastAlarmAt > 4000) {
    stopSessionAudio(); // paused, unless the alarm is still ringing
  }

  if ('mediaSession' in navigator && snap.status !== 'idle') {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: `${snap.project} • ${snap.time_str}`,
      artist: snap.mode === 'free' ? 'Stopwatch' : snap.phase === 'break' ? 'Break' : 'Work',
      album: 'Focus',
    });
    navigator.mediaSession.playbackState = snap.status === 'running' ? 'playing' : 'paused';
  }
}

export function initAudio(theApp) {
  app = theApp;
  document.addEventListener('pointerdown', unlockAudio, { once: true, passive: true });
  document.addEventListener('keydown', unlockAudio, { once: true });
  document.addEventListener('click', () => {
    if ('Notification' in window && Notification.permission === 'default') Notification.requestPermission();
  }, { once: true });
  $('session-audio').addEventListener('timeupdate', (e) => {
    // The stream is ringing on its own: do not ring a second time.
    const p = e.target;
    if (p.duration && p.duration - p.currentTime <= 4) lastAlarmAt = Date.now();
  });
  if (IS_IOS && 'mediaSession' in navigator) {
    for (const [event, action] of [['play', 'toggle'], ['pause', 'toggle'], ['nexttrack', 'next']]) {
      try {
        navigator.mediaSession.setActionHandler(event, () => app.sendAction(action));
      } catch { /* unsupported action */ }
    }
  }
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && app.state.timer?.status === 'running') requestWakeLock();
  });
  $('btn-sound-test').addEventListener('click', () => playAlarm(null, true));
}
