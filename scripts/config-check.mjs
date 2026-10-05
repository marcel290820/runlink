import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { parseDocument } from 'yaml';
const path = '.github/workflows/check.yml';
const document = parseDocument(await readFile(path, 'utf8'), { uniqueKeys: true });
assert.equal(document.errors.length, 0, String(document.errors));
const workflow = document.toJS();
assert.deepEqual(workflow.permissions, { contents: 'read' });
assert.deepEqual(workflow.jobs.check.strategy.matrix.os, ['ubuntu-26.04', 'macos-26']);
for (const step of workflow.jobs.check.steps) {
  if (step.uses) assert(/@[0-9a-f]{40}$/.test(step.uses), 'Action must have an immutable pin');
  if (step.uses?.startsWith('actions/upload-artifact')) assert(step.with['retention-days'] <= 3);
}
assert(workflow.jobs.check.steps.some(step => step.run === 'scripts/check.sh'));
console.log('CI YAML, action pins, hosted matrix, permissions and retention passed');
