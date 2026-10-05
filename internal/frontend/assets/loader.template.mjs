const approvedGUI = Object.freeze(/* APPROVED_GUI */);

const status = document.getElementById('status');
const form = document.getElementById('secret-form');
status.textContent = window.runlinkSession.present()
  ? 'Link captured. Secure owner connections are not available yet.'
  : 'Enter the secret supplied by the owner.';
form.addEventListener('submit', event => {
  event.preventDefault();
  const field = document.getElementById('secret');
  const secret = field.value;
  field.value = '';
  window.runlinkSession.capture(secret);
  status.textContent = 'Secret captured. Secure owner connections are not available yet.';
});

export async function verifyGUI(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length > 65536) throw new Error('Invalid GUI bundle size');
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)),
    byte => byte.toString(16).padStart(2, '0')).join('');
  if (!approvedGUI.includes(hash)) throw new Error('GUI release is not approved');
  return hash;
}

// The authenticated transport will call this after peer authentication is implemented.
const urls = new Set();
const policy = window.trustedTypes?.createPolicy('runlink-gui', {
  createScriptURL(value) {
    if (!urls.has(value)) throw new Error('Unverified GUI URL');
    return value;
  }
});
export async function mountGUI(bytes) {
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
}
