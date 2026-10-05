(() => {
  let secret = location.hash.slice(1);
  history.replaceState(null, '', location.pathname + location.search);
  // Authentication will consume this once; neither secrets nor task data are persisted.
  window.runlinkSession = Object.freeze({
    capture(value) { secret = value; },
    consume() { const value = secret; secret = ''; return value; },
    present() { return secret.length > 0; }
  });
})();
