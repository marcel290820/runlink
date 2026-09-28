# Rationale

A person automates a task with a script. The useful logic already exists, but sharing it creates more work: other people need to install and run the script, ask its author to run it for them, or wait for someone to build and deploy an interface around it.

The starting observation is that tools such as ngrok already make locally running applications shareable. The remaining problem is the task that has been automated but has no application around it. Its author should be able to share the working automation with very little additional effort.

The central abstraction is a simple function:

```text
optional input -> task function -> output
```

The function represents one bounded task. Someone supplies the required input, runs it, and receives a result. The implementation can be a script or compiled program in Python, JavaScript, TypeScript, Rust, Go, Java, C, C++, or another language. It does not need to be a literal function declaration or a mathematically pure function. A task can read or update approved resources, and its output can be a file, retrieved information, or confirmation of a completed action.

This common shape makes a common interface possible. Task Links can collect inputs, show execution status, and present results without requiring each author to design a frontend. The recipient interacts with the task through a browser. The author contributes the task logic; Task Links provides the interface and sharing, while execution stays on the owner's machine. This reuses the environment in which the automation already works.

This also fits an AI-assisted authoring workflow. A person can ask an agent to write a focused script for the task they want to automate. That script is the useful unit to create and maintain. Task Links supplies the surrounding product experience, so making the automation usable by others does not require generating and maintaining a separate web application.

Simplicity is the product constraint. Support tasks whose complete recipient interaction fits the function model: provide input, run, receive output. The implementation may contain several steps, but the recipient should not need a custom interaction flow. Tasks that require a bespoke interface or ongoing interactive decisions fall outside this scope.

Minimize the work between a script that already works and a coworker successfully using it. Infer routine setup where possible, and ask the owner only for decisions that cannot be inferred reliably, especially which inputs, actions, and results others are allowed to access. Keep necessary setup visible and understandable. When broader compatibility would make publishing substantially more complicated, narrow the supported task set.

The test is whether an author can share a useful task with minimal preparation and a recipient can complete it without the author's help. A polished interface supports that outcome; reducing setup effort and repeated handoffs is the reason the product exists.
