// The host object this module was handed, held for its components to read
// (the setup function runs before any component is mounted, so there is no
// component for setContext to hang on).
import type { ModuleHost } from '@prusa/connect-ui/slots';

let held: ModuleHost | undefined;

export const holdHost = (host: ModuleHost): void => {
  if (host.signal.aborted) return;
  held = host;
  host.signal.addEventListener('abort', () => {
    if (held === host) held = undefined;
  }, { once: true });
};

export const heldHost = (): ModuleHost => {
  if (!held) throw new Error('the module drew before its setup ran');
  return held;
};
