// Resolve hook of register.mjs: /kit/<path> → the kit's web/<path>.
const root = new URL('../', import.meta.url);

export async function resolve(specifier, context, next) {
  if (specifier.startsWith('/kit/')) return next(new URL(specifier.slice('/kit/'.length), root).href, context);
  return next(specifier, context);
}
