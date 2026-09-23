export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (key === 'value') node.value = value;
    else if (key === 'checked' || key === 'disabled' || key === 'hidden') node[key] = value;
    else node.setAttribute(key, value === true ? '' : String(value));
  }
  for (const child of children.flat(Infinity)) {
    if (child === undefined || child === null || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function button(text, action, style = 'secondary', attrs = {}) {
  return el('button', {type: 'button', class: style, onclick: action, ...attrs}, text);
}

export function mount(target, ...children) {
  target.replaceChildren(...children.flat(Infinity).filter(child => child !== false && child !== null && child !== undefined));
}

export class APIError extends Error {
  constructor(message, status) { super(message); this.name = 'APIError'; this.status = status; }
}

export async function api(path, {method = 'GET', body, csrf, keepalive = false} = {}) {
  const headers = {Accept: 'application/json'};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (csrf) headers['X-CSRF-Token'] = csrf;
  let response, text;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 20000);
  try {
    response = await fetch(path, {method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', keepalive, signal: controller.signal});
    text = await response.text();
  } catch {
    if (controller.signal.aborted) throw new APIError('The server request timed out. Check your connection, then try again.', 0);
    throw new APIError('Could not reach the server. Check your connection, then try again.', 0);
  } finally {
    clearTimeout(timeout);
  }
  let data;
  try { data = text ? JSON.parse(text) : {}; } catch {
    throw new APIError(`The server returned an unreadable response (${response.status}). Please try again.`, response.status);
  }
  if (!response.ok) throw new APIError(data.error || `Request failed (${response.status}). Please try again.`, response.status);
  return data;
}

export function announce(text) {
  const node = document.getElementById('announcer');
  node.textContent = '';
  requestAnimationFrame(() => { node.textContent = text; });
}

export function notice(text, kind = 'error', actions = []) {
  const node = document.getElementById('notice');
  node.className = `message ${kind}`;
  node.replaceChildren(el('p', {}, text));
  if (actions.length) node.append(el('div', {class: 'actions'}, actions));
  node.hidden = false;
  announce(text);
}

export function clearNotice() { document.getElementById('notice').hidden = true; }

export function focusHeading(container = document.getElementById('app')) {
  const heading = container.querySelector('h1, h2');
  if (heading) { heading.tabIndex = -1; heading.focus(); }
}

export function field(label, input, hint) {
  const id = input.id;
  if (hint) input.setAttribute('aria-describedby', `${id}-hint`);
  return el('div', {class: 'field'}, el('label', {for: id}, label), input, hint && el('p', {id: `${id}-hint`, class: 'help'}, hint));
}

export function select(id, options, value, action) {
  const node = el('select', {id, onchange: action}, options.map(([v, label]) => el('option', {value: v}, label)));
  if (value !== undefined) node.value = value;
  return node;
}

export function formatDate(value) {
  if (!value) return 'No activity yet';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString(undefined, {dateStyle: 'medium', timeStyle: 'short'});
}

export function confirmDialog({title, message, confirm = 'Confirm', danger = false}) {
  return new Promise(resolve => {
    const heading = el('h2', {id: 'dialog-heading'}, title);
    const cancel = button('Cancel', () => finish(false));
    const dialog = el('dialog', {'aria-labelledby': 'dialog-heading'}, heading, el('p', {}, message), el('div', {class: 'actions'}, cancel, button(confirm, () => finish(true), danger ? 'danger' : 'primary')));
    function finish(result) { dialog.close(); dialog.remove(); resolve(result); }
    dialog.addEventListener('cancel', event => { event.preventDefault(); finish(false); });
    document.body.append(dialog);
    dialog.showModal();
    cancel.focus();
  });
}
