# Runlink

Turn a simple task function into a shareable tool with minimal setup.

The owner writes a script that takes optional input, performs one bounded task, and produces output.
Runlink provides the web interface and sharing, while execution stays on the owner's machine.
The script can be written by a person or an AI agent, in any language, with as little adaptation as possible.
Sharing it should require very little work beyond writing the task logic.

Every link opens a simple interface where recipients provide input, run the task, follow progress, and receive the result.
Owners define the inputs, actions, outputs, and execution details recipients may access, allowing coworkers to use a specific capability without receiving the owner's broader access.
Links can expire or be revoked, and owners can limit and stop execution, including tasks already running.

Recipients need only a browser. Owners use a single Runlink binary to share existing scripts.
Keep the product focused on tasks that fit the input-function-output model, so publishing stays simple and recipients need no custom interface.

See [RATIONALE.md](RATIONALE.md) for the reasoning behind this focus.
