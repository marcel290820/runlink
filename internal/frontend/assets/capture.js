(() => {
  let secret = location.hash.slice(1);
  history.replaceState(null, '', location.pathname + location.search);
  // The loader takes this once; neither secrets nor task data are persisted.
  window.runlinkSession = Object.freeze({
    consume() { const value = secret; secret = ''; return value; }
  });
})();
