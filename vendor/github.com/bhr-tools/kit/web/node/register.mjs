// Node tests of an app import its modules, which import the kit by URL
// (/kit/js/dom.js, as the browser does). Run them with
//   node --import ./vendor/github.com/bhr-tools/kit/web/node/register.mjs --test 'tests/js/*.test.mjs'
// and /kit/… resolves to this copy of the kit.
import { register } from 'node:module';

register('./resolve.mjs', import.meta.url);
