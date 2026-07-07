import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
  },
  {
    // ui.tsx is an intentional primitive barrel exporting components alongside
    // helper functions (buttonVariants, cn re-exports). Fast-refresh HMR
    // boundary correctness is low-value here, so the rule is turned off.
    files: ['src/components/ui.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
])
