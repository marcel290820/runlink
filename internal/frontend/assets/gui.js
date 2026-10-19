// Runlink task GUI. The loader verifies these bytes against its approved hashes, runs
// them, and calls runlinkGUI.mount(root, task) once the owner's runner is authenticated.
// Definitions and results are owner data: they render as text, never as markup, and
// files leave the page only as downloads. Raster images may also show as a picture,
// which cannot run script or fetch anything.
//
// task.definition = { title, description?, owner?, output?, inputs? }
//   owner: the owner's display name, as in "Dana's machine"
//   output: { kind: 'text' | 'json' | 'file', label? }; label is a noun phrase, "a CSV report"
//   inputs: [{ name, type, label?, help?, placeholder?, required?, default?,
//              min?, max?, step?, maxLength?, options?, accept?, maxBytes? }]
//   type: text | multiline | number | date | boolean | choice | file
//   options: [value] or [{ value, label? }], where each value is a string, number, or boolean
//   Dates are YYYY-MM-DD strings. A property of the wrong type fails the whole definition.
// task.run(values, onProgress) settles when the run does. values maps each field name
// to its value; empty optional fields are left out and files are File objects.
//   onProgress({ stage: 'accepted' }) or onProgress({ stage: 'running', message?, fraction? })
//   resolves to [{ kind: 'text' | 'json', label?, value } | { kind: 'file', label?, name, blob }]
//   rejects with an Error whose code is invalid (with fields: { name: message }), failed,
//   timeout, stopped, disconnected, or unknown, and whose message the owner chose to expose.
(() => {
  'use strict';
  const SVG = 'http://www.w3.org/2000/svg';
  const DOT = ' · ';
  const KINDS = { file: ['file', 'a file'], text: ['text', 'text'], json: ['data', 'data'] };
  const ACRONYMS = new Set(['id', 'url', 'uri', 'eta', 'ip', 'sku', 'vat', 'api', 'uuid', 'pdf', 'csv', 'json', 'utc', 'gps', 'iban']);
  const NUMBER = /^[-+]?[\d,]*\.?\d+%?$/;
  const motion = matchMedia('(prefers-reduced-motion: no-preference)');
  // Fixed strokes on a 24-unit grid, drawn in the current text color.
  const ICONS = {
    alert: 'M12 8v5M12 16.5v.01M10.3 3.9 2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
    close: 'M6 6l12 12M18 6 6 18',
    copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
    data: 'M9 4C6 4 6 6 6 8s-1 4-3 4c2 0 3 2 3 4s0 4 3 4M15 4c3 0 3 2 3 4s1 4 3 4c-2 0-3 2-3 4s0 4-3 4',
    done: 'M5 12.5l4.5 4.5L19 7.5',
    download: 'M12 4v11M7 10l5 5 5-5M5 20h14',
    file: 'M6 3h8l4 4v14H6zM14 3v4h4',
    key: 'M11 12.5 20 3.5M16.5 7l2.5 2.5M14 9.5l2 2M8 20.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9z',
    lock: 'M7 11V8a5 5 0 0 1 10 0v3M5 11h14v10H5z',
    text: 'M5 6h14M5 10h14M5 14h10M5 18h7',
    upload: 'M12 16V5M7 10l5-5 5 5M5 20h14',
  };

  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(attrs ?? {})) {
      if (value == null || value === false) continue;
      if (key === 'text') node.textContent = value;
      else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
      else node.setAttribute(key, value === true ? '' : value);
    }
    node.append(...children.filter(child => child != null && child !== false && child !== ''));
    return node;
  }

  function svg(tag, attrs, ...children) {
    const node = document.createElementNS(SVG, tag);
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
    node.append(...children);
    return node;
  }

  const icon = name => svg('svg', { class: 'icon', viewBox: '0 0 24 24', 'aria-hidden': 'true' }, svg('path', { d: ICONS[name] }));

  // The helix that the page draws on its canvas, as a two-strand mark.
  function brand() {
    const mark = svg('svg', { class: 'mark', viewBox: '0 0 30 16', 'aria-hidden': 'true' },
      svg('path', { class: 'pair', d: 'M4.5 5.5v5M15 5v6M25.5 5.5v5' }),
      svg('path', { d: 'M1 3C8 3 8 13 15 13S22 3 29 3M1 13C8 13 8 3 15 3S22 13 29 13' }));
    return el('span', { class: 'brand' }, mark, el('span', {}, 'run', el('b', { text: 'link' })));
  }

  function clock(ms) {
    const tenths = Math.floor(ms / 100), seconds = Math.floor(tenths / 10);
    return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}.${tenths % 10}`;
  }

  function duration(ms) {
    if (ms < 999.5) return `${Math.max(1, Math.round(ms))} ms`;
    if (ms < 59950) return `${(ms / 1000).toFixed(1)} s`;
    const seconds = Math.round(ms / 1000);
    return `${Math.floor(seconds / 60)} min ${seconds % 60} s`;
  }

  function bytes(count) {
    if (count < 1000) return `${count} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let value = count / 1000, unit = 0;
    while (value >= 1000 && unit < units.length - 1) { value /= 1000; unit++; }
    return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
  }

  // Flat JSON reads as a list: "giftWrapped": false shows as "Gift wrapped" and "No".
  const words = key => {
    const text = key.replace(/([a-z\d])([A-Z][a-z])/g, (_, a, b) => `${a} ${b.toLowerCase()}`)
      .replace(/([a-z\d])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim()
      .split(' ').map(word => ACRONYMS.has(word.toLowerCase()) ? word.toUpperCase() : word).join(' ');
    return text ? text.charAt(0).toUpperCase() + text.slice(1) : key;
  };
  const plain = value => value === true ? 'Yes' : value === false ? 'No' : value === null ? 'None' : String(value);

  const tokens = accept => (accept ?? '').split(',').map(token => token.trim().toLowerCase()).filter(Boolean);
  const listed = (items, last) => items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} ${last} ${items.at(-1)}`;

  function accepts(accept, file) {
    const name = file.name.toLowerCase(), type = file.type.toLowerCase();
    const allowed = tokens(accept);
    return !allowed.length || allowed.some(token => token.startsWith('.') ? name.endsWith(token)
      : token.endsWith('/*') ? type.startsWith(token.slice(0, -1)) : type === token);
  }

  // Split CSV into rows, honouring quotes, and stop once enough rows are read. A blank
  // line is skipped; a record of one quoted empty field ("") is kept.
  function rows(text, limit) {
    const out = [];
    let row = [], cell = '', quoted = false, filled = false;
    const end = () => {
      if (filled || row.length) out.push([...row, cell]);
      row = [];
      cell = '';
      filled = false;
    };
    for (let i = 0; i < text.length && out.length < limit; i++) {
      const c = text[i];
      if (quoted) {
        if (c === '"' && text[i + 1] === '"') { cell += '"'; i++; } else if (c === '"') quoted = false; else cell += c;
      } else if (c === '"') { quoted = true; filled = true; }
      else if (c === ',') { row.push(cell); cell = ''; }
      else if (c === '\n') end();
      else if (c !== '\r' || text[i + 1] !== '\n') { cell += c; filled = true; }
    }
    // The last record may end without a newline, its last cell even empty.
    if (out.length < limit) end();
    return out;
  }

  // Raster formats a preview may show, recognized by their first bytes rather than the
  // owner's name or type. SVG is left out: it is a document, not a picture.
  const PICTURES = [['image/png', /^\x89PNG/], ['image/jpeg', /^\xff\xd8\xff/], ['image/gif', /^GIF8/],
    ['image/webp', /^RIFF[^]{4}WEBP/], ['image/avif', /^[^]{4}ftypavi[fs]/]];

  // A glance at a result file: the picture itself, the first rows of a CSV, or the first lines of text.
  async function peek(blob, name, keep) {
    const magic = String.fromCharCode(...new Uint8Array(await blob.slice(0, 12).arrayBuffer()));
    const picture = PICTURES.find(([, test]) => test.test(magic));
    if (picture) {
      const url = URL.createObjectURL(new Blob([blob], { type: picture[0] }));
      keep(url);
      const img = el('img', { class: 'picture', src: url, alt: `Preview of ${name}` });
      await img.decode();
      return el('div', { class: 'peek' }, img);
    }
    const lower = name.toLowerCase();
    const csv = blob.type === 'text/csv' || lower.endsWith('.csv');
    if (!csv && !blob.type.startsWith('text/') && !/\.(txt|log|md|json|tsv)$/.test(lower)) return null;
    const head = await blob.slice(0, 32768).text();
    if (csv) {
      const [header, ...body] = rows(head, 6);
      if (!body.length) return null;
      // A column of numbers lines up on the right, its heading included.
      const numeric = header.map((_, i) => body.some(row => row[i]) && body.every(row => !row[i] || NUMBER.test(row[i])));
      const cell = (tag, text, i, attrs) => el(tag, { ...attrs, class: numeric[i] ? 'num' : null, text });
      return el('div', { class: 'peek' }, el('p', { text: `First ${body.length} rows` }),
        el('div', { class: 'scroll' }, el('table', { class: 'sheet' },
          el('thead', {}, el('tr', {}, ...header.map((text, i) => cell('th', text, i, { scope: 'col' })))),
          el('tbody', {}, ...body.map(row => el('tr', {}, ...row.map((text, i) => cell('td', text, i))))))));
    }
    return el('div', { class: 'peek' }, el('p', { text: 'Beginning of the file' }),
      el('pre', { class: 'text', tabindex: 0, text: head.split('\n').slice(0, 12).join('\n') }));
  }

  // Every definition property the GUI reads is checked here, so a malformed definition
  // fails loudly instead of rendering a broken form.
  const isText = value => typeof value === 'string';
  // A date must exist, from year 1 on: the browser silently drops an impossible default or bound.
  const isDate = value => isText(value) && /^\d{4}-\d{2}-\d{2}$/.test(value) && !value.startsWith('0000') &&
    !Number.isNaN(Date.parse(value)) && new Date(value).toISOString().startsWith(value);
  const isFlag = value => typeof value === 'boolean';
  const isPlain = value => isText(value) || Number.isFinite(value) || isFlag(value);
  const isPositive = value => Number.isFinite(value) && value > 0;
  const maybe = test => value => value == null || test(value);
  // What each input type's value, and so its default, must be.
  const TYPES = { text: isText, multiline: isText, number: Number.isFinite, date: isDate, boolean: isFlag, choice: isPlain, file: () => false };
  const SPEC = { label: isText, help: isText, placeholder: isText, accept: isText, required: isFlag, step: isPositive,
    maxLength: value => Number.isInteger(value) && value > 0, maxBytes: isPositive };
  const isOption = option => isPlain(option) || option !== null && typeof option === 'object' && isPlain(option.value) && maybe(isText)(option.label);

  function validSpec(spec) {
    if (!isText(spec?.name) || !spec.name || !isText(spec.type) || !Object.hasOwn(TYPES, spec.type)) return false;
    const bound = maybe(spec.type === 'date' ? isDate : Number.isFinite);
    return Object.entries(SPEC).every(([key, test]) => maybe(test)(spec[key])) && maybe(TYPES[spec.type])(spec.default) &&
      bound(spec.min) && bound(spec.max) &&
      (spec.type !== 'choice' || Array.isArray(spec.options) && spec.options.length > 0 && spec.options.every(isOption));
  }

  function validate(definition) {
    const inputs = definition?.inputs ?? [];
    const output = definition?.output;
    const ok = isText(definition?.title) && definition.title.trim() !== '' && maybe(isText)(definition.description) &&
      maybe(isText)(definition.owner) && Array.isArray(inputs) && inputs.every(validSpec) &&
      new Set(inputs.map(spec => spec.name)).size === inputs.length &&
      (output == null || isText(output.kind) && Object.hasOwn(KINDS, output.kind) && maybe(isText)(output.label));
    if (!ok) throw new Error('Unsupported task definition');
  }

  function range(spec, noun) {
    if (spec.min != null && spec.max != null) return `Use a ${noun} from ${spec.min} to ${spec.max}.`;
    const date = noun === 'date';
    return spec.min != null ? `Use ${spec.min} or ${date ? 'later' : 'more'}.` : `Use ${spec.max} or ${date ? 'earlier' : 'less'}.`;
  }

  // One input: its markup, how to read and check it, where its error shows, and the
  // plain-text receipt that stands in for it while a run is in flight. Controls take
  // generated names, so an owner's field name can never shadow a form property.
  function field(spec, index) {
    const id = `input-${index}`;
    const label = spec.label?.trim() ? spec.label : spec.name;
    const help = spec.help ? el('p', { class: 'help', id: `${id}-help`, text: spec.help }) : null;
    const error = el('p', { class: 'error', id: `${id}-error` });
    const shown = el('span', { class: 'shown' });
    const describedby = [help?.id, error.id].filter(Boolean).join(' ');
    let options, chosen = -1;
    if (spec.type === 'choice') {
      options = spec.options.map(option => isPlain(option) ? { value: option, label: plain(option) }
        : { value: option.value, label: option.label || plain(option.value) });
      chosen = options.findIndex(option => option.value === spec.default);
      // A picked radio cannot be cleared, so an optional choice without a preset offers None.
      if (chosen < 0 && !spec.required) { options.unshift({ label: 'None' }); chosen = 0; }
    }
    // A switch, or a choice that starts selected, always has a value, so neither is marked optional.
    const preset = spec.type === 'choice' && options[chosen]?.value !== undefined;
    const title = [label, spec.required || spec.type === 'boolean' || preset ? null : el('span', { class: 'optional', text: 'optional' })];
    const common = { id, 'aria-describedby': describedby, required: spec.required === true };
    let node, control, read, marked;

    switch (spec.type) {
      case 'text':
      case 'multiline':
        control = spec.type === 'text'
          ? el('input', { ...common, class: 'control', type: 'text', autocomplete: 'off', placeholder: spec.placeholder, maxlength: spec.maxLength })
          : el('textarea', { ...common, class: 'control', rows: 4, placeholder: spec.placeholder, maxlength: spec.maxLength });
        control.value = spec.default ?? '';
        read = () => {
          const value = control.value;
          if (!value.trim()) return spec.required ? { error: 'Enter a value.' } : {};
          if (spec.maxLength && value.length > spec.maxLength) return { error: `Use ${spec.maxLength} characters or fewer.` };
          return { value };
        };
        break;
      case 'number':
        control = el('input', { ...common, class: 'control', type: 'number', inputmode: 'decimal', placeholder: spec.placeholder,
          min: spec.min, max: spec.max, step: spec.step ?? 'any' });
        control.value = spec.default == null ? '' : String(spec.default);
        read = () => {
          if (control.validity.badInput) return { error: 'Enter a number.' };
          if (control.value === '') return spec.required ? { error: 'Enter a number.' } : {};
          if (control.validity.rangeUnderflow || control.validity.rangeOverflow) return { error: range(spec, 'number') };
          if (control.validity.stepMismatch) return { error: spec.step === 1 ? 'Use a whole number.' : `Use steps of ${spec.step}.` };
          return { value: Number(control.value) };
        };
        break;
      case 'date':
        control = el('input', { ...common, class: 'control', type: 'date', min: spec.min, max: spec.max });
        control.value = spec.default ?? '';
        read = () => {
          if (control.validity.badInput) return { error: 'Enter a complete date.' };
          if (!control.value) return spec.required ? { error: 'Choose a date.' } : {};
          if (control.validity.rangeUnderflow || control.validity.rangeOverflow) return { error: range(spec, 'date') };
          // The receipt shows the date as the field did, in the reader's own format.
          const text = control.valueAsDate.toLocaleDateString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'UTC' });
          return { value: control.value, text };
        };
        break;
      case 'boolean':
        control = el('input', { ...common, class: 'switch', type: 'checkbox', role: 'switch', required: false });
        control.checked = spec.default === true;
        node = el('div', { class: 'field toggle' }, el('label', { for: id }, ...title), el('div', { class: 'slot' }, control, shown), help, error);
        read = () => ({ value: control.checked });
        break;
      case 'choice':
        if (options.length <= 4 && options.every(option => option.label.length <= 16)) {
          const radios = options.map((option, i) => el('input', { type: 'radio', name: id, value: i, checked: i === chosen }));
          control = radios[Math.max(chosen, 0)];
          marked = radios;
          node = el('fieldset', { class: 'field', 'aria-describedby': describedby }, el('legend', {}, ...title),
            el('div', { class: 'slot' },
              el('div', { class: 'segments' }, ...options.map((option, i) => el('label', {}, radios[i], el('span', { text: option.label })))),
              shown),
            help, error);
          read = () => {
            const picked = radios.findIndex(radio => radio.checked);
            if (picked < 0) return { error: 'Choose an option.' };
            return { value: options[picked].value, text: options[picked].label };
          };
        } else {
          control = el('select', { ...common, class: 'control' }, chosen < 0 ? el('option', { value: '', text: 'Choose one' }) : null,
            ...options.map((option, i) => el('option', { value: i, text: option.label })));
          control.value = chosen < 0 ? '' : String(chosen);
          read = () => {
            if (control.value === '') return { error: 'Choose an option.' };
            const option = options[Number(control.value)];
            return { value: option.value, text: option.label };
          };
        }
        break;
      case 'file': {
        const allowed = tokens(spec.accept);
        const limits = [allowed.length ? listed(allowed, 'or') : null, spec.maxBytes ? `up to ${bytes(spec.maxBytes)}` : null].filter(Boolean);
        const hint = limits.length ? el('small', { id: `${id}-limits`, text: limits.join(', ') }) : null;
        // The input stays in the tab order whichever view shows; its focus and error draw on that view.
        control = el('input', { ...common, class: 'file', type: 'file', accept: spec.accept,
          'aria-describedby': [hint?.id, describedby].filter(Boolean).join(' ') });
        const drop = el('div', { class: 'drop', onclick: () => control.click() }, icon('upload'),
          el('strong', {}, el('span', { text: 'Choose a file' }), ' or drop it here'), hint);
        const picked = el('div', { class: 'picked', hidden: true });
        const update = () => {
          const file = control.files[0];
          drop.hidden = Boolean(file);
          picked.hidden = !file;
          if (!file) return;
          picked.replaceChildren(icon('file'), el('span', { text: file.name }, el('small', { text: bytes(file.size) })),
            el('button', { type: 'button', 'aria-label': `Remove ${file.name}`, onclick: () => {
              control.value = '';
              update();
              invalid('');
              control.focus();
            } }, icon('close')));
        };
        control.addEventListener('change', update);
        const slot = el('div', { class: 'slot' }, control, drop, picked, shown);
        slot.addEventListener('dragover', event => { event.preventDefault(); drop.classList.add('over'); });
        slot.addEventListener('dragleave', () => drop.classList.remove('over'));
        slot.addEventListener('drop', event => {
          event.preventDefault();
          drop.classList.remove('over');
          const [file] = event.dataTransfer.files;
          if (!file || control.matches(':disabled')) return;
          const transfer = new DataTransfer();
          transfer.items.add(file);
          control.files = transfer.files;
          control.dispatchEvent(new Event('change', { bubbles: true }));
        });
        node = el('div', { class: 'field' }, el('label', { for: id }, ...title), slot, help, error);
        read = () => {
          const file = control.files[0];
          if (!file) return spec.required ? { error: 'Choose a file.' } : {};
          if (!accepts(spec.accept, file)) return { error: `Choose a ${listed(allowed, 'or')} file.` };
          if (spec.maxBytes && file.size > spec.maxBytes) return { error: `This file is ${bytes(file.size)}. The limit is ${bytes(spec.maxBytes)}.` };
          return { value: file, text: file.name };
        };
        break;
      }
    }
    node ??= el('div', { class: spec.type === 'number' || spec.type === 'date' ? 'field short' : 'field' },
      el('label', { for: id }, ...title), el('div', { class: 'slot' }, control, shown), help, error);
    marked ??= [control];

    function invalid(message) {
      error.replaceChildren(...message ? [icon('alert'), el('span', { text: message })] : []);
      for (const target of marked) {
        if (message) target.setAttribute('aria-invalid', 'true'); else target.removeAttribute('aria-invalid');
      }
    }
    const lock = ({ value, text }) => {
      shown.textContent = value === undefined ? 'None' : text ?? plain(value);
      shown.classList.toggle('empty', value === undefined);
    };
    node.addEventListener('input', () => invalid(''));
    node.addEventListener('change', () => invalid(''));
    return { name: spec.name, node, read, invalid, lock, focus: () => control.focus() };
  }

  function mount(root, task) {
    const definition = task?.definition;
    validate(definition);
    if (typeof task.run !== 'function') throw new Error('Task cannot run');
    // The log and text results are set in Martian Mono; fetch it now rather than at the first line.
    document.fonts.load('1em "Martian Mono"');

    const owner = definition.owner?.trim() ?? '';
    const machine = owner ? `${owner}'s machine` : "the owner's machine";
    const upper = text => text.charAt(0).toUpperCase() + text.slice(1);
    const [kindIcon, kindNoun] = KINDS[definition.output?.kind] ?? ['done', ''];
    const noun = definition.output ? definition.output.label?.trim() || kindNoun : '';
    const faults = {
      invalid: ['Rejected', 'Check your input', `${upper(machine)} didn't accept some of your input. Fix the highlighted fields, then run it again.`],
      failed: ['Failed', 'The task failed', `It stopped with an error on ${machine}. Try again, and if it keeps failing, let ${owner || 'the owner'} know.`],
      timeout: ['Timed out', 'The task took too long', `It reached its time limit on ${machine} and was stopped.`],
      stopped: ['Stopped', 'The run was stopped', `${owner || 'The owner'} stopped it on ${machine}.`],
      disconnected: ['Link lost', 'Connection lost', `The task may still be running on ${machine}. Running it again could repeat what it does.`],
      unknown: ['Unknown', "Can't confirm the outcome", `${upper(machine)} didn't confirm whether the task finished. Running it again could repeat what it does.`],
      unreadable: ['Unreadable', "The result can't be shown", `${upper(machine)} finished, but this page can't display what it sent.`],
    };

    const fields = (definition.inputs ?? []).map(field);
    const fieldset = fields.length
      ? el('fieldset', { class: 'fields' }, el('legend', { class: 'sr', text: 'Task input' }), ...fields.map(item => item.node))
      : el('p', { class: 'none', text: 'This task needs no input.' });
    const runButton = el('button', { class: 'primary run', type: 'submit', text: 'Run' });
    const sent = el('p', { class: 'sent', tabindex: -1, hidden: true, text: 'Sent. Keep this tab open until the result arrives.' });
    const form = el('form', { class: 'input', novalidate: true, 'aria-labelledby': 'input-heading' },
      el('div', { class: 'fields-wrap' }, el('h2', { class: 'sr', id: 'input-heading', text: 'Your input' }), fieldset),
      el('div', { class: 'run-row' }, runButton, sent));

    const readout = el('p', { class: 'readout', text: 'Ready' });
    const caption = el('p', { class: 'caption', text: 'Waiting for your input' });
    const log = el('ol', { class: 'log' });
    // The meter's scale reads --progress; the percentage shows only once the run reports one.
    const percent = el('span', { class: 'pct' });
    // The page's helix runs through the signal band and along both wires, and reads the
    // run from the bench's data-stage, data-fault, data-beat, and --progress.
    const machineNode = el('section', { class: 'machine', 'aria-labelledby': 'machine-heading' },
      el('div', { class: 'etch', 'aria-hidden': 'true' }),
      el('div', { class: 'readings' }, el('h2', { class: 'who', id: 'machine-heading', text: upper(machine) }), readout, caption,
        el('div', { class: 'meter', 'aria-hidden': 'true' }, el('span', { class: 'ticks' }), percent), log),
      el('div', { class: 'signal', 'data-helix': 'core', 'aria-hidden': 'true' }));

    const expect = () => el('div', { class: 'expect' }, icon(kindIcon),
      el('strong', { text: noun ? `You'll get ${noun}` : "You'll see when it's done" }),
      el('span', { text: noun ? 'It lands here when the run finishes.' : "This task doesn't send anything back." }));
    const result = el('div', { class: 'result' }, expect());
    const output = el('section', { class: 'output', 'aria-labelledby': 'output-heading' },
      el('h2', { class: 'tag', id: 'output-heading', text: 'Result' }), result);

    const bench = el('div', { class: 'bench', 'data-stage': 'ready' },
      el('h1', { class: 'title', text: definition.title }),
      definition.description ? el('p', { class: 'desc', text: definition.description }) : null,
      form,
      el('div', { class: 'wire w1', 'data-helix': 'link', 'aria-hidden': 'true' }),
      machineNode,
      el('div', { class: 'wire w2', 'data-helix': 'link', 'aria-hidden': 'true' }),
      output);
    const linked = el('span');
    const seal = el('p', { class: 'seal' }, icon('lock'), linked, el('span', { class: 'e2e', text: 'Encrypted end to end' }));
    const link = up => {
      seal.classList.toggle('down', !up);
      linked.replaceChildren(up ? 'Connected' : 'Disconnected', el('span', { class: 'to', text: ` ${up ? 'to' : 'from'} ${machine}` }));
    };
    link(true);
    const live = el('p', { class: 'sr', role: 'status' });
    root.replaceChildren(
      el('header', { class: 'bar' }, brand(), seal),
      el('main', { class: 'task' }, bench, live),
      el('footer', { class: 'foot' }, el('p', { class: 'notice' }, el('span', { class: 'badge' }, icon('key')),
        el('span', { text: 'This page saves nothing. Reloading it needs the original link or secret.' }))));
    // A file dropped beside the drop zone would replace this page and end the session.
    addEventListener('dragover', event => event.preventDefault());
    addEventListener('drop', event => event.preventDefault());

    let busy = false, runs = 0, started = 0, timer = 0, spoken = 0, beats = 0, downloads = [];
    // Leaving mid-run drops the live view and the session; ask first.
    addEventListener('beforeunload', event => { if (busy) event.preventDefault(); });
    const say = text => { live.textContent = text; spoken = Date.now(); };
    const retitle = prefix => { document.title = (prefix ? prefix + DOT : '') + definition.title + DOT + 'Runlink'; };
    const reveal = node => {
      const box = node.getBoundingClientRect();
      if (box.top < 0 || box.bottom > innerHeight) {
        node.scrollIntoView({ block: box.height > innerHeight ? 'start' : 'nearest', behavior: motion.matches ? 'smooth' : 'auto' });
      }
    };
    const show = (stage, big, small, time = false) => {
      bench.dataset.stage = stage;
      readout.textContent = big;
      readout.classList.toggle('time', time);
      caption.textContent = small;
      if (motion.matches) {
        readout.animate([{ opacity: 0, filter: 'blur(10px)', transform: 'translateY(.15em)' }, { opacity: 1, filter: 'blur(0)', transform: 'none' }],
          { duration: 450, easing: 'cubic-bezier(.2, .7, .2, 1)' });
      }
    };
    retitle('');

    function collect() {
      const read = fields.map(item => [item, item.read()]);
      let first = null;
      for (const [item, { error }] of read) {
        item.invalid(error ?? '');
        if (error) first ??= item;
      }
      if (first) {
        first.focus();
        return null;
      }
      for (const [item, entry] of read) item.lock(entry);
      // fromEntries defines plain properties, so a field named __proto__ stays data.
      return Object.fromEntries(read.filter(([, entry]) => entry.value !== undefined).map(([item, entry]) => [item.name, entry.value]));
    }

    // Acceptance and execution are separate: the clock and the signal start only when
    // the machine reports that the task is running.
    function progress(update) {
      if (!busy) return;
      const stage = bench.dataset.stage;
      if (update?.stage === 'accepted') {
        if (stage !== 'sending') return;
        show('accepted', 'Accepted', 'Waiting to start');
        retitle('Accepted');
        say(`${upper(machine)} accepted the run.`);
        return;
      }
      if (update?.stage !== 'running') return;
      if (stage !== 'running') {
        started = performance.now();
        bench.dataset.progress = 'unknown';
        show('running', clock(0), `Started at ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`, true);
        // The tab carries the clock too, for a reader waiting in another tab.
        timer = setInterval(() => {
          const time = clock(performance.now() - started);
          readout.textContent = time;
          retitle(`Running ${time.slice(0, 5)}`);
        }, 100);
        retitle('Running 00:00');
        say(`${upper(machine)} started the run.`);
      }
      if (typeof update.message === 'string' && update.message) {
        log.append(el('li', { 'data-t': clock(performance.now() - started), text: update.message }));
        while (log.children.length > 4) log.firstChild.remove();
        // Each message sends a pulse down the helix.
        bench.dataset.beat = String(++beats);
        if (Date.now() - spoken > 4000) say(update.message);
      }
      if (Number.isFinite(update.fraction)) {
        const fraction = Math.min(1, Math.max(0, update.fraction));
        delete bench.dataset.progress;
        bench.style.setProperty('--progress', String(fraction));
        percent.textContent = `${Math.floor(fraction * 100)}%`;
      }
    }

    function copy(text) {
      const label = el('span', { text: 'Copy' });
      return el('button', { class: 'quiet', type: 'button', onclick: async () => {
        try {
          await navigator.clipboard.writeText(text);
          label.textContent = 'Copied';
        } catch {
          label.textContent = "Couldn't copy";
        }
        setTimeout(() => { label.textContent = 'Copy'; }, 2000);
      } }, icon('copy'), label);
    }

    function render(item) {
      const label = typeof item?.label === 'string' && item.label ? el('p', { class: 'label', text: item.label }) : null;
      if (item?.kind === 'file' && item.blob instanceof Blob && typeof item.name === 'string' && item.name) {
        // A generic type keeps the browser from ever rendering owner content in this origin.
        const url = URL.createObjectURL(new Blob([item.blob], { type: 'application/octet-stream' }));
        downloads.push(url);
        const dot = item.name.lastIndexOf('.');
        const ext = dot > 0 && item.name.length - dot <= 6 ? item.name.slice(dot + 1).toUpperCase() : '';
        const save = el('a', { class: 'primary download', href: url, download: item.name }, icon('download'), `Download ${ext || 'file'}`);
        // A preview is optional: a file that cannot be read or decoded simply shows none.
        peek(item.blob, item.name, url => downloads.push(url)).then(preview => preview && save.after(preview), () => {});
        return [label, el('div', { class: 'doc' }, el('span', { class: 'ext', 'aria-hidden': 'true', text: ext || 'FILE' }),
          el('div', {}, el('p', { class: 'fname', text: item.name }), el('p', { class: 'fmeta', text: bytes(item.blob.size) }))), save];
      }
      if (item?.kind === 'text' && typeof item.value === 'string') {
        return [label, el('pre', { class: 'text', tabindex: 0, 'aria-label': 'Result text', text: item.value }),
          el('div', { class: 'actions' }, copy(item.value))];
      }
      if (item?.kind === 'json' && item.value !== undefined) {
        const value = item.value, pretty = JSON.stringify(value, null, 2);
        const keys = value !== null && typeof value === 'object' && !Array.isArray(value) ? Object.keys(value) : [];
        const flat = keys.length > 0 && keys.length <= 24 &&
          keys.every(key => value[key] === null || ['string', 'number', 'boolean'].includes(typeof value[key]));
        return [label, flat
          ? el('dl', { class: 'pairs' }, ...keys.flatMap(key => [el('dt', { text: words(key) }), el('dd', { text: plain(value[key]) })]))
          : el('pre', { class: 'code', tabindex: 0, 'aria-label': 'Result data', text: pretty }),
          el('div', { class: 'actions' }, copy(pretty))];
      }
      throw new Error('Unreadable result');
    }

    function finish(outputs, elapsed) {
      if (!Array.isArray(outputs)) throw new Error('Unreadable result');
      const nodes = outputs.length ? outputs.flatMap(render).filter(Boolean)
        : [el('p', { class: 'finished' }, icon('done'), el('span', { text: `Finished on ${machine}. This task has no result to show.` }))];
      result.replaceChildren(...nodes);
      percent.textContent = '';
      show('done', 'Done', `Took ${duration(elapsed)}`);
      runButton.textContent = 'Run again';
      runButton.classList.toggle('again', outputs.length > 0);
      retitle('Done');
      say(outputs.length ? `Done in ${duration(elapsed)}. The result is ready.` : `Done in ${duration(elapsed)}.`);
      reveal(output);
    }

    // Returns the first field the runner rejected, for focus once the form is editable again.
    function fail(error, elapsed) {
      const coded = isText(error?.code) && Object.hasOwn(faults, error.code);
      const code = coded ? error.code : 'unknown';
      const [short, heading, body] = faults[code];
      // Only a coded error carries a message the owner chose to show. It ends the run's log
      // and reads in full in the result.
      const detail = coded && typeof error.message === 'string' ? error.message.trim() : '';
      if (detail) log.append(el('li', { class: 'bad', 'data-t': clock(elapsed), text: detail }));
      else if (['failed', 'timeout', 'stopped'].includes(code)) log.lastElementChild?.classList.add('bad');
      while (log.children.length > 4) log.firstChild.remove();
      show('failed', short, `After ${duration(elapsed)}`);
      bench.dataset.fault = code;
      result.replaceChildren(el('div', { class: 'fault' }, el('strong', {}, icon('alert'), heading),
        detail ? el('p', { class: 'detail', text: detail }) : null, el('p', { text: body })));
      if (code === 'disconnected') link(false);
      runButton.textContent = code === 'invalid' ? 'Run' : 'Run again';
      retitle(code === 'invalid' ? '' : short);
      say(`${heading}. ${detail ? `${detail} ` : ''}${body}`);
      if (code !== 'invalid' || error.fields === null || typeof error.fields !== 'object') return null;
      const flagged = fields.filter(item => {
        const message = Object.hasOwn(error.fields, item.name) ? error.fields[item.name] : '';
        if (typeof message !== 'string' || !message) return false;
        item.invalid(message);
        return true;
      });
      return flagged[0] ?? null;
    }

    form.addEventListener('keydown', event => {
      if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        form.requestSubmit();
      }
    });
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy) return;
      const values = collect();
      if (!values) return;
      busy = true;
      const run = ++runs;
      downloads.forEach(url => URL.revokeObjectURL(url));
      downloads = [];
      // The form becomes a receipt of what was sent; focus waits on the status line.
      const focused = form.contains(document.activeElement);
      if (fields.length) fieldset.disabled = true;
      runButton.hidden = true;
      sent.hidden = false;
      if (focused) sent.focus();
      bench.classList.add('sent-out');
      delete bench.dataset.fault;
      delete bench.dataset.progress;
      bench.style.removeProperty('--progress');
      percent.textContent = '';
      link(true);
      log.replaceChildren();
      result.replaceChildren(expect());
      show('sending', 'Sending', 'Your input is on its way');
      retitle('Sending');
      say(`Sending your input to ${machine}.`);
      started = performance.now();
      reveal(machineNode);
      let rejected = null;
      try {
        // A late update from an earlier run never reaches this one.
        const outputs = await task.run(values, update => { if (run === runs) progress(update); });
        try {
          finish(outputs, performance.now() - started);
        } catch {
          fail({ code: 'unreadable' }, performance.now() - started);
        }
      } catch (error) {
        rejected = fail(error, performance.now() - started);
      } finally {
        clearInterval(timer);
        busy = false;
        bench.classList.remove('sent-out');
        if (fields.length) fieldset.disabled = false;
        const waiting = document.activeElement === sent || document.activeElement === document.body;
        sent.hidden = true;
        runButton.hidden = false;
        if (rejected) rejected.focus();
        else if (waiting) (output.querySelector('a, button') ?? runButton).focus({ preventScroll: true });
      }
    });
  }

  window.runlinkGUI = Object.freeze({ mount });
})();
