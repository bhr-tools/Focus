// The kit's DOM helpers, with Focus's icons registered.

import { registerIcons } from '/kit/js/dom.js';
import { Icons } from './icons.js';

export { h, icon, clear, isMobile, isTextInput } from '/kit/js/dom.js';

registerIcons(Icons);
