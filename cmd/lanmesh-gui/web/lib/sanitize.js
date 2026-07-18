const MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
export function esc(s) { return String(s).replace(/[&<>"']/g, c => MAP[c]); }
// Убираем управляющие, zero-width и bidi-override/isolate + BOM: иначе именем
// можно визуально подделать другой узел (U+202E разворачивает текст).
export function dispName(s) {
  return esc(String(s).replace(/[\u0000-\u001F\u200B-\u200F\u202A-\u202E\u2066-\u2069\uFEFF]/g, ''));
}
