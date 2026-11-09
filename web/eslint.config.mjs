// Lint of the web UI: catches what the browser only reports at run time
// (a name used but never imported, an import left unused). Run with
// scripts/checkjs.sh.
import globals from 'globals';

export default [
  {
    files: ['ui/js/**/*.js'],
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'module',
      globals: { ...globals.browser },
    },
    rules: {
      'no-undef': 'error',
      'no-unused-vars': ['error', { args: 'none', caughtErrors: 'none' }],
      'no-dupe-keys': 'error',
      'no-unreachable': 'error',
      'no-const-assign': 'error',
      'no-import-assign': 'error',
    },
  },
];
