(() => {
  const root = document.getElementById('task');
  const heading = document.createElement('h2');
  heading.textContent = 'Task workspace';
  const message = document.createElement('p');
  message.textContent = 'Task execution will be available after a secure owner connection.';
  root.replaceChildren(heading, message);
})();
