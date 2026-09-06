import { cacheThumbnail } from './results.mjs';
const cache = new Map();
const queue = [];
let active = 0;

export function thumbnail(session, index) {
  const key = `${session}:${index}`;
  if (cache.has(key)) return Promise.resolve(cache.get(key));
  return new Promise(resolve => { queue.push({ session, index, key, resolve }); drain(); });
}

function drain() {
  while (active < 2 && queue.length) {
    const task = queue.shift();
    active++;
    window.go.main.GUIApp.GetThumbnail(task.session, task.index)
      .catch(() => '')
      .then(value => { cacheThumbnail(cache, task.key, value); task.resolve(value); })
      .finally(() => { active--; drain(); });
  }
}
