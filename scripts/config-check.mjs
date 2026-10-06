// Enforce the CI workflow's security properties beyond actionlint.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { parseDocument } from 'yaml';

const document = parseDocument(await readFile('.github/workflows/check.yml', 'utf8'), { uniqueKeys: true });
assert.deepEqual(document.errors, []);
const { permissions, jobs: { check } } = document.toJS();
assert.deepEqual(permissions, { contents: 'read' });
assert.deepEqual(check.strategy.matrix.os, ['ubuntu-26.04', 'macos-26']);
for (const step of check.steps.filter(step => step.uses)) {
  assert.match(step.uses, /@[0-9a-f]{40}$/, 'actions must be pinned to a commit');
  if (step.uses.startsWith('actions/checkout')) assert.equal(step.with['persist-credentials'], false);
  if (step.uses.startsWith('actions/upload-artifact')) assert(step.with['retention-days'] <= 3);
}
assert(check.steps.some(step => step.run === 'scripts/check.sh'), 'CI must run the shared gate');
console.log('CI YAML, action pins, hosted matrix, permissions, credentials and retention passed');
