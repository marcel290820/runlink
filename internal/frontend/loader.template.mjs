const approvedGUI = Object.freeze(/* APPROVED_GUI */);

// The gate shows where the link stands until the verified GUI replaces it.
const gate = document.getElementById('gate');
const form = document.getElementById('secret-form');
const field = document.getElementById('secret');
const fieldError = document.getElementById('secret-error');
const note = document.getElementById('note');
const states = {
  secret: ['Enter the link secret', 'The owner sends it separately from the link.'],
  unavailable: ["Can't connect yet", "This version of Runlink can't reach the owner's machine, so nothing was sent. " +
    'Let the person who sent you the link know.'],
};
const reloadNote = 'You will need the secret again if you reload this page.';
const historyNote = 'This link contains its secret, so it can stay in your browser history and on synced devices. ' +
  'To keep it out, ask the owner to send the secret separately.';

function show(state, hint) {
  const [title, status] = states[state];
  gate.dataset.state = state;
  document.getElementById('gate-title').textContent = title;
  document.getElementById('status').textContent = status;
  form.hidden = state !== 'secret';
  note.textContent = hint;
  note.hidden = false;
}

if (window.runlinkSession.present()) {
  show('unavailable', historyNote);
} else {
  show('secret', reloadNote);
  field.focus();
}
field.addEventListener('input', () => {
  fieldError.textContent = '';
  field.removeAttribute('aria-invalid');
});
form.addEventListener('submit', event => {
  event.preventDefault();
  // Pasted secrets often carry stray whitespace; a generated secret never contains any.
  const secret = field.value.trim();
  field.value = '';
  if (!secret) {
    fieldError.textContent = 'Paste the secret the owner sent you.';
    field.setAttribute('aria-invalid', 'true');
    field.focus();
    return;
  }
  window.runlinkSession.capture(secret);
  show('unavailable', reloadNote);
});

export async function verifyGUI(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length > 65536) throw new Error('Invalid GUI bundle size');
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)),
    byte => byte.toString(16).padStart(2, '0')).join('');
  if (!approvedGUI.includes(hash)) throw new Error('GUI release is not approved');
  return hash;
}

// The authenticated transport will call this after peer authentication is implemented,
// with the task object that gui.js documents. The GUI registers runlinkGUI when it runs.
const urls = new Set();
const policy = window.trustedTypes?.createPolicy('runlink-gui', {
  createScriptURL(value) {
    if (!urls.has(value)) throw new Error('Unverified GUI URL');
    return value;
  }
});
export async function mountGUI(bytes, task) {
  await verifyGUI(bytes);
  const url = URL.createObjectURL(new Blob([bytes], { type: 'text/javascript' }));
  urls.add(url);
  try {
    await new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = policy ? policy.createScriptURL(url) : url;
      script.onload = () => { script.remove(); resolve(); };
      script.onerror = () => { script.remove(); reject(new Error('GUI loading failed')); };
      document.head.append(script);
    });
  } finally { urls.delete(url); URL.revokeObjectURL(url); }
  const gui = window.runlinkGUI;
  delete window.runlinkGUI;
  if (typeof gui?.mount !== 'function') throw new Error('GUI did not register');
  gui.mount(document.getElementById('task'), task);
  document.getElementById('gate-view').hidden = true;
}
