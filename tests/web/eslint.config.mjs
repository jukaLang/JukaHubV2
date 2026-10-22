// Bug-finding lint for the static web builder: undefined names, duplicate keys,
// unreachable code and similar mistakes, not style. It exists because a Dynamic
// List click handler read an `el` that was never in scope and threw on every
// click; `no-undef` finds that class of bug before a user does.
//
// Run from the repo root (CI does):
//   tests/web/node_modules/.bin/eslint -c tests/web/eslint.config.mjs \
//     script.js builder-device.js builder-gallery.js gallery/layouts.js
import globals from 'globals';

// builder-device.js and builder-gallery.js load as classic scripts after
// script.js and share its top-level scope, so these names are real globals.
const sharedFromScript = [
  'buildEditorConfig', 'showToast', 'announce', 'validateProject', 'openValidationPanel',
  'lastImportReport', 'normalizeImportedConfig', 'pushUndo', 'importedConfigSnapshot',
  'loadJukaApp', 'scheduleAutoSave', 'refreshReloadButtonState',
];

// `let` bindings in script.js that the other builder files assign to.
const writableFromScript = ['importedConfigSnapshot', 'lastImportReport'];

export default [
  {
    files: ['**/*.js'],
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'script',
      globals: { ...globals.browser },
    },
    rules: {
      'no-undef': 'error',
      'no-redeclare': ['error', { builtinGlobals: false }],
      'no-dupe-keys': 'error', 'no-dupe-else-if': 'error', 'no-dupe-args': 'error', 'no-duplicate-case': 'error',
      'no-unreachable': 'error', 'no-self-assign': 'error', 'no-cond-assign': 'error', 'use-isnan': 'error',
      'valid-typeof': 'error', 'no-fallthrough': 'error', 'no-const-assign': 'error', 'no-loss-of-precision': 'error',
      'no-sparse-arrays': 'error', 'no-unsafe-finally': 'error', 'no-unsafe-negation': 'error', 'no-unused-labels': 'error',
      'no-useless-backreference': 'error', 'getter-return': 'error', 'no-empty-character-class': 'error',
      'no-invalid-regexp': 'error', 'no-irregular-whitespace': 'error', 'no-misleading-character-class': 'error',
      'no-global-assign': 'error', 'no-shadow-restricted-names': 'error', 'no-delete-var': 'error', 'no-octal': 'error',
      'no-with': 'error', 'require-yield': 'error',
      // script.js deliberately wraps loadScene, loadJukaApp and clearAll after
      // defining them, so reassigning a function declaration is intended.
      'no-func-assign': 'off',
    },
  },
  {
    files: ['**/builder-device.js', '**/builder-gallery.js'],
    languageOptions: {
      globals: Object.fromEntries(sharedFromScript.map(n => [n, writableFromScript.includes(n) ? 'writable' : 'readonly'])),
    },
  },
];
